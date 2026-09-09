package subtitles

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// whisperGen wraps the bundled whisper.cpp CLI for local AI subtitle generation. It's inert when
// the binary or a model isn't present — the module then reports AI as unavailable instead of
// failing, so everything builds/runs before the Dockerfile bundles whisper.
type whisperGen struct {
	bin string // whisper-cli path: the portable build, Vulkan or CPU ("" = not installed)
	// sycl is the Intel oneAPI build (whisper-cli-sycl), tried first when it's bundled. On
	// an Arc card it's the only build that uses the matrix units: ggml's Vulkan backend
	// keeps them off for Alchemist. "" when the image doesn't carry it.
	sycl        string
	modelsDir   string // where the GGML model files live (data dir / whisper)
	noGPUFlag   bool   // whether this build understands --no-gpu (i.e. was built with a GPU backend)
	noFallback  bool   // --no-fallback available
	suppressNST bool   // --suppress-nst available
	dtwFlag     bool   // --dtw (token timestamps by DTW over the attention heads) available
	jsonFull    bool   // --output-json-full available
	flashAttn   bool   // --flash-attn available (a real speedup on GPU backends)
	dlMu        sync.Mutex
	dl          map[string]bool // model filenames currently downloading

	// backend is what the last run actually used: "vulkan", "cpu", or "" before any run.
	// Learned from whisper-cli's own output rather than guessed from the build, since a
	// Vulkan build on a host with no visible device silently runs on the CPU.
	backendMu sync.Mutex
	backend   string
	device    string // the GPU line whisper printed ("Intel(R) Arc(tm) A380 ... matrix cores: none")

	// syclOff is set the first time the oneAPI build fails or runs without a GPU; from then
	// on the portable build is used, and syclWhy says what happened. Sticky for the life of
	// the process: a broken driver doesn't fix itself between episodes, and retrying it on
	// every chunk would cost a model load each time.
	syclOff bool
	syclWhy string
	note    func(msg string) // where to report the switch, when the service wants to hear it
}

// pickBin is the build to run: the oneAPI one while it works, else the portable one.
func (w *whisperGen) pickBin() string {
	w.backendMu.Lock()
	defer w.backendMu.Unlock()
	if w.sycl != "" && !w.syclOff {
		return w.sycl
	}
	return w.bin
}

// disableSycl retires the oneAPI build for this process and says why, once.
func (w *whisperGen) disableSycl(why string) {
	w.backendMu.Lock()
	already := w.syclOff
	w.syclOff, w.syclWhy = true, why
	note := w.note
	w.backendMu.Unlock()
	if !already && note != nil {
		note("AI: Intel oneAPI (SYCL) whisper build set aside, using the Vulkan/CPU build instead — " + why)
	}
}

// SyclNote is why the oneAPI build isn't in use ("" while it is, or was never bundled).
func (w *whisperGen) SyclNote() string {
	if w == nil {
		return ""
	}
	w.backendMu.Lock()
	defer w.backendMu.Unlock()
	return w.syclWhy
}

// Device is the GPU whisper reported, with its capability flags — "matrix cores:
// none" on that line is the single biggest reason a Vulkan run is slow.
func (w *whisperGen) Device() string {
	if w == nil {
		return ""
	}
	w.backendMu.Lock()
	defer w.backendMu.Unlock()
	return w.device
}

