package subtitles

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The oneAPI build picks SYCL device 0 unless told otherwise, and on a host with an Arc
// card next to the CPU's own graphics, device 0 is as likely to be the integrated one.
// Before the first real run, the build is probed on a second of silence: the device
// table it prints says what's there, the strongest discrete GPU is chosen, and every
// run after that carries whisper's -dev flag for it. The probe doubles as the "does
// this build work at all here" check, so a broken driver is found on a one-second
// file rather than a four-minute chunk.

// syclDevice is one row of ggml-sycl's device table:
// "| 0| [level_zero:gpu:0]| Intel Arc A380 Graphics| 12.55| 128| 1024| 32| 6001M| 1.6.33276|".
type syclDevice struct {
	Index  int
	Type   string // "[level_zero:gpu:0]"
	Name   string
	CUs    int
	Mem    string // "6001M"
	Driver string
}

// syclDevices parses the table out of a run's output.
func syclDevices(out []byte) []syclDevice {
	var rows []syclDevice
	for _, line := range bytes.Split(out, []byte("\n")) {
		t := strings.TrimSpace(string(line))
		if !strings.HasPrefix(t, "|") || strings.HasPrefix(t, "|ID|") || strings.HasPrefix(t, "|--") {
			continue
		}
		f := strings.Split(t, "|")
		if len(f) < 10 {
			continue
		}
		for k := range f {
			f[k] = strings.TrimSpace(f[k])
		}
		idx, err := strconv.Atoi(f[1])
		if err != nil || !strings.HasPrefix(f[2], "[") {
			continue
		}
		cus, _ := strconv.Atoi(f[5])
		rows = append(rows, syclDevice{Index: idx, Type: f[2], Name: f[3], CUs: cus, Mem: f[8], Driver: f[9]})
	}
	return rows
}

// syclMainDevice is the device ggml-sycl said it used ("using device N (...) as main
// device"), or 0.
func syclMainDevice(out []byte) int {
	const marker = "using device "
	if i := bytes.Index(out, []byte(marker)); i >= 0 {
		rest := out[i+len(marker):]
		if j := bytes.IndexByte(rest, ' '); j > 0 {
			if n, err := strconv.Atoi(string(rest[:j])); err == nil {
				return n
			}
		}
	}
	return 0
}

// chooseSyclDevice picks the device to run on: Level Zero over OpenCL (the same card
// shows up once per backend), a discrete card over integrated graphics, then the most
// compute units. -1 when the table is empty.
func chooseSyclDevice(rows []syclDevice) int {
	best, bestScore := -1, -1
	for _, r := range rows {
		score := r.CUs
		if strings.HasPrefix(r.Type, "[level_zero") {
			score += 100000
		}
		n := strings.ToLower(r.Name)
		if strings.Contains(n, "arc") || strings.Contains(n, "data center") || strings.Contains(n, "flex") || strings.Contains(n, "max") {
			score += 10000
		}
		if score > bestScore {
			best, bestScore = r.Index, score
		}
	}
	return best
}

// probeSycl runs the oneAPI build once on a second of silence and settles which device
// it should use, or retires it. Once per process; cheap next to any real job.
func (w *whisperGen) probeSycl(ctx context.Context, model string) {
	w.backendMu.Lock()
	if w.sycl == "" || w.syclOff || w.syclProbed {
		w.backendMu.Unlock()
		return
	}
	w.syclProbed = true
	w.backendMu.Unlock()

	wav := filepath.Join(os.TempDir(), fmt.Sprintf("whisper-probe-%d.wav", time.Now().UnixNano()))
	if err := writeSilentWAV(wav, time.Second); err != nil {
		return // can't probe; the first real chunk will tell instead
	}
	defer os.Remove(wav)
	outBase := strings.TrimSuffix(wav, ".wav")
	defer os.Remove(outBase + ".json")
	args := []string{"-m", model, "-f", wav, "-t", "1", "-nf", "-ojf", "-of", outBase}
	out, err := runWhisper(ctx, w.sycl, args, nil)
	if ctx.Err() != nil {
		w.backendMu.Lock()
		w.syclProbed = false // try again next time
		w.backendMu.Unlock()
		return
	}
	if err != nil {
		w.disableSycl("its probe failed: " + tailStr(out, 300))
		return
	}
	if backendOf(out) != "sycl" {
		w.disableSycl("it found no GPU (is /dev/dri passed to the container? `clinfo -l` inside it should list the card); the Vulkan build is used meanwhile")
		return
	}
	rows := syclDevices(out)
	dev := chooseSyclDevice(rows)
	if dev < 0 {
		dev = 0
	}
	w.backendMu.Lock()
	w.syclDev = dev
	note := w.note
	w.backendMu.Unlock()
	if note != nil && len(rows) > 1 {
		var names []string
		for _, r := range rows {
			names = append(names, fmt.Sprintf("%d: %s %s", r.Index, r.Name, r.Type))
		}
		chosen := ""
		for _, r := range rows {
			if r.Index == dev {
				chosen = r.Name
			}
		}
		note("info", fmt.Sprintf("AI: oneAPI sees %d GPU devices (%s) — using %d: %s", len(rows), strings.Join(names, "; "), dev, chosen))
	}
}

// writeSilentWAV writes d of 16 kHz mono 16-bit silence.
func writeSilentWAV(path string, d time.Duration) error {
	const rate = 16000
	n := int(d.Seconds() * rate)
	data := make([]byte, n*2)
	var hdr bytes.Buffer
	hdr.WriteString("RIFF")
	binary.Write(&hdr, binary.LittleEndian, uint32(36+len(data)))
	hdr.WriteString("WAVEfmt ")
	binary.Write(&hdr, binary.LittleEndian, uint32(16))
	binary.Write(&hdr, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&hdr, binary.LittleEndian, uint16(1)) // mono
	binary.Write(&hdr, binary.LittleEndian, uint32(rate))
	binary.Write(&hdr, binary.LittleEndian, uint32(rate*2))
	binary.Write(&hdr, binary.LittleEndian, uint16(2))
	binary.Write(&hdr, binary.LittleEndian, uint16(16))
	hdr.WriteString("data")
	binary.Write(&hdr, binary.LittleEndian, uint32(len(data)))
	return os.WriteFile(path, append(hdr.Bytes(), data...), 0o644)
}
