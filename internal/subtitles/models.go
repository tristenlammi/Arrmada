package subtitles

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// whisperAssets are the downloadable local-AI models (GGML). Fetched on demand to the models dir so
// the image stays small — the user picks what they need (turbo for English, large-v3 to translate).
var whisperAssets = []struct {
	name, label, url string
	sizeMB           int
}{
	{modelTurbo, "large-v3-turbo — fast English transcription", "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin", 1600},
	{modelLarge, "large-v3 — required to translate foreign audio to English", "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3.bin", 3100},
	{vadModel, "Silero VAD — suppresses non-speech hallucination (recommended)", "https://huggingface.co/ggml-org/whisper-vad/resolve/main/ggml-silero-v5.1.2.bin", 3},
}

// ModelInfo is a whisper asset's state for the Settings UI.
type ModelInfo struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	SizeMB      int    `json:"size_mb"`
	Present     bool   `json:"present"`
	Downloading bool   `json:"downloading"`
}

// WhisperStatus is the local-AI readiness for the Settings panel.
type WhisperStatus struct {
	BinaryReady bool        `json:"binary_ready"` // whisper-cli installed
	Ready       bool        `json:"ready"`        // binary + a usable model
	Models      []ModelInfo `json:"models"`
}

// WhisperStatus reports the local-AI binary + model state.
func (s *Service) WhisperStatus() WhisperStatus {
	w := s.whisper
	st := WhisperStatus{BinaryReady: w != nil && w.bin != "", Ready: w.available()}
	for _, a := range whisperAssets {
		st.Models = append(st.Models, ModelInfo{
			Name: a.name, Label: a.label, SizeMB: a.sizeMB,
			Present: w.hasModel(a.name), Downloading: w.isDownloading(a.name),
		})
	}
	return st
}

// DownloadModel fetches a whisper asset in the background, logging progress to the activity console.
// Returns an error only for an unknown name or an already-running download.
func (s *Service) DownloadModel(name string) error {
	var url string
	for _, a := range whisperAssets {
		if a.name == name {
			url = a.url
		}
	}
	if url == "" {
		return fmt.Errorf("unknown model %q", name)
	}
	if s.whisper.hasModel(name) {
		return fmt.Errorf("%s is already downloaded", name)
	}
	if !s.whisper.markDownloading(name) {
		return fmt.Errorf("%s is already downloading", name)
	}
	go func() {
		defer s.whisper.unmarkDownloading(name)
		s.event("info", "Downloading "+name+"… (this is a large file)")
		if err := s.whisper.fetch(context.Background(), url, name, s.event); err != nil {
			s.event("error", "download "+name+" failed: "+err.Error())
			return
		}
		s.event("info", "✓ Downloaded "+name+" — local AI is ready")
	}()
	return nil
}

func (w *whisperGen) isDownloading(name string) bool {
	if w == nil {
		return false
	}
	w.dlMu.Lock()
	defer w.dlMu.Unlock()
	return w.dl[name]
}

// markDownloading claims a download slot; false if one's already in flight for name.
func (w *whisperGen) markDownloading(name string) bool {
	w.dlMu.Lock()
	defer w.dlMu.Unlock()
	if w.dl[name] {
		return false
	}
	w.dl[name] = true
	return true
}
func (w *whisperGen) unmarkDownloading(name string) {
	w.dlMu.Lock()
	delete(w.dl, name)
	w.dlMu.Unlock()
}

// fetch streams a URL to <modelsDir>/<name> via a temp file (atomic rename on success), logging
// progress every ~10%.
func (w *whisperGen) fetch(ctx context.Context, url, name string, log func(level, msg string)) error {
	if err := os.MkdirAll(w.modelsDir, 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := filepath.Join(w.modelsDir, name+".part")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	pw := &progressWriter{total: resp.ContentLength, name: name, log: log, last: time.Now()}
	_, err = io.Copy(f, io.TeeReader(resp.Body, pw))
	closeErr := f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, filepath.Join(w.modelsDir, name))
}

// progressWriter logs download progress at ~10% steps (and no more than every 2s).
type progressWriter struct {
	total    int64
	got      int64
	lastPct  int
	name     string
	log      func(level, msg string)
	last     time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.got += int64(n)
	if p.total > 0 {
		pct := int(p.got * 100 / p.total)
		if pct >= p.lastPct+10 && time.Since(p.last) > 2*time.Second {
			p.lastPct = pct
			p.last = time.Now()
			p.log("info", fmt.Sprintf("  %s: %d%% (%d/%d MB)", p.name, pct, p.got>>20, p.total>>20))
		}
	}
	return n, nil
}