// deviceOf pulls the device line out of whisper's output. The Vulkan backend prints
// "ggml_vulkan: 0 = <device> (<driver>) | uma: .. | fp16: .. | matrix cores: <mode>";
// the SYCL backend prints a table whose first row is
// "| 0| [level_zero:gpu:0]| Intel Arc A380 Graphics| 12.55| 128| 1024| 32| 6001M| 1.6.33276|"
// (type, name, version, compute units, work-group, sub-group, memory, driver), and a
// separate "SYCL_USE_XMX: yes" line for whether the build targets the matrix units.
func deviceOf(out []byte) string {
	xmx := ""
	if i := bytes.Index(out, []byte("SYCL_USE_XMX: ")); i >= 0 {
		rest := out[i+len("SYCL_USE_XMX: "):]
		if j := bytes.IndexByte(rest, '\n'); j >= 0 {
			rest = rest[:j]
		}
		xmx = strings.TrimSpace(string(rest))
	}
	for _, line := range bytes.Split(out, []byte("\n")) {
		for _, p := range []string{"ggml_vulkan: 0 = ", "Vulkan0: "} {
			if i := bytes.Index(line, []byte(p)); i >= 0 {
				return strings.TrimSpace(string(line[i+len(p):]))
			}
		}
		if t := bytes.TrimSpace(line); bytes.HasPrefix(t, []byte("| 0|")) {
			f := strings.Split(string(t), "|")
			if len(f) < 10 {
				continue
			}
			for k := range f {
				f[k] = strings.TrimSpace(f[k])
			}
			d := fmt.Sprintf("%s %s | %s CUs | %s | driver %s", f[3], f[2], f[5], f[8], f[9])
			if xmx != "" {
				d += " | XMX: " + xmx
			}
			return d
		}
	}
	return ""
}

// Backend reports which compute backend the AI last ran on, for the status panel.
func (w *whisperGen) Backend() string {
	if w == nil {
		return ""
	}
	w.backendMu.Lock()
	defer w.backendMu.Unlock()
	return w.backend
}

func (w *whisperGen) setBackend(b string) {
	w.backendMu.Lock()
	w.backend = b
	w.backendMu.Unlock()
}

func (w *whisperGen) setDevice(d string) {
	w.backendMu.Lock()
	w.device = d
	w.backendMu.Unlock()
}

// threads is how many CPU threads to give whisper-cli. Its default is 4, which on a
// 24-core host left five-sixths of the machine idle and made a 45-minute episode take
// most of an hour. Diminishing returns past ~16 on the decoder, so cap there.
func threads() int {
	n := runtime.NumCPU()
	if n > 16 {
		n = 16
	}
	if n < 1 {
		n = 1
	}
	return n
}

// backendOf reads which backend whisper-cli reported using. whisper says
// "whisper_backend_init_gpu: using SYCL0 backend" / "using Vulkan0 backend" when it has
// a GPU and "no GPU found" when it hasn't; the Vulkan build also prints a
// "ggml_vulkan: Found N Vulkan devices" line at startup when it has a device.
func backendOf(out []byte) string {
	switch {
	case bytes.Contains(out, []byte("no GPU found")):
		return "cpu"
	case bytes.Contains(out, []byte("using SYCL")):
		return "sycl"
	case bytes.Contains(out, []byte("using Vulkan")), bytes.Contains(out, []byte("ggml_vulkan: Found")), bytes.Contains(out, []byte("Vulkan0")):
		return "vulkan"
	}
	return "cpu"
}

// GGML model filenames (from ggerganov/whisper.cpp on Hugging Face). turbo is fast and used
// for same-language transcription; large-v3 is required for translate-to-English (turbo can't
// translate).
const (
	modelTurbo = "ggml-large-v3-turbo.bin"
	modelLarge = "ggml-large-v3.bin"
)

// dtwPreset names the model's alignment-head preset for --dtw, which is how whisper-cli
// produces per-token timestamps worth having.
func dtwPreset(model string) string {
	switch filepath.Base(model) {
	case modelTurbo:
		return "large.v3.turbo"
	case modelLarge:
		return "large.v3"
	}
	return ""
}

