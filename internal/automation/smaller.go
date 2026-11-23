package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tristenlammi/arrmada/internal/langs"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// "Find a smaller release": instead of re-encoding a film Convert would shrink, download a
// release that is already smaller and replace the file with it. Hand-picked only — the
// interactive search (rankReleases), narrowed to the releases that would be a sensible
// replacement and saying for each why it qualifies and what it would give up. Grabbing one
// is an ordinary manual grab with the file it replaces written on the grab (replace_expect),
// which buys two things on the way in:
//
//   - the download is read before it is imported, and held in Needs you when it isn't what
//     its name said in a way that matters (another resolution, no HDR, another length);
//   - once imported, the release the old file came from becomes the new file's baseline
//     (the same baseline a conversion keeps), so the upgrader never fetches it back.
//
// The replaced file goes where every upgrade's old file goes: the recycle bin. It does not
// go to Convert's originals hold — when the new file takes the old one's name (the default
// naming has no codec in it) the importer recycles the old file while placing the new one,
// and moving that elsewhere means changing how every import places files.

// ErrNoFileToReplace: the track has no file on disk, so there's nothing to look for a
// smaller copy of.
var ErrNoFileToReplace = errors.New("this version has no file on disk to replace")

// The rules a replacement is held to.
const (
	// replaceMinSaving is how much smaller a release must be than the file: a clear margin,
	// not a few percent that isn't worth a download and a seeding obligation.
	replaceMinSaving = 0.20
	// convertBloatedBPP mirrors Convert's bloatedModernBPP (internal/convert/decide.go): an
	// HEVC picture spending more bits per pixel than this is one Convert would re-encode
	// again, so replacing with it would loop.
	convertBloatedBPP = 0.15
)

// replaceCurrent is the file a smaller release would replace, as the replacement must keep
// it. Stored on the grab (replace_expect) for the check when the download lands.
type replaceCurrent struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	// Release is the release the file came from, and OrigSize its size: for a converted
	// file, what it was before the conversion. The new file's baseline once it's imported.
	Release     string   `json:"release"`
	OrigSize    int64    `json:"orig_size"`
	Resolution  string   `json:"resolution,omitempty"` // "2160p" | "1080p" | …
	Codec       string   `json:"codec,omitempty"`
	HDR         string   `json:"hdr,omitempty"` // SDR | HDR10 | HDR10+ | HLG
	DolbyVision bool     `json:"dv,omitempty"`
	Atmos       bool     `json:"atmos,omitempty"`
	Lossless    bool     `json:"lossless,omitempty"`
	AudioLangs  []string `json:"audio_langs,omitempty"`
	DurationSec int      `json:"duration_sec,omitempty"` // read from the file; 0 = unknown
	Edition     string   `json:"edition,omitempty"`
	Remux       bool     `json:"remux,omitempty"`
}

// ReplaceFit is why a release qualifies as a smaller replacement, and what it gives up.
type ReplaceFit struct {
	SavesGB  float64  `json:"saves_gb"`
	SavesPct int      `json:"saves_pct"`
	Bitrate  float64  `json:"bitrate_mbps,omitempty"`
	Why      []string `json:"why"`
	Loses    []string `json:"loses,omitempty"`
}

// SmallerCurrent is the file being replaced, as the picker shows it.
type SmallerCurrent struct {
	Release     string   `json:"release,omitempty"`
	SizeGB      float64  `json:"size_gb"`
	Bitrate     float64  `json:"bitrate_mbps,omitempty"`
	Resolution  string   `json:"resolution,omitempty"`
	Codec       string   `json:"codec,omitempty"`
	HDR         string   `json:"hdr,omitempty"`
	DolbyVision bool     `json:"dolby_vision,omitempty"`
	Atmos       bool     `json:"atmos,omitempty"`
	Lossless    bool     `json:"lossless,omitempty"`
	AudioLangs  []string `json:"audio_langs,omitempty"`
	RuntimeMin  int      `json:"runtime_min,omitempty"`
	Edition     string   `json:"edition,omitempty"`
	// The bitrate a replacement must stay inside: the profile's window for the resolution,
	// with a built-in floor when the profile sets none (FloorFromProfile false).
	FloorMbps        float64 `json:"floor_mbps"`
	CeilingMbps      float64 `json:"ceiling_mbps,omitempty"`
	FloorFromProfile bool    `json:"floor_from_profile"`
}

