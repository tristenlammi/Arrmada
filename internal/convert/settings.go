package convert

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Settings is everything Convert lets you choose — deliberately few things. The quality bar,
// the minimum saving, seeding safety and the failure limit are not settings: they're the
// promise, and they're always on.
type Settings struct {
	Auto          bool   `json:"auto"`        // convert the library automatically
	HoursStart    string `json:"hours_start"` // "HH:MM"; both empty = any time
	HoursEnd      string `json:"hours_end"`
	AllowAV1      bool   `json:"allow_av1"`      // my devices play AV1
	UseGPU        bool   `json:"use_gpu"`        // faster, somewhat bigger files
	PauseWatching bool   `json:"pause_watching"` // pause while someone is watching Plex

	KeepAudioLangs   string `json:"keep_audio_langs"`   // CSV; empty = keep all
	KeepOriginalLang bool   `json:"keep_original_lang"` // also keep the title's original language
	DropCommentary   bool   `json:"drop_commentary"`
	KeepSubLangs     string `json:"keep_sub_langs"` // CSV; empty = keep all
	ImageSubs        string `json:"image_subs"`     // keep | when_text | remove
	TidyTracks       bool   `json:"tidy_tracks"`    // rewrite files whose only gap is a few tracks
	Crop             bool   `json:"crop"`           // remove black bars from re-encoded films

	// Advanced.
	ScratchDir  string `json:"scratch_dir"`
	VaapiDevice string `json:"vaapi_device"`
	CPUCores    int    `json:"cpu_cores"` // 0 = half the machine
	Workers     int    `json:"workers"`
	ScanAt      string `json:"scan_at"`

	// Read-only context for the page.
	ServerTime    string `json:"server_time"` // the hours are read on this clock
	ServerTZ      string `json:"server_tz"`
	PlexWatching  bool   `json:"plex_watching_known"` // Plex monitoring is on, so pausing can work
	CanPause      bool   `json:"can_pause"`           // running encodes can be frozen on this platform
	HasGPU        bool   `json:"has_gpu"`             // a working hardware encoder exists
	GPUDoesAV1    bool   `json:"gpu_does_av1"`
	HDR10PlusTool bool   `json:"hdr10plus_tool"`
}

// GetSettings reads the current settings.
func (s *Service) GetSettings(ctx context.Context) Settings {
	p := s.prefs(ctx)
	g := s.settings
	cores, _ := strconv.Atoi(g.Get(ctx, keyCPUCores, "0"))
	zone, _ := time.Now().Zone()
	_, hasHEVC := hardwareFor("hevc", s.encoders)
	_, hasAV1 := hardwareFor("av1", s.encoders)
	return Settings{
		Auto: p.auto, HoursStart: p.start, HoursEnd: p.end,
		AllowAV1: p.allowAV1, UseGPU: p.useGPU, PauseWatching: p.pauseWatching,
		KeepAudioLangs:   strings.Join(p.keepAudio, ", "),
		KeepOriginalLang: p.keepOriginal, DropCommentary: p.dropCommentary,
		KeepSubLangs: strings.Join(p.keepSubs, ", "), ImageSubs: p.imageSubs, TidyTracks: p.tidyTracks, Crop: p.crop,
		ScratchDir: g.Get(ctx, keyScratchDir, ""), VaapiDevice: g.Get(ctx, keyVaapiDevice, ""),
		CPUCores: cores, Workers: s.workerCount(ctx), ScanAt: g.Get(ctx, keyScanAt, defaultScanAt),
		ServerTime: time.Now().Format("15:04"), ServerTZ: zone,
		PlexWatching: s.watchingKnown(), CanPause: canSuspend,
		HasGPU: hasHEVC || hasAV1, GPUDoesAV1: hasAV1, HDR10PlusTool: s.hdr10plusTool != "",
	}
}