func detectWhisper(modelsDir string) *whisperGen {
	bin, _ := exec.LookPath("whisper-cli")
	sycl, _ := exec.LookPath("whisper-cli-sycl")
	if bin == "" {
		// Only the oneAPI build: it's the one and only build, with nothing to fall back to.
		bin, sycl = sycl, ""
	}
	w := &whisperGen{bin: bin, sycl: sycl, modelsDir: modelsDir, dl: map[string]bool{}}
	if bin != "" {
		// Flags differ between whisper.cpp versions; an unknown one makes whisper-cli print
		// usage and do nothing. Probe the help text once and pass only what it knows. Both
		// builds come from the same whisper.cpp tag, so one probe covers them.
		out, _ := exec.Command(bin, "--help").CombinedOutput()
		w.noGPUFlag = strings.Contains(string(out), "--no-gpu")
		w.noFallback = strings.Contains(string(out), "--no-fallback")
		w.suppressNST = strings.Contains(string(out), "--suppress-nst")
		w.flashAttn = strings.Contains(string(out), "--flash-attn")
		w.dtwFlag = strings.Contains(string(out), "--dtw")
		w.jsonFull = strings.Contains(string(out), "--output-json-full")
	}
	return w
}

func (w *whisperGen) hasModel(name string) bool {
	if w == nil || w.modelsDir == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(w.modelsDir, name))
	return err == nil && fi.Size() > 0
}

// available reports whether generation can actually run (a binary + at least one usable
// model). detectWhisper guarantees bin is set whenever any build is present.
func (w *whisperGen) available() bool {
	return w != nil && w.bin != "" && (w.hasModel(modelTurbo) || w.hasModel(modelLarge))
}

// modelPath picks the model for the task: translate-to-English requires large-v3 (turbo can't
// translate); same-language transcription prefers turbo and falls back to large-v3. "" = no
// suitable model.
func (w *whisperGen) modelPath(translate bool) string {
	if translate {
		if w.hasModel(modelLarge) {
			return filepath.Join(w.modelsDir, modelLarge)
		}
		return ""
	}
	if w.hasModel(modelTurbo) {
		return filepath.Join(w.modelsDir, modelTurbo)
	}
	if w.hasModel(modelLarge) {
		return filepath.Join(w.modelsDir, modelLarge)
	}
	return ""
}

// audioStreamFor picks the audio stream the AI should listen to: for a transcription,
// the first track in the wanted language (a commentary track only if nothing else is);
// for a translation, the track flagged default, else the first. -1 means ffmpeg's own
// default, used when the file's streams aren't known.
func audioStreamFor(mi *mediaInfo, lang string, translate bool) (int, AudioTrack) {
	if mi == nil || len(mi.Audio) == 0 {
		return -1, AudioTrack{}
	}
	if !translate {
		pick := -1
		for _, t := range mi.Audio {
			if !langMatches(t.Lang, lang) {
				continue
			}
			if strings.Contains(strings.ToLower(t.Title), "commentary") {
				if pick < 0 {
					pick = t.Index
				}
				continue
			}
			return t.Index, t
		}
		if pick >= 0 {
			return pick, mi.Audio[pick]
		}
		return -1, AudioTrack{}
	}
	for _, t := range mi.Audio {
		if t.Default {
			return t.Index, t
		}
	}
	return mi.Audio[0].Index, mi.Audio[0]
}