// NotSuitable is a release the search found that wouldn't be a sensible replacement.
type NotSuitable struct {
	Title   string  `json:"title"`
	Summary string  `json:"summary"`
	SizeGB  float64 `json:"size_gb"`
	Indexer string  `json:"indexer"`
	Why     string  `json:"why"`
}

// SmallerList is the "Find a smaller release" response: the interactive search narrowed
// to sensible replacements (Releases, each with Replace), the file they'd replace, and the
// rest with the reason each was left out.
type SmallerList struct {
	ReleaseList
	Current     SmallerCurrent `json:"current"`
	Notes       []string       `json:"notes,omitempty"`
	NotSuitable []NotSuitable  `json:"not_suitable,omitempty"`
}

// replaceRules is what judgeReplacement holds a release to beyond the file itself.
type replaceRules struct {
	floor, ceiling float64  // Mb/s; ceiling 0 = none
	runtimeMin     int      // to turn a size into a bitrate; 0 = unknown
	needLangs      []string // audio languages the profile asks for that the file has
}

// SmallerReleases searches for releases that could replace track versionID's file (0 = the
// default file) with a smaller one. movies.ErrNotFound for a track that isn't the movie's,
// ErrNoFileToReplace when it has no file.
func (c *Coordinator) SmallerReleases(ctx context.Context, movieID, versionID int64) (SmallerList, error) {
	m, v, err := c.replaceTrack(ctx, movieID, versionID)
	if err != nil {
		return SmallerList{}, err
	}
	cur := c.replaceCurrentOf(ctx, m, v)
	list, err := c.RankReleasesFor(ctx, movieID, versionID, nil)
	if err != nil {
		return SmallerList{}, err
	}
	p, _ := c.quality.Resolve(ctx, list.Profile)
	w := p.Windows[quality.ResolutionKey(parser.Resolution(cur.Resolution))]
	rules := replaceRules{ceiling: w.Max, runtimeMin: m.Runtime,
		needLangs: neededLangs(p.Languages, movieOrigLanguage(m), cur.AudioLangs)}
	if rules.runtimeMin <= 0 {
		rules.runtimeMin = cur.DurationSec / 60
	}
	fromProfile := w.Min > 0
	rules.floor = w.Min
	if !fromProfile {
		rules.floor = defaultFloorMbps(cur.Resolution)
	}

	out := SmallerList{ReleaseList: list, Current: SmallerCurrent{
		Release: cur.Release, SizeGB: gbOf(cur.SizeBytes), Bitrate: bitrateMbps(gbOf(cur.SizeBytes), rules.runtimeMin),
		Resolution: cur.Resolution, Codec: cur.Codec, HDR: cur.HDR, DolbyVision: cur.DolbyVision,
		Atmos: cur.Atmos, Lossless: cur.Lossless, AudioLangs: cur.AudioLangs, RuntimeMin: cur.DurationSec / 60,
		Edition: cur.Edition, FloorMbps: rules.floor, CeilingMbps: rules.ceiling, FloorFromProfile: fromProfile,
	}}
	// The ranked list's recommendation was for the title, not for replacing this file.
	out.Releases, out.Why, out.ChosenOver = nil, nil, ""
	for _, rr := range list.Releases {
		fit, why := judgeReplacement(cur, rr, rules)
		if why != "" {
			out.NotSuitable = append(out.NotSuitable, NotSuitable{Title: rr.Title, Summary: rr.Summary, SizeGB: rr.SizeGB, Indexer: rr.Indexer, Why: why})
			continue
		}
		rr.Replace, rr.Recommended = fit, false
		out.Releases = append(out.Releases, rr)
	}
	if out.Releases == nil {
		out.Releases = []RankedRelease{}
	}
	out.Notes = replaceNotes(cur)
	return out, nil
}

// replaceNotes are what the picker says about the file itself, before any release.
func replaceNotes(cur replaceCurrent) []string {
	notes := []string{"When the new file is imported, this one goes to the recycle bin, as an upgrade's old file does."}
	if cur.Remux {
		notes = append(notes, "This file is a remux — the disc's own picture. Convert's encode of it is a known quantity; a release's encode can be better or worse.")
	}
	if cur.DurationSec <= 0 {
		notes = append(notes, "This file's length couldn't be read, so the download can't be checked for being the same cut.")
	}
	return notes
}

