package convert

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	langtab "github.com/tristenlammi/arrmada/internal/langs"
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
	KeepSubLangs     string `json:"keep_sub_langs"` // CSV; empty = keep all; "@subtitles" = same as Subtitles
	ImageSubs        string `json:"image_subs"`     // keep | when_text | remove
	TidyTracks       bool   `json:"tidy_tracks"`    // rewrite files whose only gap is a few tracks
	Crop             bool   `json:"crop"`           // remove black bars from re-encoded films
	DolbyVision      string `json:"dolby_vision"`   // drop (keep the HDR10/HLG base) | skip (leave them alone) | keep (as profile 8.1, preview)
	TenBit           string `json:"ten_bit"`        // 8-bit SDR sources: "always" 10-bit | "match_source"
	Resumable        bool   `json:"resumable"`      // encode long films in pieces a restart can't throw away (chunk.go)

	// Undo: how long a converted original is kept in the originals hold (0 = no undo), and
	// how much the hold may keep before conversions pause (GB; 0 = no limit).
	HoldDays  int `json:"hold_days"`
	HoldMaxGB int `json:"hold_max_gb"`

	// Advanced.
	ScratchDir  string `json:"scratch_dir"`
	VaapiDevice string `json:"vaapi_device"`
	CPUCores    int    `json:"cpu_cores"` // 0 = half the machine
	Workers     int    `json:"workers"`
	ScanAt      string `json:"scan_at"`

	// Read-only context for the page.
	ServerTime   string `json:"server_time"` // the hours are read on this clock
	ServerTZ     string `json:"server_tz"`
	PlexWatching bool   `json:"plex_watching_known"` // Plex monitoring is on, so pausing can work
	CanPause     bool   `json:"can_pause"`           // running encodes can be frozen on this platform
	HasGPU       bool   `json:"has_gpu"`             // a working hardware encoder exists
	GPUDoesAV1   bool   `json:"gpu_does_av1"`
	// GPUTenBit says every hardware encoder Convert would use can write 10-bit, so "always
	// 10-bit" applies on the GPU too. False with a GPU: its encodes of 8-bit files stay 8-bit.
	GPUTenBit bool `json:"gpu_ten_bit"`
	// GPUHDR and GPUHDRAV1 are the HDR formats ("HDR10", "HLG") the GPU's HEVC and AV1
	// encoders proved at startup that they keep (gpuhdr.go); those files encode on the GPU in
	// GPU mode. Empty: all HDR encodes on the CPU. HDR10+ and Dolby Vision never appear.
	GPUHDR        []string `json:"gpu_hdr"`
	GPUHDRAV1     []string `json:"gpu_hdr_av1"`
	HDR10PlusTool bool     `json:"hdr10plus_tool"`
	DoviTool      bool     `json:"dovi_tool"` // Dolby Vision can be kept through a conversion
}

// GetSettings reads the current settings.
func (s *Service) GetSettings(ctx context.Context) Settings {
	p := s.prefs(ctx)
	g := s.settings
	cores, _ := strconv.Atoi(g.Get(ctx, keyCPUCores, "0"))
	zone, _ := time.Now().Zone()
	hwHEVC, hasHEVC := hardwareFor("hevc", s.encoders)
	hwAV1, hasAV1 := hardwareFor("av1", s.encoders)
	gpuTenBit := (hasHEVC || hasAV1) && (!hasHEVC || hwHEVC.TenBit) && (!hasAV1 || hwAV1.TenBit)
	gpuHDR, gpuHDRAV1 := []string{}, []string{}
	if hasHEVC && !s.hardwareIsBroken(hwHEVC.Name) {
		gpuHDR = s.gpuHDRKinds(hwHEVC)
	}
	if hasAV1 && !s.hardwareIsBroken(hwAV1.Name) {
		gpuHDRAV1 = s.gpuHDRKinds(hwAV1)
	}
	return Settings{
		Auto: p.auto, HoursStart: p.start, HoursEnd: p.end,
		AllowAV1: p.allowAV1, UseGPU: p.useGPU, PauseWatching: p.pauseWatching,
		KeepAudioLangs:   strings.Join(p.keepAudio, ", "),
		KeepOriginalLang: p.keepOriginal, DropCommentary: p.dropCommentary,
		KeepSubLangs: strings.Join(splitCSV(p.keepSubsSaved), ", "), ImageSubs: p.imageSubs, TidyTracks: p.tidyTracks, Crop: p.crop, DolbyVision: p.dolbyVision, TenBit: p.tenBit, Resumable: p.resumable,
		HoldDays: s.holdDays(ctx), HoldMaxGB: s.holdMaxGB(ctx),
		ScratchDir: g.Get(ctx, keyScratchDir, ""), VaapiDevice: g.Get(ctx, keyVaapiDevice, ""),
		CPUCores: cores, Workers: s.workerCount(ctx), ScanAt: g.Get(ctx, keyScanAt, defaultScanAt),
		ServerTime: time.Now().Format("15:04"), ServerTZ: zone,
		PlexWatching: s.watchingKnown(), CanPause: canSuspend,
		HasGPU: hasHEVC || hasAV1, GPUDoesAV1: hasAV1, GPUTenBit: gpuTenBit, GPUHDR: gpuHDR, GPUHDRAV1: gpuHDRAV1, HDR10PlusTool: s.hdr10plusTool != "", DoviTool: s.doviTool != "",
	}
}

