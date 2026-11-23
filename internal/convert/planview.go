package convert

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The conversion plan, shown before anything runs: what one file's picture becomes, which
// audio and subtitle tracks stay or go and why, the container change, and every warning.
// It is built from the library index and the very functions the runner uses (planFor,
// keptAudio/keptSubs through trackDecisions, planWarnings), so the card and the real
// conversion can't disagree. There is no probing; the only I/O is listing the file's folder
// for subtitle sidecars. Black bars and the HEVC/AV1 pick are settled per file at conversion
// time (they mean decoding the film), so the plan says so rather than guessing.

// PlanVideo is the picture half of a plan.
type PlanVideo struct {
	FromCodec string `json:"from_codec"`
	FromRes   string `json:"from_res"`
	FromHDR   string `json:"from_hdr"`
	FromBits  int    `json:"from_bits"`
	// ToCodec is the likely target; "" = the picture is copied untouched.
	ToCodec string `json:"to_codec,omitempty"`
	ToBits  int    `json:"to_bits,omitempty"`
	ToHDR   string `json:"to_hdr,omitempty"`
	CRF     int    `json:"crf,omitempty"`
	// EstBytes is the whole file's expected size afterwards; Measured says it comes from a
	// test encode of this file rather than the estimate.
	EstBytes int64 `json:"est_bytes"`
	Measured bool  `json:"measured,omitempty"`
	// Why says why the picture is left alone, when it is.
	Why       string `json:"why,omitempty"`
	CodecNote string `json:"codec_note,omitempty"`
	CropNote  string `json:"crop_note,omitempty"`
	HDRNote   string `json:"hdr_note,omitempty"`
}

// PlanContainer is the container before and after (the output is always Matroska).
type PlanContainer struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PlanView is one file's conversion plan, for the Library's plan card and the Convert-now
// confirmation.
type PlanView struct {
	Key       string          `json:"key"`
	Kind      string          `json:"kind"`
	Title     string          `json:"title"`
	PosterURL string          `json:"poster_url,omitempty"`
	SizeBytes int64           `json:"size_bytes"`
	Needs     Needs           `json:"needs"`
	Video     PlanVideo       `json:"video"`
	Audio     []TrackDecision `json:"audio"`
	Subs      []TrackDecision `json:"subs"`
	Container PlanContainer   `json:"container"`
	Warnings  []string        `json:"warnings"`
	// EtaSec is how long the conversion should take; null until the forecast exists.
	EtaSec *float64 `json:"eta_sec"`
}

// ErrNotIndexed means the file isn't in the library index (or hasn't been analysed yet).
var ErrNotIndexed = errors.New("this file hasn't been analysed yet — rescan the library")

// ErrInvalidItem means a key that names no movie or episode.
var ErrInvalidItem = errors.New("invalid item")

// PlanView builds one file's plan from the index.
func (s *Service) PlanView(ctx context.Context, key string) (PlanView, error) {
	r, err := s.indexRowFor(ctx, key)
	if err != nil {
		return PlanView{}, err
	}
	p := s.prefs(ctx)
	plan, needs := p.planForTitle(r.Info, r.Path, r.ref(), nil)
	return s.planView(ctx, p, r, plan, needs), nil
}