// MarkReplaceGrab records, on the newest grab of title for the movie's track, the file it is
// meant to replace with a smaller one — what the download is checked against when it lands,
// and the baseline it leaves. Called right after the grab is recorded.
func (c *Coordinator) MarkReplaceGrab(ctx context.Context, movieID, versionID int64, title string) error {
	m, v, err := c.replaceTrack(ctx, movieID, versionID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(c.replaceCurrentOf(ctx, m, v))
	if err != nil {
		return err
	}
	_, err = c.db.ExecContext(ctx,
		`UPDATE grabs SET replace_expect = ? WHERE id = (
		   SELECT id FROM grabs WHERE movie_id = ? AND media_type = 'movie' AND version_id = ? AND title = ?
		   ORDER BY id DESC LIMIT 1)`, string(b), movieID, v.ID, title)
	if err == nil {
		c.grabsChanged()
		c.movies.AddEvent(ctx, movieID, "grabbed", "Looking to replace the file with a smaller release: "+title)
	}
	return err
}

// replaceTrack is movie movieID with its track versionID (0 = the default), which must have
// a file.
func (c *Coordinator) replaceTrack(ctx context.Context, movieID, versionID int64) (movies.Movie, movies.Version, error) {
	m, err := c.movies.Get(ctx, movieID)
	if err != nil {
		return m, movies.Version{}, err
	}
	versions, err := c.movies.VersionRows(ctx, movieID)
	if err != nil {
		return m, movies.Version{}, err
	}
	for _, v := range versions {
		if v.ID != versionID {
			continue
		}
		if !v.HasFile || v.FilePath == "" {
			return m, v, ErrNoFileToReplace
		}
		return m, v, nil
	}
	return m, movies.Version{}, movies.ErrNotFound
}

// probeFile reads a file's picture, length and sound (ffprobe, or the test seam).
func (c *Coordinator) probeFile(path string) (mediainfo.Info, error) {
	if c.probeFileFn != nil {
		return c.probeFileFn(path)
	}
	return mediainfo.Probe(path)
}

// replaceCurrentOf is what track v's file is: its name's facts, then the cached media info,
// a read of the file itself (its length, and its picture's HDR and Dolby Vision), and
// Convert's analysis when it still describes the file (HDR10+, lossless audio, languages).
func (c *Coordinator) replaceCurrentOf(ctx context.Context, m movies.Movie, v movies.Version) replaceCurrent {
	size := versionSizeBytes(v)
	cur := replaceCurrent{Path: v.FilePath, SizeBytes: size, Edition: v.Edition,
		Release: strings.TrimSpace(v.ConvertedFromRelease), OrigSize: v.ConvertedFromSize}
	if cur.Release == "" {
		cur.Release = upgradeBaseline(m, v)
	}
	if cur.OrigSize <= 0 {
		cur.OrigSize = size
	}
	nameOf := v.SourceRelease
	if strings.TrimSpace(nameOf) == "" {
		nameOf = filepath.Base(v.FilePath)
	}
	name := parser.Parse(nameOf)
	cur.Resolution, cur.Codec = string(name.Resolution), string(name.Codec)
	cur.HDR, cur.DolbyVision = hdrOfName(name.HDR)
	cur.Atmos = slices.Contains(name.Audio, "Atmos")
	cur.Lossless = name.AudioLossless
	cur.Remux = name.Source == parser.SourceRemux || parser.Parse(cur.Release).Source == parser.SourceRemux
	if cur.Edition == "" {
		cur.Edition = name.Edition
	}
	if f := v.File; f != nil && f.Probed {
		if f.Resolution != "" {
			cur.Resolution = f.Resolution
		}
		cur.Atmos = cur.Atmos || f.Atmos
		cur.DurationSec = f.DurationMin * 60
	}
	if mi, err := c.probeFile(v.FilePath); err == nil && mi.VideoCodec != "" {
		if mi.Resolution != "" {
			cur.Resolution = mi.Resolution
		}
		cur.Codec = mi.VideoCodec
		if mi.DurationSec > 0 {
			cur.DurationSec = mi.DurationSec
		}
		cur.DolbyVision = slices.Contains(mi.HDR, "DV")
		switch {
		case slices.Contains(mi.HDR, "HLG"):
			cur.HDR = "HLG"
		case !slices.Contains(mi.HDR, "HDR10"):
			cur.HDR = "SDR"
		case cur.HDR != "HDR10+": // the read can't see HDR10+; the name or Convert's analysis can
			cur.HDR = "HDR10"
		}
		cur.Atmos = cur.Atmos || mi.Atmos
	}
	if f := c.factsFor(ctx, v.FilePath, size); f != nil {
		if f.Resolution != "" && f.Resolution != "SD" {
			cur.Resolution = f.Resolution
		}
		if f.HDR != "" {
			cur.HDR = f.HDR
		}
		cur.DolbyVision = f.DolbyVision
		cur.Atmos, cur.Lossless = f.Atmos, f.Lossless
		cur.AudioLangs = f.AudioLangs
	}
	if cur.HDR == "" {
		cur.HDR = "SDR"
	}
	return cur
}

// hdrOfName reads a release name's HDR tags as the file's picture format and Dolby Vision.
func hdrOfName(tags []string) (hdr string, dv bool) {
	dv = slices.Contains(tags, "DV")
	switch {
	case slices.Contains(tags, "HDR10+"):
		return "HDR10+", dv
	case slices.Contains(tags, "HDR10"):
		return "HDR10", dv
	case slices.Contains(tags, "HLG"):
		return "HLG", dv
	}
	return "", dv
}

// neededLangs are the audio languages a replacement must keep: those the profile asks for
// (Must or Prefer; "original" is the film's own language) that the file has. When the
// file's languages aren't known, the profile's Musts.
func neededLangs(rules map[string]string, origLang string, fileLangs []string) []string {
	var out []string
	for code, state := range rules {
		if state != quality.PrefMust && state != quality.PrefWant {
			continue
		}
		if code == quality.LangOriginal {
			code = origLang
		}
		code = langs.Base(code)
		if code == "" || slices.Contains(out, code) {
			continue
		}
		has := false
		for _, l := range fileLangs {
			has = has || langs.Base(l) == code
		}
		if has || (len(fileLangs) == 0 && state == quality.PrefMust) {
			out = append(out, code)
		}
	}
	slices.Sort(out)
	return out
}

// defaultFloorMbps is the whole-file bitrate under which a release is taken for a starved
// encode when the profile sets no floor for its resolution.
func defaultFloorMbps(res string) float64 {
	switch res {
	case "2160p":
		return 10
	case "1080p":
		return 3
	case "720p":
		return 1.5
	}
	return 0.7
}

// nominalPixels is a resolution's full frame, in pixels.
func nominalPixels(res string) float64 {
	switch res {
	case "2160p":
		return 3840 * 2160
	case "1080p":
		return 1920 * 1080
	case "720p":
		return 1280 * 720
	case "576p":
		return 720 * 576
	}
	return 720 * 480
}

// convertLeavesMbps is the whole-file bitrate under which Convert leaves an HEVC picture of
// this resolution alone (convertBloatedBPP at 24 frames a second, plus a tenth for sound).
// A letterboxed film has fewer pixels than the full frame, so this errs high.
func convertLeavesMbps(res string) float64 {
	return convertBloatedBPP * nominalPixels(res) * 24 / 1e6 / 0.9
}

// judgeReplacement decides whether rr would be a sensible replacement for the file cur:
// the fit when it is, else why not (the first rule it fails). Every rule is about not
// trading the file for something worse: the profile takes it; same resolution; HEVC or AV1
// that Convert would leave alone; clearly smaller but inside the bitrate window, never
// under its floor; the same edition; and nothing the file has is lost — HDR, Dolby Vision,
// HDR10+, Atmos, lossless audio, the profile's audio languages.
func judgeReplacement(cur replaceCurrent, rr RankedRelease, r replaceRules) (*ReplaceFit, string) {
	rel := parser.Parse(rr.Title)
	switch {
	case rr.Blocklisted:
		return nil, "Blocklisted"
	case !rr.Eligible && rr.RejectReason != "":
		return nil, "Your profile doesn't take it: " + rr.RejectReason
	case !rr.Eligible:
		return nil, "Your profile doesn't take it"
	case cur.Release != "" && normRelease(rr.Title) == normRelease(cur.Release):
		return nil, "It's the release your file came from"
	}

	res := string(rel.Resolution)
	switch {
	case rel.Resolution == parser.ResUnknown || rel.ResolutionInferred:
		return nil, "Its name doesn't say its resolution"
	case cur.Resolution != "" && res != cur.Resolution:
		if parser.ResolutionRank(rel.Resolution) < parser.ResolutionRank(parser.Resolution(cur.Resolution)) {
			return nil, fmt.Sprintf("Lower resolution (%s; your file is %s)", res, cur.Resolution)
		}
		return nil, fmt.Sprintf("A different resolution (%s; your file is %s)", res, cur.Resolution)
	}

	if rel.Codec != parser.CodecX265 && rel.Codec != parser.CodecAV1 {
		codec := string(rel.Codec)
		if codec == "" {
			codec = "its codec isn't named"
		}
		return nil, "Not HEVC or AV1 (" + codec + ") — Convert would only re-encode it"
	}

	curGB := gbOf(cur.SizeBytes)
	if curGB <= 0 || rr.SizeGB <= 0 {
		return nil, "Its size isn't known"
	}
	saving := 1 - rr.SizeGB/curGB
	if saving < replaceMinSaving {
		if saving <= 0 {
			return nil, "Not smaller than your file"
		}
		return nil, fmt.Sprintf("Only %d%% smaller than your file — a replacement must save at least %d%%", pct(saving), pct(replaceMinSaving))
	}

	br := bitrateMbps(rr.SizeGB, r.runtimeMin)
	switch {
	case br <= 0:
		return nil, "Its bitrate can't be worked out — the film's length isn't known"
	case br < r.floor:
		return nil, fmt.Sprintf("%.1f Mb/s is under the %.1f Mb/s floor — likely a starved encode", br, r.floor)
	case r.ceiling > 0 && br > r.ceiling:
		return nil, fmt.Sprintf("%.1f Mb/s is over your profile's %.1f Mb/s ceiling", br, r.ceiling)
	case rel.Codec == parser.CodecX265 && br > convertLeavesMbps(res):
		return nil, fmt.Sprintf("At %.1f Mb/s Convert would re-encode this one too", br)
	}

	if !sameEdition(rel.Edition, cur.Edition) {
		return nil, fmt.Sprintf("A different edition (%s; your file is %s)", editionLabel(rel.Edition), editionLabel(cur.Edition))
	}

	hdr, dv := hdrOfName(rel.HDR)
	switch {
	case cur.DolbyVision && !dv:
		return nil, "Would lose Dolby Vision"
	case cur.HDR == "HDR10+" && hdr != "HDR10+":
		return nil, "Would lose HDR10+"
	case cur.HDR != "SDR" && cur.HDR != "" && hdr == "" && !dv:
		return nil, "Would lose HDR (your file is " + cur.HDR + ")"
	case cur.Atmos && !slices.Contains(rel.Audio, "Atmos"):
		return nil, "Would lose Atmos — its name doesn't say it has it"
	case cur.Lossless && !rel.AudioLossless:
		return nil, "Would lose lossless audio"
	}
	for _, l := range r.needLangs {
		if !slices.Contains(rr.AudioLangs, l) {
			return nil, "Would lose " + langName(l) + " audio"
		}
	}

	fit := &ReplaceFit{SavesGB: curGB - rr.SizeGB, SavesPct: pct(saving), Bitrate: br}
	codec := "HEVC"
	if rel.Codec == parser.CodecAV1 {
		codec = "AV1"
	}
	fit.Why = append(fit.Why, fmt.Sprintf("Saves %.1f GB (%d%%)", fit.SavesGB, fit.SavesPct), fmt.Sprintf("%s at %.1f Mb/s", codec, br))
	switch {
	case cur.DolbyVision:
		fit.Why = append(fit.Why, "Keeps Dolby Vision")
	case cur.HDR != "SDR" && cur.HDR != "":
		fit.Why = append(fit.Why, "Keeps "+cur.HDR)
	case hdr != "" || dv:
		fit.Why = append(fit.Why, "HDR, where your file is SDR")
	}
	if cur.Atmos {
		fit.Why = append(fit.Why, "Keeps Atmos")
	}
	if cur.Lossless {
		fit.Why = append(fit.Why, "Keeps lossless audio")
	}
	if cur.Edition != "" {
		fit.Why = append(fit.Why, "Same edition ("+cur.Edition+")")
	}
	if len(r.needLangs) > 0 {
		names := make([]string, len(r.needLangs))
		for i, l := range r.needLangs {
			names[i] = langName(l)
		}
		fit.Why = append(fit.Why, "Keeps "+strings.Join(names, " and ")+" audio")
	}
	if curBr := bitrateMbps(curGB, r.runtimeMin); curBr > 0 {
		fit.Loses = append(fit.Loses, fmt.Sprintf("Lower bitrate: %.1f Mb/s, your file %.1f Mb/s", br, curBr))
	}
	if cur.Remux {
		fit.Loses = append(fit.Loses, "An encode, where your file is the disc's own picture")
	}
	if rr.AudioLangsNote != "" {
		fit.Loses = append(fit.Loses, "Audio language: "+rr.AudioLangsNote)
	}
	return fit, ""
}

// pct is a fraction as a whole percentage.
func pct(f float64) int { return int(math.Round(f * 100)) }

// sameEdition reports two editions as the same cut ("" is the standard one).
func sameEdition(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func editionLabel(e string) string {
	if strings.TrimSpace(e) == "" {
		return "the standard cut"
	}
	return e
}

func langName(code string) string {
	if n := langs.Name(code); n != "" {
		return n
	}
	return strings.ToUpper(code)
}

// --- when the download lands ------------------------------------------------------------

// replaceGrab is a "Find a smaller release" grab: the track it's for and the file it replaces.
type replaceGrab struct {
	movieID, versionID int64
	title              string
	expect             replaceCurrent
}

// replaceGrabFor finds the smaller-release grab a download belongs to — by its info hash,
// else by its name — or false for any other download.
func (c *Coordinator) replaceGrabFor(ctx context.Context, hash, name string) (replaceGrab, bool) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT movie_id, version_id, title, info_hash, replace_expect FROM grabs
		  WHERE media_type = 'movie' AND replace_expect <> '' ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return replaceGrab{}, false
	}
	defer rows.Close()
	var byName *replaceGrab
	for rows.Next() {
		var g replaceGrab
		var ihash, raw string
		if rows.Scan(&g.movieID, &g.versionID, &g.title, &ihash, &raw) != nil || json.Unmarshal([]byte(raw), &g.expect) != nil {
			continue
		}
		if hash != "" && ihash != "" && strings.EqualFold(ihash, hash) {
			return g, true
		}
		if byName == nil && name != "" && (hash == "" || ihash == "") && normRelease(g.title) == normRelease(name) {
			byName = &g
		}
	}
	if byName != nil {
		return *byName, true
	}
	return replaceGrab{}, false
}

