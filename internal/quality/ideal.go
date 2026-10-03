package quality

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/parser"
)

// The target file. A video profile describes the file the library should hold — "HEVC or
// AV1, HDR10+, Dolby Atmos, 20–30 Mb/s at 4K" — once, and that one description drives
// three things:
//
//   - what's grabbed: a "must" is never grabbed without, and a resolution's bitrate ceiling
//     rejects anything above it;
//   - how releases rank: "want" ranks a release up, "avoid" ranks it down (a strictly lower
//     tier — see Decide), and so does falling under a bitrate floor;
//   - the library check: each file is judged against the target (CheckFit), and so is a
//     file's release when deciding whether upgrading can stop (TargetMet).
//
// The grab side runs on the engine's format scores and required formats: Compile writes the
// target into them on save, and Migrate reads a profile written before targets existed back
// out of them, so nothing about an existing profile's grabbing changes.

// Option states.
const (
	PrefNone  = ""      // no opinion
	PrefOK    = "ok"    // fits the target; no pull either way when grabbing
	PrefWant  = "want"  // fits the target, and ranks a release up
	PrefMust  = "must"  // never grabbed without (one of a row's musts)
	PrefAvoid = "avoid" // doesn't fit, and ranks a release down
)

// IdealFile is a profile's target file. Every part is optional.
type IdealFile struct {
	// Codec and HDR are rows of alternatives — a file is exactly one of them — keyed
	// "hevc" | "av1" | "h264" and "SDR" | "HDR10" | "HDR10+" | "HLG" | "DV".
	Codec map[string]string `json:"codec,omitempty"`
	HDR   map[string]string `json:"hdr,omitempty"`
	// Audio is a set of features a file has or hasn't: "atmos", "lossless".
	Audio map[string]string `json:"audio,omitempty"`
	// Bitrate is the whole file's average bitrate window per resolution ("2160p",
	// "1080p", "720p", "SD"), in Mb/s — the same figure the library tables show.
	Bitrate map[string]BitrateWindow `json:"bitrate,omitempty"`
}