// AudioLanguages are the audio languages Convert keeps, as canonical codes; empty when it
// keeps every track. The quality engine reads them as the household's own languages: a
// release dubbed into one of them is never "foreign-only".
func (s *Service) AudioLanguages(ctx context.Context) []string {
	var out []string
	for _, l := range splitCSV(s.settings.Get(ctx, keyKeepAudioLangs, "")) {
		if c := langtab.Canonical(l); c != "" {
			out = append(out, c)
		}
	}
	return out
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
	DolbyVision      *string `json:"dolby_vision"`
	TenBit           *string `json:"ten_bit"`
	Resumable        *bool   `json:"resumable"`
	HoldDays         *int    `json:"hold_days"`
	HoldMaxGB        *int    `json:"hold_max_gb"`
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
	if p.DolbyVision != nil {
		switch *p.DolbyVision {
		case DVDrop, DVSkip:
		case DVKeep:
			if s.doviTool == "" {
				return Settings{}, fmt.Errorf("keeping Dolby Vision needs dovi_tool, which isn't installed")
			}
		default:
			return Settings{}, fmt.Errorf("unknown Dolby Vision choice %q", *p.DolbyVision)
		}
	}
	if p.TenBit != nil && *p.TenBit != TenBitAlways && *p.TenBit != TenBitMatchSource {
		return Settings{}, fmt.Errorf("unknown 10-bit choice %q", *p.TenBit)
	}
	if p.Workers != nil && (*p.Workers < 1 || *p.Workers > maxWorkers) {
		return Settings{}, fmt.Errorf("conversions at once must be 1–%d", maxWorkers)
	}
	if p.CPUCores != nil && *p.CPUCores < 0 {
		return Settings{}, fmt.Errorf("CPU cores can't be negative")
	}
	if p.HoldDays != nil && (*p.HoldDays < 0 || *p.HoldDays > maxHoldDays) {
		return Settings{}, fmt.Errorf("keep originals for 0–%d days", maxHoldDays)
	}
	if p.HoldMaxGB != nil && (*p.HoldMaxGB < 0 || *p.HoldMaxGB > maxHoldGB) {
		return Settings{}, fmt.Errorf("the originals hold can be 0–%d GB", maxHoldGB)
	}

	// The defaults that depend on whether this install has used Convert before are settled
	// before this save can make it look as if it had: a new install's first save switching
	// Auto on must not turn its Dolby Vision default into "drop".
	s.dvChoice(ctx)
	s.tenBitChoice(ctx)

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
	setS(keyDolbyVision, p.DolbyVision, trim)
	setS(keyTenBit, p.TenBit, trim)
	setB(keyResumable, p.Resumable)
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
	if p.HoldDays != nil {
		if err := g.Set(ctx, keyHoldDays, strconv.Itoa(*p.HoldDays)); err != nil {
			errs = append(errs, keyHoldDays)
		}
	}
	if p.HoldMaxGB != nil {
		if err := g.Set(ctx, keyHoldMaxGB, strconv.Itoa(*p.HoldMaxGB)); err != nil {
			errs = append(errs, keyHoldMaxGB)
		}
	}
	if p.HoldDays != nil || p.HoldMaxGB != nil {
		s.holdFreed(ctx) // files paused on a full hold may fit now
	}
	if len(errs) > 0 {
		return Settings{}, fmt.Errorf("could not save %s", strings.Join(errs, ", "))
	}
	s.invalidateLibraryCache()
	s.wakeUp()
	return s.GetSettings(ctx), nil
}