// holdSmallerMismatch is the import gate's check of a smaller-release grab: the download's
// main video is read and held for a decision when it isn't what its name said in a way
// that matters — the file there stays as it is. Any other download passes untouched.
func (c *Coordinator) holdSmallerMismatch(ctx context.Context, hash, name, contentPath string) (string, bool) {
	if c.movies == nil || c.db == nil {
		return "", false
	}
	key := strings.ToLower(hash)
	if key == "" {
		key = "name:" + normRelease(name)
	}
	if _, passed := c.smallerChecked.Load(key); passed {
		return "", false
	}
	g, ok := c.replaceGrabFor(ctx, hash, name)
	if !ok || c.hashAlreadyImported(ctx, hash) {
		return "", false
	}
	vids, err := library.FindVideos(contentPath)
	if err != nil || len(vids) == 0 {
		// Nothing to read yet (still packed, or unreadable): the import's own checks decide.
		c.log.Info("smaller release: no video to check in the download — left to the import", "release", name, "err", err)
		return "", false
	}
	main := vids[0]
	for _, v := range vids[1:] {
		if v.Size > main.Size {
			main = v
		}
	}
	var problems []string
	if mi, err := c.probeFile(main.Path); err != nil {
		problems = []string{"it couldn't be read (" + err.Error() + ")"}
	} else {
		problems = replacementProblems(g.expect, mi)
	}
	if len(problems) == 0 {
		c.smallerChecked.Store(key, true)
		c.log.Info("smaller release: the download is what its name said", "release", name)
		return "", false
	}
	title := ""
	if m, err := c.movies.Get(ctx, g.movieID); err == nil {
		title = m.Title
	}
	reason := "Grabbed to replace your file with a smaller one, but " + strings.Join(problems, "; ") +
		". Your file is untouched — Import anyway to use it, or Reject to remove it and block the release."
	_, indexerName, _ := c.grabbedMediaForHash(ctx, hash, name, "movie")
	c.addReview(ctx, Review{
		Hash: hash, Name: name, ContentPath: contentPath, MediaType: "movie", ReasonCode: ReasonReplaceCheck,
		ExpectedID: g.movieID, ExpectedTitle: title, ParsedTitle: parser.Parse(name).Title,
		Reason: reason, Indexer: indexerName, SizeBytes: main.Size,
	})
	return reason, true
}

