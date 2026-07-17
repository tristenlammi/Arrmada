package subtitles

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// whisperGen wraps the bundled whisper.cpp CLI for local AI subtitle generation. It's inert when
// the binary or a model isn't present — the module then reports AI as unavailable instead of
// failing, so everything builds/runs before the Dockerfile bundles whisper.
type whisperGen struct {
	bin       string // whisper-cli path ("" = not installed)
	modelsDir string // where the GGML model files live (data dir / whisper)
	dlMu      sync.Mutex
	dl        map[string]bool // model filenames currently downloading
}

// GGML model + VAD filenames (from ggerganov/whisper.cpp on Hugging Face). turbo is fast and used
// for same-language transcription; large-v3 is required for translate-to-English (turbo can't
// translate). silero is the VAD model that suppresses non-speech hallucination.
const (
	modelTurbo = "ggml-large-v3-turbo.bin"
	modelLarge = "ggml-large-v3.bin"
	vadModel   = "ggml-silero-v5.1.2.bin"
)

func detectWhisper(modelsDir string) *whisperGen {
	bin, _ := exec.LookPath("whisper-cli")
	return &whisperGen{bin: bin, modelsDir: modelsDir, dl: map[string]bool{}}
}

func (w *whisperGen) hasModel(name string) bool {
	if w == nil || w.modelsDir == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(w.modelsDir, name))
	return err == nil && fi.Size() > 0
}

// available reports whether generation can actually run (binary + at least one usable model).
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

// generate produces an SRT for one language from a video's audio: extract 16 kHz mono → run
// whisper.cpp (VAD-fronted when the VAD model is present) → the SRT sidecar, then strip stock-phrase
// hallucinations. translate=true asks Whisper to translate the (foreign) audio to English.
func (w *whisperGen) generate(ctx context.Context, ffmpeg, videoPath, srtPath, lang string, translate bool) error {
	model := w.modelPath(translate)
	if model == "" {
		return fmt.Errorf("no whisper model available for %s", ifElse(translate, "translation", "transcription"))
	}
	// 1. Extract mono 16 kHz PCM — what Whisper expects.
	wav := filepath.Join(os.TempDir(), fmt.Sprintf("whisper-%d.wav", time.Now().UnixNano()))
	defer os.Remove(wav)
	if err := exec.CommandContext(ctx, ffmpeg, "-y", "-hide_banner", "-i", videoPath,
		"-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wav).Run(); err != nil {
		return fmt.Errorf("extract audio: %w", err)
	}
	// 2. Transcribe/translate to SRT. whisper-cli writes "<outBase>.srt".
	outBase := strings.TrimSuffix(srtPath, filepath.Ext(srtPath))
	args := []string{"-m", model, "-f", wav, "-osrt", "-of", outBase}
	if translate {
		args = append(args, "--translate")
	} else if lang != "" {
		args = append(args, "-l", lang)
	}
	if w.hasModel(vadModel) {
		args = append(args, "--vad", "--vad-model", filepath.Join(w.modelsDir, vadModel))
	}
	if err := exec.CommandContext(ctx, w.bin, args...).Run(); err != nil {
		return fmt.Errorf("whisper: %w", err)
	}
	// 3. Drop stock-phrase hallucinations ("thank you" / "thanks for watching" over non-speech).
	if b, err := os.ReadFile(srtPath); err == nil {
		if cleaned := filterStockPhrases(string(b)); cleaned != string(b) {
			_ = os.WriteFile(srtPath, []byte(cleaned), 0o644)
		}
	}
	return nil
}

// aiPlan decides how (if at all) AI can produce a wanted-language subtitle from a file's audio:
//   "transcribe" — the audio is already in that language;
//   "translate"  — the target is English and the audio isn't (Whisper's only translation direction),
//                  or the audio language is unknown (auto-detect + translate is a safe default);
//   ""           — impossible: Whisper can't translate into a non-English language.
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