// SettingsPatch is a partial update; nil fields are left as they are.
type SettingsPatch struct {
	Auto             *bool   `json:"auto"`
	HoursStart       *string `json:"hours_start"`
	HoursEnd         *string `json:"hours_end"`
	AllowAV1         *bool   `json:"allow_av1"`
	UseGPU           *bool   `json:"use_gpu"`
	PauseWatching    *bool   `json:"pause_watching"`
	KeepAudioLangs   *string `json:"keep_audio_langs"`
	KeepOriginalLang *bool   `json:"keep_original_lang"`
	DropCommentary   *bool   `json:"drop_commentary"`
	KeepSubLangs     *string `json:"keep_sub_langs"`
	ImageSubs        *string `json:"image_subs"`
	TidyTracks       *bool   `json:"tidy_tracks"`
	Crop             *bool   `json:"crop"`
	ScratchDir       *string `json:"scratch_dir"`
	VaapiDevice      *string `json:"vaapi_device"`
	CPUCores         *int    `json:"cpu_cores"`
	Workers          *int    `json:"workers"`
	ScanAt           *string `json:"scan_at"`
}

// UpdateSettings validates and saves a patch. Nothing is saved if any field is invalid.
func (s *Service) UpdateSettings(ctx context.Context, p SettingsPatch) (Settings, error) {
	hm := func(name string, v *string) error {
		if v == nil || strings.TrimSpace(*v) == "" {
			return nil
		}
		if _, ok := parseHM(*v); !ok {
			return fmt.Errorf("%s must be a time like 01:30", name)
		}
		return nil
	}
	for name, v := range map[string]*string{"start time": p.HoursStart, "end time": p.HoursEnd, "scan time": p.ScanAt} {
		if err := hm(name, v); err != nil {
			return Settings{}, err
		}
	}
	if p.ImageSubs != nil {
		switch *p.ImageSubs {
		case ImageSubsKeep, ImageSubsWhenText, ImageSubsRemove:
		default:
			return Settings{}, fmt.Errorf("unknown image-subtitle choice %q", *p.ImageSubs)
		}
	}
	if p.Workers != nil && (*p.Workers < 1 || *p.Workers > maxWorkers) {
		return Settings{}, fmt.Errorf("conversions at once must be 1–%d", maxWorkers)
	}
	if p.CPUCores != nil && *p.CPUCores < 0 {
		return Settings{}, fmt.Errorf("CPU cores can't be negative")
	}

	g := s.settings
	var errs []string
	setB := func(k string, v *bool) {
		if v != nil {
			if err := g.SetBool(ctx, k, *v); err != nil {
				errs = append(errs, k)
			}
		}
	}
	setS := func(k string, v *string, clean func(string) string) {
		if v != nil {
			if err := g.Set(ctx, k, clean(*v)); err != nil {
				errs = append(errs, k)
			}
		}
	}
	trim := strings.TrimSpace
	langs := func(v string) string { return strings.Join(splitCSV(strings.ToLower(v)), ", ") }
	setB(keyAuto, p.Auto)
	setS(keySweepStart, p.HoursStart, trim)
	setS(keySweepEnd, p.HoursEnd, trim)
	setB(keyAllowAV1, p.AllowAV1)
	setB(keyUseGPU, p.UseGPU)
	setB(keyPauseWatching, p.PauseWatching)
	setS(keyKeepAudioLangs, p.KeepAudioLangs, langs)
	setB(keyKeepOrigLang, p.KeepOriginalLang)
	setB(keyDropCommentary, p.DropCommentary)
	setS(keyKeepSubLangs, p.KeepSubLangs, langs)
	setS(keyImageSubs, p.ImageSubs, trim)
	setB(keyTidyTracks, p.TidyTracks)
	setB(keyCrop, p.Crop)
	setS(keyScratchDir, p.ScratchDir, trim)
	setS(keyVaapiDevice, p.VaapiDevice, trim)
	setS(keyScanAt, p.ScanAt, trim)
	if p.CPUCores != nil {
		if err := g.Set(ctx, keyCPUCores, strconv.Itoa(*p.CPUCores)); err != nil {
			errs = append(errs, keyCPUCores)
		}
	}
	if p.Workers != nil {
		if err := g.Set(ctx, keyWorkers, strconv.Itoa(*p.Workers)); err != nil {
			errs = append(errs, keyWorkers)
		}
	}
	if len(errs) > 0 {
		return Settings{}, fmt.Errorf("could not save %s", strings.Join(errs, ", "))
	}
	s.invalidateLibraryCache()
	s.wakeUp()
	return s.GetSettings(ctx), nil
}