// replacementProblems is how a downloaded replacement differs from the file it was picked
// to replace in a way that matters: another resolution, HDR or Dolby Vision missing, or
// another length (beyond 1%, at least half a minute — the same cut, read from both files).
// Empty when it's fine.
func replacementProblems(exp replaceCurrent, mi mediainfo.Info) []string {
	if mi.VideoCodec == "" {
		return []string{"no video could be read in it"}
	}
	var out []string
	if exp.Resolution != "" && mi.Resolution != "" && mi.Resolution != exp.Resolution {
		out = append(out, fmt.Sprintf("it's %s, not %s like your file", mi.Resolution, exp.Resolution))
	}
	hasHDR := slices.Contains(mi.HDR, "HDR10") || slices.Contains(mi.HDR, "HLG")
	hasDV := slices.Contains(mi.HDR, "DV")
	if exp.HDR != "" && exp.HDR != "SDR" && !hasHDR && !hasDV {
		out = append(out, "it has no HDR (your file is "+exp.HDR+")")
	}
	if exp.DolbyVision && !hasDV {
		out = append(out, "it has no Dolby Vision")
	}
	if exp.DurationSec > 0 {
		switch {
		case mi.DurationSec <= 0:
			out = append(out, "its length couldn't be read, so it can't be checked as the same cut")
		default:
			tol := max(exp.DurationSec/100, 30)
			if abs(mi.DurationSec-exp.DurationSec) > tol {
				out = append(out, fmt.Sprintf("it runs %s and your file %s — another cut, or an incomplete file",
					filmLength(mi.DurationSec), filmLength(exp.DurationSec)))
			}
		}
	}
	return out
}