// generate produces an SRT for one language from a video's audio: extract 16 kHz mono,
// cut it into chunks at silences (chunks.go), run whisper.cpp on each with DTW word
// timestamps, and build the cues from the timed words (words.go). translate=true asks
// whisper to translate the (foreign) audio to English. audioStream is the 0:a:N stream
// to transcribe, or -1 for ffmpeg's default.
func (w *whisperGen) generate(ctx context.Context, ffmpeg, videoPath, srtPath, lang string, translate bool, audioStream int, progress func(pct int)) error {
	model := w.modelPath(translate)
	if model == "" {
		return fmt.Errorf("no whisper model available for %s", ifElse(translate, "translation", "transcription"))
	}
	if !w.jsonFull {
		return fmt.Errorf("this whisper-cli build has no --output-json-full; rebuild the image (whisper.cpp v1.9+)")
	}
	// Work in the temp dir under a clean (space-free) base.
	stamp := time.Now().UnixNano()
	base := filepath.Join(os.TempDir(), fmt.Sprintf("whisper-%d", stamp))
	wav := base + ".wav"
	defer os.Remove(wav)

	// 1. Extract mono 16 kHz PCM — what whisper expects — from the chosen audio stream,
	// and confirm it's real audio.
	extract := []string{"-y", "-hide_banner", "-i", videoPath}
	if audioStream >= 0 {
		extract = append(extract, "-map", fmt.Sprintf("0:a:%d", audioStream))
	}
	extract = append(extract, "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wav)
	if out, err := exec.CommandContext(ctx, ffmpeg, extract...).CombinedOutput(); err != nil {
		return fmt.Errorf("extract audio: %w: %s", err, tailStr(out, 300))
	}
	if fi, err := os.Stat(wav); err != nil || fi.Size() < 4096 {
		return fmt.Errorf("extracted audio was empty (no decodable audio track?)")
	}
	total := wavDuration(wav)

	// 2. Cut at silences. A failed detection just means one chunk.
	sils, err := detectSilences(ctx, ffmpeg, wav, total)
	if err != nil && ctx.Err() != nil {
		return err
	}
	chunks := planChunks(total, sils)

	// 3. Transcribe each chunk. Timestamps come back relative to the chunk; the chunk's
	// start is added when the words are read.
	var words []word
	noGPU := false
	dtw := w.dtwFlag
	bin := w.pickBin()
	for i, c := range chunks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		cwav := fmt.Sprintf("%s-c%d.wav", base, i)
		outB := fmt.Sprintf("%s-c%d", base, i)
		if err := cutChunk(ctx, ffmpeg, wav, c, cwav); err != nil {
			return err
		}
		prog := func(pct int) {
			if progress != nil {
				progress(overallProgress(c, pct, total))
			}
		}
		run := func(b string) ([]byte, error) {
			args := w.args(model, cwav, outB, lang, translate, dtw)
			if noGPU {
				args = append(args, "--no-gpu")
			}
			return runWhisper(ctx, b, args, prog)
		}
		out, err := run(bin)
		if err != nil && bin == w.sycl && ctx.Err() == nil {
			// The oneAPI build failed outright (no Level Zero device, a driver the runtime
			// doesn't like, out of memory). Retire it and carry on with the portable build,
			// for this chunk and everything after.
			w.disableSycl("it failed: " + tailStr(out, 300))
			bin = w.bin
			out, err = run(bin)
		}
		if err != nil && dtw && ctx.Err() == nil {
			// DTW needs the model's alignment-head preset and the backend's cooperation;
			// if this build refuses, the words fall back to segment timing rather than
			// the whole file failing.
			dtw = false
			out, err = run(bin)
		}
		if err != nil && !noGPU && w.noGPUFlag && ctx.Err() == nil {
			// A GPU build that can't initialise its device (driver missing in the container,
			// /dev/dri not passed through, an out-of-memory on a small card) fails outright
			// rather than degrading. Retry on the CPU: slower, but it produces the subtitle.
			noGPU = true
			out, err = run(bin)
		}
		os.Remove(cwav)
		if err != nil {
			os.Remove(outB + ".json")
			return fmt.Errorf("whisper: %w: %s", err, tailStr(out, 400))
		}
		// Recorded per chunk, so the status reflects whatever ran last.
		if noGPU {
			w.setBackend("cpu")
			w.setDevice("")
		} else {
			w.setBackend(backendOf(out))
			w.setDevice(deviceOf(out))
		}
		if bin == w.sycl && !noGPU && backendOf(out) != "sycl" && ctx.Err() == nil {
			// It ran, but on the CPU: the oneAPI build saw no GPU. This chunk's output is
			// fine; the portable build (which may have Vulkan) takes over from the next one.
			w.disableSycl("it found no Level Zero GPU. Check /dev/dri is passed to the container and that `clinfo` inside it lists the card; the Vulkan build is used meanwhile")
			bin = w.bin
		}
		data, rerr := os.ReadFile(outB + ".json")
		os.Remove(outB + ".json")
		if rerr != nil {
			return fmt.Errorf("whisper produced no output for chunk %d: %s", i+1, tailStr(out, 400))
		}
		ws, perr := wordsFromJSON(data, c.start)
		if perr != nil {
			return perr
		}
		words = append(words, ws...)
	}
	if len(words) == 0 {
		return fmt.Errorf("whisper found no speech in the audio")
	}

	// 4. Words → cues → SRT, minus stock-phrase hallucinations.
	srt := filterStockPhrases(formatSRT(shapeWordCues(words)))
	return os.WriteFile(srtPath, []byte(srt), 0o644)
}