// BitrateWindow is a Mb/s range; a zero bound is open.
type BitrateWindow struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// UnmarshalJSON also reads the first form of the ideal file (lists of accepted codecs and
// HDR formats, and Atmos/lossless flags), which shipped report-only.
func (f *IdealFile) UnmarshalJSON(b []byte) error {
	var raw struct {
		Codec    map[string]string        `json:"codec"`
		Codecs   []string                 `json:"codecs"`
		HDR      json.RawMessage          `json:"hdr"`
		Audio    map[string]string        `json:"audio"`
		Atmos    bool                     `json:"atmos"`
		Lossless bool                     `json:"lossless"`
		Bitrate  map[string]BitrateWindow `json:"bitrate"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*f = IdealFile{Codec: raw.Codec, Audio: raw.Audio, Bitrate: raw.Bitrate}
	// The first form's lists meant "these fit" — ok, so grabbing is unchanged by reading them.
	for _, c := range raw.Codecs {
		f.set(&f.Codec, c, PrefOK)
	}
	if len(raw.HDR) > 0 && string(raw.HDR) != "null" {
		var m map[string]string
		if json.Unmarshal(raw.HDR, &m) == nil {
			f.HDR = m
		} else {
			var list []string
			if err := json.Unmarshal(raw.HDR, &list); err != nil {
				return err
			}
			for _, h := range list {
				f.set(&f.HDR, h, PrefOK)
			}
		}
	}
	// Its flags meant "the file must have it" — the nearest state is want.
	if raw.Atmos {
		f.set(&f.Audio, "atmos", PrefWant)
	}
	if raw.Lossless {
		f.set(&f.Audio, "lossless", PrefWant)
	}
	return nil
}

func (f *IdealFile) set(m *map[string]string, key, state string) {
	if *m == nil {
		*m = map[string]string{}
	}
	if state == PrefNone {
		delete(*m, key)
		return
	}
	(*m)[key] = state
}

// Empty reports whether nothing has been set up.
func (f IdealFile) Empty() bool {
	for _, m := range []map[string]string{f.Codec, f.HDR, f.Audio} {
		for _, st := range m {
			if st != PrefNone {
				return false
			}
		}
	}
	for _, w := range f.Bitrate {
		if w.Min > 0 || w.Max > 0 {
			return false
		}
	}
	return true
}

// targetFormat ties a target option to the built-in format the engine scores it with.
type targetFormat struct {
	row, key, format string
}

// targetFormats lists every target option and its engine format (see DefaultFormats).
var targetFormats = []targetFormat{
	{"codec", "hevc", "HEVC"}, {"codec", "av1", "AV1"}, {"codec", "h264", "H.264"},
	{"hdr", "DV", "Dolby Vision"}, {"hdr", "HDR10+", "HDR10+"}, {"hdr", "HDR10", "HDR10"},
	{"hdr", "HLG", "HLG"}, {"hdr", "SDR", "SDR"},
	{"audio", "atmos", "Atmos"}, {"audio", "lossless", "Lossless"},
}

func (f *IdealFile) row(name string) *map[string]string {
	switch name {
	case "codec":
		return &f.Codec
	case "hdr":
		return &f.HDR
	}
	return &f.Audio
}

// preferScore is the score a want (or, negated, an avoid) carries when the profile doesn't
// already hold one — the same weight the builder's Prefer always used.
const preferScore = 50

// Compile writes the target into the engine's format scores and required formats. A
// score the profile already carries in the right direction is kept, so a weight tuned in
// Advanced survives.
func (sp *StoredProfile) Compile() {
	if sp.Ideal == nil {
		sp.Ideal = &IdealFile{}
	}
	if sp.FormatScores == nil {
		sp.FormatScores = map[string]int{}
	}
	for _, tf := range targetFormats {
		st := (*sp.Ideal.row(tf.row))[tf.key]
		cur := sp.FormatScores[tf.format]
		sp.RequiredFormats = removeStr(sp.RequiredFormats, tf.format)
		switch st {
		case PrefWant:
			if cur <= 0 {
				sp.FormatScores[tf.format] = preferScore
			}
		case PrefMust:
			if cur <= 0 {
				sp.FormatScores[tf.format] = preferScore
			}
			sp.RequiredFormats = append(sp.RequiredFormats, tf.format)
		case PrefAvoid:
			if cur >= 0 {
				sp.FormatScores[tf.format] = -preferScore
			}
		default:
			delete(sp.FormatScores, tf.format)
		}
	}
	// The per-resolution windows replace the single ceiling (Migrate copied it in).
	sp.BitrateCapMbps = 0
	if sp.Ideal.Empty() {
		sp.Ideal = nil
	}
}

// Migrate reads a profile's target out of its format scores, required formats and single
// bitrate ceiling where the target doesn't say otherwise — a profile written before
// targets existed opens showing exactly what it already did.
func (sp *StoredProfile) Migrate() {
	if sp.MediaType != MediaMovie && sp.MediaType != MediaSeries {
		return
	}
	ideal := IdealFile{}
	if sp.Ideal != nil {
		ideal = *sp.Ideal
	}
	for _, tf := range targetFormats {
		row := ideal.row(tf.row)
		if st := (*row)[tf.key]; st != PrefNone && st != PrefOK {
			continue
		}
		switch score := sp.FormatScores[tf.format]; {
		case containsStr(sp.RequiredFormats, tf.format):
			ideal.set(row, tf.key, PrefMust)
		case score > 0:
			ideal.set(row, tf.key, PrefWant)
		case score < 0:
			ideal.set(row, tf.key, PrefAvoid)
		}
	}
	if sp.BitrateCapMbps > 0 {
		for _, key := range resolutionKeys(sp.AllowedResolutions) {
			w := ideal.Bitrate[key]
			if w.Max <= 0 {
				w.Max = sp.BitrateCapMbps
				if ideal.Bitrate == nil {
					ideal.Bitrate = map[string]BitrateWindow{}
				}
				ideal.Bitrate[key] = w
			}
		}
	}
	if !ideal.Empty() {
		sp.Ideal = &ideal
	}
}

// resolutionKeys are the target's bitrate keys a profile covers ("SD" for 576p/480p).
func resolutionKeys(allowed []string) []string {
	if len(allowed) == 0 {
		return []string{"2160p", "1080p", "720p", "SD"}
	}
	var out []string
	for _, r := range allowed {
		k := ResolutionKey(parser.Resolution(r))
		if k != "" && !containsStr(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// ResolutionKey is the target key for a resolution.
func ResolutionKey(r parser.Resolution) string {
	switch r {
	case parser.Res2160p:
		return "2160p"
	case parser.Res1080p:
		return "1080p"
	case parser.Res720p:
		return "720p"
	case parser.Res576p, parser.Res480p:
		return "SD"
	}
	return ""
}

func removeStr(xs []string, v string) []string {
	out := xs[:0:0]
	for _, x := range xs {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// --- Judging a file ------------------------------------------------------------------

// FileFacts is what a file actually is, as far as the target cares.
type FileFacts struct {
	Resolution  string  `json:"resolution"` // "2160p" | "1080p" | "720p" | "SD"
	Codec       string  `json:"codec"`      // "hevc" | "av1" | "h264" | "other"
	HDR         string  `json:"hdr"`        // the picture's own format: SDR | HDR10 | HDR10+ | HLG
	DolbyVision bool    `json:"dolby_vision,omitempty"`
	Atmos       bool    `json:"atmos,omitempty"`
	Lossless    bool    `json:"lossless,omitempty"`
	BitrateMbps float64 `json:"bitrate_mbps"`
}

// ReleaseFacts reads the target's facts from a release name (and its bitrate, 0 when
// unknown) — how an existing file is judged when only its release is known.
func ReleaseFacts(r parser.Release, bitrateMbps float64) FileFacts {
	f := FileFacts{Resolution: ResolutionKey(r.Resolution), BitrateMbps: bitrateMbps, HDR: "SDR"}
	switch r.Codec {
	case parser.CodecX265:
		f.Codec = "hevc"
	case parser.CodecAV1:
		f.Codec = "av1"
	case parser.CodecX264:
		f.Codec = "h264"
	default:
		f.Codec = "other"
	}
	switch {
	case containsStr(r.HDR, "HDR10+"):
		f.HDR = "HDR10+"
	case containsStr(r.HDR, "HDR10"):
		f.HDR = "HDR10"
	case containsStr(r.HDR, "HLG"):
		f.HDR = "HLG"
	}
	f.DolbyVision = containsStr(r.HDR, "DV")
	f.Atmos = containsStr(r.Audio, "Atmos")
	f.Lossless = containsStr(r.Audio, "TrueHD") || containsStr(r.Audio, "DTS-HD") || containsStr(r.Audio, "FLAC")
	return f
}

// Fit statuses, worst first.
const (
	FitOver     = "over"     // above the bitrate ceiling
	FitUnder    = "under"    // below the bitrate floor
	FitMismatch = "mismatch" // the bitrate's fine, something else isn't
	FitOK       = "fits"
)

// FitIssue is one way a file misses its target.
type FitIssue struct {
	Kind string `json:"kind"` // bitrate | resolution | codec | hdr | atmos | lossless
	Msg  string `json:"msg"`
}

// Fit is a file's verdict against its profile's target.
type Fit struct {
	Status string         `json:"status"`
	Window *BitrateWindow `json:"window,omitempty"` // the window that applied, if any
	Issues []FitIssue     `json:"issues,omitempty"`
}

// CheckFit judges one file against a target. allowed are the profile's resolutions
// (empty = any).
func CheckFit(ideal IdealFile, allowed []string, f FileFacts) Fit {
	fit := Fit{Status: FitOK}
	add := func(kind, msg string) { fit.Issues = append(fit.Issues, FitIssue{kind, msg}) }

	if len(allowed) > 0 && f.Resolution != "" && !resolutionAllowed(f.Resolution, allowed) {
		add("resolution", f.Resolution+" isn't one of this profile's resolutions")
	}
	if msg := rowMiss(ideal.Codec, []string{f.Codec}, codecName(f.Codec), codecName); msg != "" {
		add("codec", msg)
	}
	// An HDR10+ picture is an HDR10 one too (the base layer), and a Dolby Vision file is
	// whatever is under its Dolby Vision layer — unless Dolby Vision itself has a state.
	hdrVals := []string{f.HDR}
	if f.HDR == "HDR10+" {
		hdrVals = append(hdrVals, "HDR10")
	}
	have := f.HDR
	if f.DolbyVision {
		hdrVals = append([]string{"DV"}, hdrVals...)
		have = "Dolby Vision (" + f.HDR + " base)"
	}
	if msg := rowMiss(ideal.HDR, hdrVals, have, hdrName); msg != "" {
		add("hdr", msg)
	}
	for _, ft := range []struct {
		key, label string
		has        bool
	}{{"atmos", "Dolby Atmos", f.Atmos}, {"lossless", "lossless audio", f.Lossless}} {
		switch ideal.Audio[ft.key] {
		case PrefWant, PrefMust:
			if !ft.has {
				add(ft.key, "no "+ft.label+" track")
			}
		case PrefAvoid:
			if ft.has {
				add(ft.key, "has "+ft.label+", which this profile avoids")
			}
		}
	}
	mismatch := len(fit.Issues) > 0

	if w, ok := ideal.Bitrate[f.Resolution]; ok && (w.Min > 0 || w.Max > 0) {
		fit.Window = &w
		switch br := f.BitrateMbps; {
		case br <= 0:
			// Unknown bitrate: nothing to judge it by.
		case w.Max > 0 && br > w.Max:
			fit.Status = FitOver
			add("bitrate", fmt.Sprintf("%.1f Mb/s is over the %.0f Mb/s ceiling", br, w.Max))
		case w.Min > 0 && br < w.Min:
			fit.Status = FitUnder
			add("bitrate", fmt.Sprintf("%.1f Mb/s is under the %.0f Mb/s floor", br, w.Min))
		}
	}
	if fit.Status == FitOK && mismatch {
		fit.Status = FitMismatch
	}
	return fit
}

// rowMiss judges a row of alternatives. vals are the file's values, most specific first
// (a Dolby Vision HDR10+ file is DV, then HDR10+, then HDR10). It returns "" when the file
// fits the row:
//
//   - any value avoided → it doesn't fit;
//   - the row has musts → one of the file's values must be a must;
//   - otherwise, if the row names anything as ok/want, one of the values must be one of
//     those; a row with nothing chosen accepts anything.
func rowMiss(row map[string]string, vals []string, have string, name func(string) string) string {
	if len(row) == 0 {
		return ""
	}
	state := func(v string) string {
		for k, st := range row {
			if strings.EqualFold(k, v) {
				return st
			}
		}
		return PrefNone
	}
	for _, v := range vals {
		if state(v) == PrefAvoid {
			return have + ", which this profile avoids"
		}
	}
	var musts, fits []string
	for k, st := range row {
		switch st {
		case PrefMust:
			musts = append(musts, k)
		case PrefOK, PrefWant:
			fits = append(fits, k)
		}
	}
	want := fits
	accept := func(st string) bool { return st == PrefOK || st == PrefWant || st == PrefMust }
	if len(musts) > 0 {
		want = musts
		accept = func(st string) bool { return st == PrefMust }
	}
	if len(want) == 0 {
		return ""
	}
	for _, v := range vals {
		if accept(state(v)) {
			return ""
		}
	}
	sort.Strings(want)
	return fmt.Sprintf("%s, not %s", have, joinNames(want, name))
}

// TargetMet reports whether a file already is the profile's target, so upgrading can stop:
// at the best resolution the profile allows, fitting the target — and inside a bitrate
// window that has a floor. A ceiling alone can't say a file is good enough (a thin encode
// sits under it too), so a target with no floor for the resolution never stops upgrades:
// they run exactly as they did before targets.
func (sp StoredProfile) TargetMet(f FileFacts) bool {
	if sp.Ideal == nil || sp.Ideal.Empty() {
		return false
	}
	if best := bestResolutionKey(sp.AllowedResolutions); best != "" && f.Resolution != best {
		return false
	}
	if w := sp.Ideal.Bitrate[f.Resolution]; w.Min <= 0 || f.BitrateMbps <= 0 {
		return false
	}
	return CheckFit(*sp.Ideal, sp.AllowedResolutions, f).Status == FitOK
}

// bestResolutionKey is the highest resolution a profile allows, as a target key ("" = any).
func bestResolutionKey(allowed []string) string {
	best, rank := "", -1
	for _, r := range allowed {
		if n := resRank[parser.Resolution(r)]; n > rank {
			best, rank = ResolutionKey(parser.Resolution(r)), n
		}
	}
	return best
}

// resolutionAllowed matches a file's resolution against the profile's list, where "SD"
// covers the profile's 576p and 480p.
func resolutionAllowed(res string, allowed []string) bool {
	for _, a := range allowed {
		if a == res || (res == "SD" && (a == "576p" || a == "480p")) {
			return true
		}
	}
	return false
}

func codecName(c string) string {
	switch strings.ToLower(c) {
	case "hevc":
		return "HEVC"
	case "av1":
		return "AV1"
	case "h264":
		return "H.264"
	case "", "other":
		return "another codec"
	}
	return strings.ToUpper(c)
}

func hdrName(h string) string {
	if strings.EqualFold(h, "DV") {
		return "Dolby Vision"
	}
	return h
}

func joinNames(xs []string, name func(string) string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = name(x)
	}
	if len(out) <= 1 {
		return strings.Join(out, "")
	}
	return strings.Join(out[:len(out)-1], ", ") + " or " + out[len(out)-1]
}

// summary describes the target in a line: "HEVC/AV1 · HDR10+ · Atmos · 4K 20–30 Mb/s".
func (f IdealFile) summary(allowed []string) []string {
	var parts []string
	pick := func(row map[string]string, name func(string) string) string {
		var musts, wants, oks []string
		for k, st := range row {
			switch st {
			case PrefMust:
				musts = append(musts, name(k))
			case PrefWant:
				wants = append(wants, name(k))
			case PrefOK:
				oks = append(oks, name(k))
			}
		}
		for _, l := range [][]string{musts, wants, oks} {
			sort.Strings(l)
		}
		switch {
		case len(musts) > 0:
			return strings.Join(musts, "/") + " only"
		case len(wants) > 0:
			return strings.Join(wants, "/")
		}
		return strings.Join(oks, "/")
	}
	if s := pick(f.Codec, codecName); s != "" {
		parts = append(parts, s)
	}
	if s := pick(f.HDR, hdrName); s != "" {
		parts = append(parts, s)
	}
	for _, a := range []struct{ key, label string }{{"atmos", "Atmos"}, {"lossless", "lossless audio"}} {
		switch f.Audio[a.key] {
		case PrefMust:
			parts = append(parts, a.label+" required")
		case PrefWant:
			parts = append(parts, a.label)
		}
	}
	for _, key := range []string{"2160p", "1080p", "720p", "SD"} {
		w, ok := f.Bitrate[key]
		if !ok || (w.Min <= 0 && w.Max <= 0) {
			continue
		}
		label := key
		if key == "2160p" {
			label = "4K"
		}
		switch {
		case w.Min > 0 && w.Max > 0:
			parts = append(parts, fmt.Sprintf("%s %.0f–%.0f Mb/s", label, w.Min, w.Max))
		case w.Max > 0:
			parts = append(parts, fmt.Sprintf("%s ≤%.0f Mb/s", label, w.Max))
		default:
			parts = append(parts, fmt.Sprintf("%s ≥%.0f Mb/s", label, w.Min))
		}
	}
	return parts
}