// filmLength is a length in seconds as "2h 03m" (or "48m").
func filmLength(sec int) string {
	h, m := sec/3600, (sec%3600)/60
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %02dm", h, m)
}

// keepReplacedBaseline runs after a download is imported into movie movieID: when it came
// from a smaller-release grab, the release the old file came from becomes the new file's
// baseline (movies.SetReplacedFrom), so the upgrader measures releases against what was
// given up and never fetches it back. Nothing for any other download.
func (c *Coordinator) keepReplacedBaseline(ctx context.Context, movieID int64, hash string) {
	if hash == "" || c.movies == nil {
		return
	}
	g, ok := c.replaceGrabFor(ctx, hash, "")
	if !ok || g.movieID != movieID {
		return
	}
	versions, err := c.movies.VersionRows(ctx, movieID)
	if err != nil {
		return
	}
	for _, v := range versions {
		if v.ID != g.versionID || !v.HasFile {
			continue
		}
		set, err := c.movies.SetReplacedFrom(ctx, movieID, v.ID, v.FilePath, g.expect.Release, g.expect.OrigSize)
		if err != nil {
			c.log.Warn("smaller release: couldn't record the replaced release as the baseline", "movie_id", movieID, "err", err)
			return
		}
		if set {
			c.movies.AddEvent(ctx, movieID, "file.replaced", fmt.Sprintf("Replaced a %.1f GB file with a smaller release; upgrades are measured against %s",
				gbOf(g.expect.SizeBytes), g.expect.Release))
		}
		return
	}
}