// planView shapes a plan the runner would run into the card's terms.
func (s *Service) planView(ctx context.Context, p prefs, r indexRow, plan Plan, needs Needs) PlanView {
	mi := r.Info
	v := PlanView{
		Key: ItemKey(r.MediaType, r.MovieID, r.VersionID, r.SeriesID, r.Season, r.Episode), Kind: r.MediaType,
		Title: r.Title, PosterURL: r.PosterURL, SizeBytes: mi.SizeBytes, Needs: needs,
		Container: PlanContainer{From: mi.Container, To: mi.Container},
		Warnings:  []string{},
	}
	if v.SizeBytes <= 0 {
		v.SizeBytes = r.SizeBytes
	}
	v.Video = PlanVideo{FromCodec: strings.ToUpper(mi.VideoCodec), FromRes: mi.Resolution, FromHDR: mi.HDR,
		FromBits: bitsOf(mi.TenBit), EstBytes: v.SizeBytes - needs.Save, Measured: needs.Measured, Why: needs.Why}
	if !needs.Any() {
		v.Video.EstBytes = v.SizeBytes
	}
	for _, d := range trackDecisions(mi, plan) {
		if d.Type == "audio" {
			v.Audio = append(v.Audio, d)
			continue
		}
		// The ledger doesn't keep subtitle titles; the card shows them ("SDH", "Signs").
		if d.Index >= 0 && d.Index < len(mi.Subs) && mi.Subs[d.Index].SubIndex == d.Index {
			d.Title = mi.Subs[d.Index].Title
		}
		v.Subs = append(v.Subs, d)
	}
	if v.Audio == nil {
		v.Audio = []TrackDecision{}
	}
	if v.Subs == nil {
		v.Subs = []TrackDecision{}
	}
	if !needs.Any() {
		return v // nothing would change: no container rewrite, nothing to warn about
	}
	// The output is always Matroska: it holds every audio and subtitle format untouched.
	v.Container.To = "MKV"

	if plan.VideoCodec != "" {
		enc := s.encoderChoice(mi, plan.VideoCodec, p.useGPU)
		v.Video.ToCodec = strings.ToUpper(plan.VideoCodec)
		v.Video.CRF = plan.Quality
		v.Video.ToHDR = mi.EncodeHDR()
		if plan.KeepDV {
			v.Video.ToHDR = "Dolby Vision 8.1 (" + mi.EncodeHDR() + " base)"
		}
		v.Video.ToBits = outputBits(mi, plan, enc)
		if p.allowAV1 && mi.EncodeHDR() != "HDR10+" && !plan.KeepDV {
			v.Video.CodecNote = "HEVC or AV1 — a side-by-side test of a few clips picks at conversion time"
		}
		switch {
		case !p.crop:
			v.Video.CropNote = "black bars are kept (crop is off)"
		case plan.KeepDV:
			v.Video.CropNote = "black bars are kept — the Dolby Vision metadata describes the full frame"
		case keepsImageSubs(mi, plan):
			v.Video.CropNote = "black bars are kept — image subtitles stay, and they're drawn on the full frame"
		default:
			v.Video.CropNote = "black bars are checked at conversion time"
		}
		switch hdr := mi.EncodeHDR(); {
		case plan.KeepDV:
			v.Video.HDRNote = "Dolby Vision is kept as profile 8.1 (preview), over its " + hdr + " base"
		case hdr == "HDR10+" || (hdr == "HDR10" && codecClass(mi.VideoCodec) == "hevc"):
			v.Video.HDRNote = "HDR10+ is checked at conversion time and kept if present (HEVC only)"
		case isHDR(hdr):
			v.Video.HDRNote = hdr + " is kept"
		}
	}
	v.Warnings = append(v.Warnings, planViewWarnings(mi, plan, p, v.Video)...)
	if w := s.dvProfileWarning(ctx, r, plan); w != "" {
		v.Warnings = append(v.Warnings, w)
	}
	// The time forecast (throughput.go), the same estimate Up next shows.
	eta := float64(s.newEstimator(ctx, p).seconds(factsFor(mi, plan, v.SizeBytes)))
	v.EtaSec = &eta
	return v
}

// planViewWarnings is everything the card warns about: what the conversion loses that you
// didn't explicitly ask to lose (planWarnings — the same list the ledger records), plus the
// changes worth knowing before an in-place replacement.
func planViewWarnings(mi *MediaInfo, plan Plan, p prefs, vid PlanVideo) []string {
	w := planWarnings(mi, plan)
	if c := strings.ToUpper(mi.Container); c != "" && c != "MKV" {
		w = append(w, fmt.Sprintf("the container changes from %s to MKV — the file's extension changes too", c))
	}
	if plan.VideoCodec != "" && vid.ToBits == 10 && !mi.TenBit {
		w = append(w, "the 8-bit picture is encoded as 10-bit — smoother gradients, but a few older devices can't direct-play 10-bit")
	}
	if plan.VideoCodec != "" && p.crop && !keepsImageSubs(mi, plan) && !plan.KeepDV {
		w = append(w, "black bars are checked at conversion time and removed only where every sampled frame shows them")
	}
	if n := sidecarCoveredImageSubs(mi, plan); n > 0 {
		w = append(w, fmt.Sprintf("%d image subtitle track(s) removed because an .srt beside the file covers the language — deleting that .srt later won't bring them back", n))
	}
	return w
}