// args builds the whisper-cli command line for one chunk.
//
//   - -ojf: full JSON with per-token timestamps (what the cues are built from).
//   - -dtw: token timestamps aligned over the attention heads rather than whisper's
//     experimental estimate — the difference between a word timed to the audio and
//     one timed to where the decoder happened to be.
//   - -nf: temperature fallback re-decodes a window up to five more times when the
//     first decode looks poor; the windows that look poor are music and noise, where
//     every retry invents the same line. Off.
//   - --suppress-nst: no ♪ / [Music] tokens.
//   - -pp: progress lines, which runWhisper turns into the job's progress.
func (w *whisperGen) args(model, wav, outBase, lang string, translate, dtw bool) []string {
	args := []string{"-m", model, "-f", wav, "-ojf", "-of", outBase, "-t", strconv.Itoa(threads()), "-pp"}
	if translate {
		args = append(args, "--translate")
	} else if lang != "" {
		args = append(args, "-l", lang)
	}
	if dtw {
		if p := dtwPreset(model); p != "" {
			args = append(args, "-dtw", p)
		}
	}
	if w.noFallback {
		args = append(args, "-nf")
	}
	if w.suppressNST {
		args = append(args, "--suppress-nst")
	}
	// Flash attention fuses the attention kernels; on the GPU backends it is a
	// straightforward speedup with the same output.
	if w.flashAttn {
		args = append(args, "-fa")
	}
	return args
}

// progressRe matches whisper-cli's -pp output: "whisper_print_progress_callback: progress =  25%".
var progressRe = regexp.MustCompile(`progress\s*=\s*(\d{1,3})%`)

// runWhisper runs whisper-cli, streaming its output so progress lines reach the caller
// while it runs, and returns the full combined output for diagnostics afterwards. The old
// CombinedOutput held everything until exit — fine for errors, useless for a bar.
func runWhisper(ctx context.Context, bin string, args []string, progress func(int)) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		return nil, err
	}
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		last := -1
		for sc.Scan() {
			line := sc.Bytes()
			buf.Write(line)
			buf.WriteByte('\n')
			if progress == nil {
				continue
			}
			if m := progressRe.FindSubmatch(line); m != nil {
				if pct, err := strconv.Atoi(string(m[1])); err == nil && pct != last && pct >= 0 && pct <= 100 {
					last = pct
					progress(pct)
				}
			}
		}
	}()
	err := cmd.Wait()
	pw.Close()
	<-done
	return buf.Bytes(), err
}

// tailStr returns the last n bytes of out as a single trimmed line (for error diagnostics).
func tailStr(out []byte, n int) string {
	s := strings.TrimSpace(string(out))
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return strings.ReplaceAll(s, "\n", " ⏎ ")
}

// aiPlan decides how (if at all) AI can produce a wanted-language subtitle from a file's audio:
//
//	"transcribe" — the audio is already in that language;
//	"translate"  — the target is English and the audio isn't (Whisper's only translation direction),
//	               or the audio language is unknown (auto-detect + translate is a safe default);
//	""           — impossible: Whisper can't translate into a non-English language.
func aiPlan(audioLangs []string, wanted string) string {
	for _, a := range audioLangs {
		if langMatches(a, wanted) {
			return "transcribe"
		}
	}
	if isEnglish(wanted) {
		return "translate"
	}
	return ""
}

func isEnglish(l string) bool {
	l = strings.ToLower(strings.TrimSpace(l))
	return l == "en" || l == "eng" || l == "english"
}

func ifElse(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}