// sidecarCoveredImageSubs counts the image subtitle tracks that go only because an external
// .srt covers their language: no embedded text track of that kind would remain for it.
func sidecarCoveredImageSubs(mi *MediaInfo, plan Plan) int {
	if plan.Subs.ImageSubs != ImageSubsWhenText {
		return 0
	}
	kept := map[int]bool{}
	textFull, textForced := map[string]bool{}, map[string]bool{}
	for _, s := range keptSubs(mi, plan) {
		kept[s.SubIndex] = true
		if s.Text && s.Forced {
			textForced[normLang(s.Lang)] = true
		} else if s.Text {
			textFull[normLang(s.Lang)] = true
		}
	}
	n := 0
	for _, s := range langFilteredSubs(mi, plan) {
		if s.Text || kept[s.SubIndex] {
			continue
		}
		l := normLang(s.Lang)
		if (s.Forced && !textForced[l]) || (!s.Forced && !textFull[l]) {
			n++
		}
	}
	return n
}

// outputBits is the picture's bit depth after an encode: the CPU encoders write what the
// 10-bit choice says (tenbit.go outputPixFmt); hardware does too where the GPU can write
// 10-bit (hwOutputTenBit), and otherwise keeps the source's depth.
func outputBits(mi *MediaInfo, plan Plan, enc Encoder) int {
	if enc.Kind == "cpu" || enc.Kind == "" {
		return bitsOf(outputPixFmt(mi, plan) == pixFmt10)
	}
	return bitsOf(hwOutputTenBit(enc, mi, plan))
}

func bitsOf(tenBit bool) int {
	if tenBit {
		return 10
	}
	return 8
}

// indexRowFor reads one item's indexed row, with its analysis.
func (s *Service) indexRowFor(ctx context.Context, key string) (indexRow, error) {
	it, ok := parseKey(key)
	if !ok {
		return indexRow{}, ErrInvalidItem
	}
	if s.index == nil {
		return indexRow{}, ErrNotIndexed
	}
	q := `SELECT path, media_type, movie_id, series_id, season, episode, title, year, poster_url,
	             size_bytes, video_codec, info_json, orig_lang, version_id, version_label
	        FROM convert_library WHERE `
	var args []any
	if it.Kind == "episode" {
		q += `media_type = 'episode' AND series_id = ? AND season = ? AND episode = ?`
		args = []any{it.SeriesID, it.Season, it.Episode}
	} else {
		// Each version of a film has its own row; the key names which one (0 = its default file).
		q += `media_type = 'movie' AND movie_id = ? AND version_id = ?`
		args = []any{it.MovieID, it.VersionID}
	}
	q += ` ORDER BY indexed_at DESC LIMIT 1`
	var r indexRow
	var infoJSON string
	err := s.index.db.QueryRowContext(ctx, q, args...).Scan(&r.Path, &r.MediaType, &r.MovieID, &r.SeriesID, &r.Season,
		&r.Episode, &r.Title, &r.Year, &r.PosterURL, &r.SizeBytes, &r.Codec, &infoJSON, &r.OrigLang, &r.VersionID, &r.VersionLabel)
	if errors.Is(err, sql.ErrNoRows) {
		return indexRow{}, ErrNotIndexed
	}
	if err != nil {
		return indexRow{}, err
	}
	var mi MediaInfo
	if infoJSON == "" || json.Unmarshal([]byte(infoJSON), &mi) != nil {
		return indexRow{}, ErrNotIndexed
	}
	r.Info = &mi
	return r, nil
}
