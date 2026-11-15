package music

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The matcher (MUS-31) decides, for a group of audio files, which edition of which album
// they are and which file is which track. It is pure: the caller reads the files' tags
// and gathers the candidate editions (Service.MatchCandidates), and the import places what
// the result says (MatchResult.Assignments onto MatchResult.EditionID's tracks). Nothing
// here touches a file or the database.
//
// It decides in tiers rather than pass/fail: a confident match imports, a partial album
// imports what it has when that's enough, and anything doubtful goes to Reviews with a
// per-field breakdown (MatchResult.Report) that says in words why.

// FileTags are the tags of one audio file the matcher reads (MUS-27's reader gives them;
// a file with none has the zero value).
type FileTags struct {
	Title, Artist, AlbumArtist, Album  string
	Track, TrackTotal, Disc, DiscTotal int
	Date                               string
	RecordingMBID, ReleaseTrackMBID    string
	ReleaseMBID, ReleaseGroupMBID      string
	ISRC, Barcode, Label               string
	CatalogNumber, Country, Media      string
	Compilation                        bool
	// AlbumArtistMBID is the first album-artist MBID the file carries: the library scan
	// finds (or adds) the artist by it without a name search.
	AlbumArtistMBID string
}

// tagged reports whether the tags say anything that identifies the file.
func (t FileTags) tagged() bool {
	return t.Title != "" || t.Album != "" || t.RecordingMBID != "" || t.ReleaseMBID != ""
}

// MatchFile is one audio file as the matcher sees it.
type MatchFile struct {
	Path      string
	SizeBytes int64
	// FolderDisc is the disc a "CD2" / "Disc 2" folder says the file is on (0: none).
	FolderDisc int
	// DurationMs is the measured length (0: unknown).
	DurationMs int
	Tags       FileTags
	// Fingerprint lists the MusicBrainz recording ids AcoustID names for the file's
	// audio (MUS-32); empty when it wasn't fingerprinted.
	Fingerprint []string

	// From the file name, filled by prepareFiles.
	fnDisc, fnNum int
	fnTitle       string
}

// number is the file's (disc, track) position: the tags', else the file name's. disc 0
// means unknown.
func (f *MatchFile) number() (disc, num int) {
	if f.Tags.Track > 0 {
		disc = f.Tags.Disc
		if disc == 0 {
			disc = f.FolderDisc
		}
		return disc, f.Tags.Track
	}
	disc = f.fnDisc
	if disc == 0 {
		disc = f.FolderDisc
	}
	return disc, f.fnNum
}

// title is the file's title: the tag, else what the file name says.
func (f *MatchFile) title() string {
	if t := strings.TrimSpace(f.Tags.Title); t != "" {
		return t
	}
	return f.fnTitle
}

// Name is the file's name, for reports.
func (f MatchFile) Name() string { return filepath.Base(f.Path) }

// reGenericName is a file name that is only a placeholder and a number.
var reGenericName = regexp.MustCompile(`(?i)^(?:track|audio\s*track|piste|pista|titel|title|song)?[\s._-]*0*(\d{1,3})$`)

// prepareFiles reads each file's name for a number and a title, with MatchTracks' care:
// the compact "1-04" disc-track shape is only believed when at least two files of the
// folder are named that way. A file named "01 - Artist - Title" (a compilation's usual
// shape) keeps only the title.
func prepareFiles(files []MatchFile) []*MatchFile {
	compactIn := map[string]int{}
	for _, f := range files {
		if reDiscTrack.MatchString(baseName(f.Path)) {
			compactIn[filepath.Dir(f.Path)]++
		}
	}
	out := make([]*MatchFile, len(files))
	for i := range files {
		f := files[i]
		base := baseName(f.Path)
		if m := reGenericName.FindStringSubmatch(strings.TrimSpace(base)); m != nil {
			// "track01", "Track 1", "AudioTrack 05": a ripper's placeholder. The number
			// may be right; the name says nothing about the song.
			f.fnNum, _ = strconv.Atoi(m[1])
			out[i] = &f
			continue
		}
		num := readNumber(base, compactIn[filepath.Dir(f.Path)] >= 2)
		if num.track > 200 {
			num = numbering{}
		}
		f.fnDisc, f.fnNum = num.disc, num.track
		switch {
		case num.used != "":
			f.fnTitle = titleOf(f.Path, num.used)
		case len(num.prefixes) > 0:
			f.fnTitle = titleOf(f.Path, num.prefixes[len(num.prefixes)-1])
		default:
			f.fnTitle = strings.TrimSpace(base)
		}
		out[i] = &f
	}
	return out
}

// fileTitles are the titles a file could be read as: the title, and for a "Artist -
// Title" shaped name without a title tag, the part after the artist.
func (f *MatchFile) titles() []string {
	t := f.title()
	if t == "" {
		return nil
	}
	out := []string{t}
	if f.Tags.Title == "" {
		if i := strings.LastIndex(t, " - "); i > 0 {
			out = append(out, strings.TrimSpace(t[i+3:]))
		}
	}
	return out
}

// MatchCandidate is one edition the files might be: an album's edition with its full
// track list, and what the matcher needs to know about the album.
type MatchCandidate struct {
	AlbumID    int64
	AlbumTitle string
	// AlbumYear is the release group's first year (the "original" year).
	AlbumYear int
	// Edition holds the edition with its discs and tracks (Media[].Tracks).
	Edition Edition
	// Credit is the edition's artist credit as it reads ("Various Artists" for a
	// compilation).
	Credit         string
	VariousArtists bool
	// ArtistAliases are the album artist's other names (MUS-38): tags or a folder naming
	// the artist by one fit as well as by the credit. Only the album's own artist's
	// aliases are here, so they can only bring this album closer, never another.
	ArtistAliases []string
	// Target marks the album's target edition; Pinned that the album's edition is pinned.
	Target, Pinned bool
	// TrackCredits are the credits of tracks credited differently from the edition
	// (release track id → "A feat. B").
	TrackCredits map[int64]string
	// ISRCs by recording id; RecordingAliases are merged-away recording MBIDs that still
	// name a track's recording (old MBID → current).
	ISRCs            map[int64][]string
	RecordingAliases map[string]string

	tracks []candTrack
}

// candTrack is one track of a candidate, flattened for matching.
type candTrack struct {
	ID, RecordingID       int64
	MBID, RecordingMBID   string
	Disc, Position, Index int // Index runs across discs, from 1
	Title                 string
	LengthMs              int
	Credit                string
	ISRCs                 []string
	aliases               []string
	// creditAliases are the album artist's aliases when the track carries the album's
	// own credit (none for a track credited differently).
	creditAliases []string
}

func (t *candTrack) hasAlias(mbid string) bool { return containsFold(t.aliases, mbid) }

// label is "2-04 Title" (disc-track) or "04 Title".
func (t *candTrack) label(discs int) string {
	if discs > 1 {
		return fmt.Sprintf("%d-%02d %s", t.Disc, t.Position, t.Title)
	}
	return fmt.Sprintf("%02d %s", t.Position, t.Title)
}

// trackList flattens the candidate's tracks, leaving out discs in an ignored format (a
// bonus DVD): no download of the album holds them as audio.
func (c *MatchCandidate) trackList() []candTrack {
	if c.tracks != nil {
		return c.tracks
	}
	aliases := map[string][]string{}
	for old, cur := range c.RecordingAliases {
		aliases[strings.ToLower(cur)] = append(aliases[strings.ToLower(cur)], old)
	}
	idx := 0
	out := []candTrack{}
	for _, m := range c.Edition.Media {
		if IgnoredMedium(m.Format) {
			continue
		}
		for _, rt := range m.Tracks {
			idx++
			credit := c.TrackCredits[rt.ID]
			var creditAliases []string
			if credit == "" {
				credit, creditAliases = c.Credit, c.ArtistAliases
			}
			out = append(out, candTrack{
				ID: rt.ID, RecordingID: rt.RecordingID, MBID: rt.MBID, RecordingMBID: rt.RecordingMBID,
				Disc: m.Position, Position: rt.Position, Index: idx, Title: rt.Title, LengthMs: rt.LengthMs,
				Credit: credit, ISRCs: c.ISRCs[rt.RecordingID], aliases: aliases[strings.ToLower(rt.RecordingMBID)],
				creditAliases: creditAliases,
			})
		}
	}
	c.tracks = out
	return out
}

// discCount is how many (non-ignored) discs the candidate has.
func (c *MatchCandidate) discCount() int {
	n := 0
	for _, m := range c.Edition.Media {
		if !IgnoredMedium(m.Format) {
			n++
		}
	}
	return n
}

// Match decisions.
const (
	MatchImport  = "import"  // every track of the edition is here, confidently
	MatchPartial = "partial" // some tracks, confidently, and enough of them (or the wanted ones)
	MatchReview  = "review"  // hold it for a person
)

// Reasons a match is held for review (Reviews' reason codes; the first two are the
// existing ones the matcher also gives).
const (
	MatchReasonMismatch            = "mismatch"
	MatchReasonUnmatched           = "unmatched"
	MatchReasonLowConfidence       = "low_confidence"
	MatchReasonCloseEditions       = "close_editions"
	MatchReasonPartial             = "partial"
	MatchReasonTagConflict         = "tag_conflict"
	MatchReasonFingerprintConflict = "fingerprint_conflict"
)

// MatchSettings are the matcher's thresholds (Settings → Media → Music matching).
type MatchSettings struct {
	// Strong: auto-import with no grab behind the download (beets' strong threshold).
	Strong float64 `json:"strong"`
	// Expected: auto-import when the download was grabbed for this album.
	Expected float64 `json:"expected"`
	// Review: up to here the best candidate is offered in Reviews; past it, "couldn't
	// identify".
	Review float64 `json:"review"`
	// TrackMax: a file whose track distance is above this isn't placed.
	TrackMax float64 `json:"track_max"`
	// Gap: a non-equivalent runner-up within this of the best sends it to Reviews.
	Gap float64 `json:"gap"`
	// PartialMin: a partial download auto-imports when it covers at least this share of
	// the edition (or exactly the tracks still wanted).
	PartialMin float64 `json:"partial_min"`
}

// DefaultMatchSettings are the shipped thresholds.
func DefaultMatchSettings() MatchSettings {
	return MatchSettings{Strong: 0.04, Expected: 0.15, Review: 0.40, TrackMax: 0.30, Gap: 0.03, PartialMin: 0.5}
}

// Validate checks the thresholds make sense together.
func (s MatchSettings) Validate() error {
	for _, v := range []struct {
		name string
		val  float64
	}{{"strong", s.Strong}, {"expected", s.Expected}, {"review", s.Review}, {"track max", s.TrackMax}, {"gap", s.Gap}, {"partial minimum", s.PartialMin}} {
		if math.IsNaN(v.val) || v.val < 0 || v.val > 1 {
			return fmt.Errorf("the %s threshold must be between 0 and 1", v.name)
		}
	}
	switch {
	case s.Strong > s.Expected:
		return errors.New("the strong threshold can't be above the expected one (a grab is extra evidence, so it allows more)")
	case s.Expected > s.Review:
		return errors.New("the expected threshold can't be above the review one")
	case s.TrackMax <= 0:
		return errors.New("the track maximum must be above 0, or no file is ever placed")
	case s.Gap > 0.5:
		return errors.New("the close runner-up gap must be 0.5 or less")
	}
	return nil
}

// MatchInput is one group of files and the candidates they might be.
type MatchInput struct {
	Files []MatchFile
	// FolderName is the download's (or folder's) name: the album, artist and year when
	// the tags don't say.
	FolderName string
	Candidates []MatchCandidate
	// GrabbedAlbumID is the album the download was grabbed for (0: nobody grabbed it).
	GrabbedAlbumID int64
	// ExpectedFromNames says GrabbedAlbumID comes from a library folder's names (the
	// library scan), not a grab: with untagged files that is the only evidence, so only a
	// whole album, or exactly the tracks still wanted, is placed.
	ExpectedFromNames bool
	// Forced: a person chose the album (Reviews' "import anyway", the library scan): the
	// best fit is placed, file by file, and nothing goes to Reviews.
	Forced bool
	// ForceEditionID (with Forced): the edition a person chose; the files go on it, the
	// album switching to it even when another edition is pinned.
	ForceEditionID int64
	// Wanted are the tracks the album still wants (no file yet): a partial download of
	// exactly those imports whatever its share.
	Wanted   []WantedTrack
	Settings MatchSettings
	// Policy weighs disc format and country when it is "preferred".
	Policy EditionPolicy
}

// WantedTrack is one track an album still wants.
type WantedTrack struct {
	AlbumID, TrackID, RecordingID int64
	Title                         string
}

// Pair is one file assigned to one track of a candidate.
type Pair struct {
	File     int // index into the input files
	Track    int // index into the candidate's track list
	Distance float64
	Notes    []string
}

// Scored is one candidate weighed against the files.
type Scored struct {
	Cand *MatchCandidate
	// Distance is the whole distance (what ranks candidates); Fit leaves out the missing
	// tracks, which is what the thresholds judge (a partial download is a good fit of
	// fewer tracks, not a bad one).
	Distance, Fit float64
	Pairs         []Pair
	Missing       []int // track indexes with no file
	Extra         []int // file indexes with no track
	dist          *distance
}

// Assignment is one file to place on one release track.
type Assignment struct {
	File     MatchFile
	TrackID  int64
	Distance float64
}

// Unplaced is a file the match leaves in the download, and why.
type Unplaced struct {
	File MatchFile
	Why  string
}

// MatchResult is the matcher's decision and everything the import and Reviews need.
type MatchResult struct {
	Decision string
	Reason   string // the review reason, when Decision is MatchReview
	Summary  string
	// Ranked are the candidates best first.
	Ranked []*Scored
	Best   *Scored
	// Expected says the download was grabbed for Best's album.
	Expected bool
	// AlbumID / EditionID: where the files go. Switch says EditionID isn't the album's
	// target and becomes it (MUS-30's switch, files and all) before anything is placed.
	AlbumID, EditionID int64
	Switch             bool
	Assignments        []Assignment
	Unplaced           []Unplaced
	Report             MatchReport
	// ContentRoot is the download folder the files came from, set by the import, not the
	// matcher: placement takes an album's extras only from inside it (MUS-36), and none
	// from a single-file download or a shared folder.
	ContentRoot string
}

// Match weighs the files against every candidate and decides.
func Match(in MatchInput) MatchResult {
	s := in.Settings
	if s == (MatchSettings{}) {
		s = DefaultMatchSettings()
	}
	files := prepareFiles(in.Files)
	cons := consensusOf(files, in.FolderName)
	var ranked []*Scored
	for i := range in.Candidates {
		ranked = append(ranked, scoreCandidate(&in.Candidates[i], files, cons, in.Policy))
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Distance != ranked[j].Distance {
			return ranked[i].Distance < ranked[j].Distance
		}
		return ranked[i].Cand.Target && !ranked[j].Cand.Target
	})
	res := MatchResult{Ranked: ranked}
	if len(ranked) == 0 || len(files) == 0 {
		res.review(MatchReasonUnmatched, "Couldn't identify the files: nothing in the library to compare them with.", files, s)
		res.Report = res.report(files, in, s)
		return res
	}
	best := ranked[0]
	if in.Forced {
		for _, sc := range ranked {
			if in.ForceEditionID != 0 && sc.Cand.Edition.ID == in.ForceEditionID {
				best = sc
				break
			}
		}
		res.Best = best
		res.Expected = true
		res.place(best, in, files, s)
		res.Decision = MatchImport
		if len(res.Assignments) == 0 {
			// Nothing to place: the album stays on the edition it has.
			res.Decision = MatchReview
			res.Reason = MatchReasonLowConfidence
			res.Switch = false
		}
		res.Summary = res.placedSummary()
		res.Report = res.report(files, in, s)
		return res
	}

	// A close runner-up that isn't the same edition under another name: two pressings
	// the files fit almost equally. When both are editions of one album and exactly one is
	// its target, that one wins; otherwise (two albums, or neither the target) a person
	// picks.
	if second := runnerUp(ranked, best); second != nil && second.Distance-best.Distance <= s.Gap {
		if best.Cand.AlbumID != second.Cand.AlbumID || best.Cand.Target == second.Cand.Target {
			res.Best = best
			res.review(MatchReasonCloseEditions, fmt.Sprintf("Two editions fit almost equally: %s and %s.",
				editionWords(best.Cand), editionWords(second.Cand)), files, s)
			res.Report = res.report(files, in, s)
			return res
		}
		if second.Cand.Target {
			best = second
		}
	}
	res.Best = best
	res.Expected = in.GrabbedAlbumID != 0 && in.GrabbedAlbumID == best.Cand.AlbumID

	// Held whatever the distance.
	if in.GrabbedAlbumID != 0 && best.Cand.AlbumID != in.GrabbedAlbumID {
		res.review(MatchReasonMismatch, fmt.Sprintf("Grabbed for another album, but the files fit %q best.", best.Cand.AlbumTitle), files, s)
		res.Report = res.report(files, in, s)
		return res
	}
	if n, ok := tagConflicts(files, best, in.Candidates); ok {
		res.review(MatchReasonTagConflict, fmt.Sprintf("%d of %d files carry MusicBrainz recording ids from another album.", n, len(files)), files, s)
		res.Report = res.report(files, in, s)
		return res
	}
	if n := fingerprintConflicts(files, best); n >= 2 {
		res.review(MatchReasonFingerprintConflict, fmt.Sprintf("AcoustID says %d files are other recordings than their tags or track say.", n), files, s)
		res.Report = res.report(files, in, s)
		return res
	}

	threshold := s.Strong
	if res.Expected {
		threshold = s.Expected
	}
	fit := best.Fit
	if res.Expected && len(best.Missing) == 0 && len(best.Extra) > 0 && len(best.Extra) <= len(best.Cand.trackList()) && best.dist != nil {
		// #115: every track of the edition is here, in a download grabbed for it. The extra
		// files (bonus, demo or live tracks) stay in the download, so they don't make the
		// album's own tracks a worse fit. Ranking still counts them.
		fit = round4(best.dist.value(CompMissingTracks, CompUnmatched))
	}
	nameOnly := true
	for _, f := range files {
		if f.Tags.tagged() || len(f.Fingerprint) > 0 { // identified by sound counts too
			nameOnly = false
			break
		}
	}
	if fit > threshold || (nameOnly && !res.Expected) {
		reason := MatchReasonLowConfidence
		why := fmt.Sprintf("The best fit, %s, is %.2f away (automatic up to %.2f).", editionWords(best.Cand), fit, threshold)
		switch {
		case fit > s.Review:
			reason = MatchReasonUnmatched
			why = fmt.Sprintf("Couldn't identify the files: the closest, %s, is %.2f away.", editionWords(best.Cand), fit)
		case nameOnly && fit <= threshold:
			why = fmt.Sprintf("Only the file names say these are %s, and nobody grabbed the download for it.", editionWords(best.Cand))
		}
		res.review(reason, why, files, s)
		res.Report = res.report(files, in, s)
		return res
	}

	res.place(best, in, files, s)
	if len(res.Assignments) == 0 {
		res.review(MatchReasonLowConfidence, "No file fits a track closely enough to place.", files, s)
		res.Report = res.report(files, in, s)
		return res
	}
	tracks := len(best.Cand.trackList())
	switch {
	case len(best.Missing) == 0:
		res.Decision = MatchImport
	case res.wantedExactly(in.Wanted, best):
		res.Decision = MatchPartial
	case !(nameOnly && in.ExpectedFromNames) && float64(len(res.Assignments)) >= s.PartialMin*float64(tracks) && best.Distance <= s.Review:
		// With nothing but file and folder names to go on (no tags, and the album chosen by
		// a folder's name rather than a grab), only a whole album or exactly the tracks
		// still wanted is placed: part of an album by names alone is a guess.
		res.Decision = MatchPartial
	default:
		res.Assignments = nil
		res.review(MatchReasonPartial, fmt.Sprintf("Only %d of the %d tracks of %s are here.", len(best.Pairs), tracks, editionWords(best.Cand)), files, s)
		res.Report = res.report(files, in, s)
		return res
	}
	res.Summary = res.placedSummary()
	res.Report = res.report(files, in, s)
	return res
}

// review marks the result as held, with nothing to place.
func (r *MatchResult) review(reason, summary string, files []*MatchFile, _ MatchSettings) {
	r.Decision, r.Reason, r.Summary = MatchReview, reason, summary
	r.Assignments, r.Switch = nil, false
	r.Unplaced = r.Unplaced[:0]
	for _, f := range files {
		r.Unplaced = append(r.Unplaced, Unplaced{File: *f, Why: "held for review"})
	}
}

func (r *MatchResult) placedSummary() string {
	if r.Best == nil {
		return ""
	}
	s := fmt.Sprintf("%d file(s) fit %s (distance %.2f)", len(r.Assignments), editionWords(r.Best.Cand), r.Best.Distance)
	if n := len(r.Unplaced); n > 0 {
		s += fmt.Sprintf("; %d left in the download", n)
	}
	return s + "."
}

// runnerUp is the best candidate that isn't best's edition or one equivalent to it.
func runnerUp(ranked []*Scored, best *Scored) *Scored {
	for _, sc := range ranked[1:] {
		if sc.Cand.Edition.ID == best.Cand.Edition.ID && sc.Cand.AlbumID == best.Cand.AlbumID {
			continue
		}
		if sc.Cand.AlbumID == best.Cand.AlbumID && EditionsEquivalent(sc.Cand.Edition, best.Cand.Edition) {
			continue
		}
		return sc
	}
	return nil
}

// place turns the winner's pairs into assignments on the edition the files go to: the
// winner itself, the album's target when the winner is the same tracks under another
// edition, or the pinned target when the album's edition is pinned (only files whose
// recordings are on it). Files over the track maximum, or with no track, stay in the
// download.
func (r *MatchResult) place(win *Scored, in MatchInput, files []*MatchFile, s MatchSettings) {
	r.AlbumID = win.Cand.AlbumID
	r.EditionID = win.Cand.Edition.ID
	tracks := win.Cand.trackList()
	var target *MatchCandidate
	for i := range in.Candidates {
		c := &in.Candidates[i]
		if c.AlbumID == win.Cand.AlbumID && c.Target {
			target = c
			break
		}
	}
	// translate maps a track of the winner onto the edition the files go to (ok false:
	// it has no place there).
	translate := func(ti int) (int64, bool) { return tracks[ti].ID, true }
	chosen := in.ForceEditionID != 0 && in.ForceEditionID == win.Cand.Edition.ID
	switch {
	case win.Cand.Target:
	case target == nil && win.Cand.Pinned && !chosen:
		// Pinned to an edition whose tracks couldn't be read: nowhere to put anything.
		translate = func(int) (int64, bool) { return 0, false }
	case target == nil:
		r.Switch = true // the album has no target edition yet: this one becomes it
	case EditionsEquivalent(win.Cand.Edition, target.Edition):
		// The same tracks in the same order: any equivalent edition satisfies the album.
		tt := target.trackList()
		r.EditionID = target.Edition.ID
		translate = func(ti int) (int64, bool) {
			if ti < len(tt) {
				return tt[ti].ID, true
			}
			return 0, false
		}
	case win.Cand.Pinned && !chosen:
		tt := target.trackList()
		r.EditionID = target.Edition.ID
		translate = func(ti int) (int64, bool) { return sameRecording(tracks[ti], tt) }
	default:
		r.Switch = true
	}
	placed := map[int]bool{}
	for _, p := range win.Pairs {
		f := files[p.File]
		if p.Distance > s.TrackMax {
			r.Unplaced = append(r.Unplaced, Unplaced{File: *f, Why: fmt.Sprintf("fits %s only at %.2f", tracks[p.Track].label(win.Cand.discCount()), p.Distance)})
			placed[p.File] = true
			continue
		}
		id, ok := translate(p.Track)
		if !ok {
			r.Unplaced = append(r.Unplaced, Unplaced{File: *f, Why: "its track isn't on the pinned edition"})
			placed[p.File] = true
			continue
		}
		r.Assignments = append(r.Assignments, Assignment{File: *f, TrackID: id, Distance: p.Distance})
		placed[p.File] = true
	}
	for i, f := range files {
		if !placed[i] {
			r.Unplaced = append(r.Unplaced, Unplaced{File: *f, Why: "matches no track"})
		}
	}
}

// sameRecording finds t's recording among tracks: by recording, else by a unique
// fold-equal title.
func sameRecording(t candTrack, tracks []candTrack) (int64, bool) {
	for _, o := range tracks {
		if (t.RecordingID != 0 && o.RecordingID == t.RecordingID) ||
			(t.RecordingMBID != "" && strings.EqualFold(o.RecordingMBID, t.RecordingMBID)) {
			return o.ID, true
		}
	}
	var hit int64
	n := 0
	for _, o := range tracks {
		if NormKey(o.Title) == NormKey(t.Title) {
			hit, n = o.ID, n+1
		}
	}
	return hit, n == 1
}

// wantedExactly reports whether the placed files are exactly tracks the album still
// wants: each one lands on a wanted track, and no file is left over.
func (r *MatchResult) wantedExactly(wanted []WantedTrack, win *Scored) bool {
	if len(r.Assignments) == 0 || len(r.Unplaced) > 0 {
		return false
	}
	var mine []WantedTrack
	for _, w := range wanted {
		if w.AlbumID == win.Cand.AlbumID {
			mine = append(mine, w)
		}
	}
	// Exactly: every wanted track gets a file, and every file is a wanted track. A few
	// files of an album that wants everything are a partial, not "what's wanted".
	if len(mine) == 0 || len(mine) != len(win.Pairs) {
		return false
	}
	tracks := win.Cand.trackList()
	covered := map[int]bool{}
	for _, p := range win.Pairs {
		t := tracks[p.Track]
		hit := -1
		for i, w := range mine {
			if !covered[i] && (w.TrackID == t.ID || (w.RecordingID != 0 && w.RecordingID == t.RecordingID) || NormKey(w.Title) == NormKey(t.Title)) {
				hit = i
				break
			}
		}
		if hit < 0 {
			return false
		}
		covered[hit] = true
	}
	return true
}

// tagConflicts counts files whose recording MBID isn't on any edition of best's album the
// matcher has (so belongs to another album). ok when at least a quarter of the files do.
// Only judged when the album's editions carry recording MBIDs at all.
func tagConflicts(files []*MatchFile, best *Scored, cands []MatchCandidate) (int, bool) {
	recs := map[string]bool{}
	for i := range cands {
		c := &cands[i]
		if c.AlbumID != best.Cand.AlbumID {
			continue
		}
		for _, t := range c.trackList() {
			if t.RecordingMBID != "" {
				recs[strings.ToLower(t.RecordingMBID)] = true
				for _, a := range t.aliases {
					recs[strings.ToLower(a)] = true
				}
			}
		}
	}
	if len(recs) == 0 {
		return 0, false
	}
	n := 0
	for _, f := range files {
		m := strings.ToLower(f.Tags.RecordingMBID)
		if m == "" || recs[m] {
			continue
		}
		// AcoustID placing the sound on this album outweighs a stale tag.
		onAlbum := false
		for _, fp := range f.Fingerprint {
			if recs[strings.ToLower(fp)] {
				onAlbum = true
				break
			}
		}
		if !onAlbum {
			n++
		}
	}
	return n, n > 0 && n*4 >= len(files)
}

// fingerprintConflicts counts files AcoustID names as other recordings: other than their
// own recording tag, or than the track they were assigned to.
func fingerprintConflicts(files []*MatchFile, best *Scored) int {
	tracks := best.Cand.trackList()
	n := 0
	for _, p := range best.Pairs {
		f := files[p.File]
		if len(f.Fingerprint) == 0 {
			continue
		}
		t := tracks[p.Track]
		switch {
		case f.Tags.RecordingMBID != "" && !containsFold(f.Fingerprint, f.Tags.RecordingMBID):
			n++
		case t.RecordingMBID != "" && !containsFold(f.Fingerprint, t.RecordingMBID) && !anyAlias(f.Fingerprint, t.aliases):
			n++
		}
	}
	return n
}

func anyAlias(ids, aliases []string) bool {
	for _, a := range aliases {
		if containsFold(ids, a) {
			return true
		}
	}
	return false
}

// consensus is what most of the files' tags agree on (else the folder name).
type consensus struct {
	Album, Artist, ReleaseMBID string
	ReleaseShare               float64 // share of files with the most common release MBID
	Year, DiscTotal            int
	Media, Country, Label      string
	CatalogNumber, Barcode     string
}

// plurality is the most common non-empty value and its share of n.
func plurality(vals []string, n int) (string, float64) {
	counts := map[string]int{}
	var order []string
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if counts[v] == 0 {
			order = append(order, v)
		}
		counts[v]++
	}
	best, bc := "", 0
	for _, v := range order {
		if counts[v] > bc {
			best, bc = v, counts[v]
		}
	}
	if n == 0 {
		return best, 0
	}
	return best, float64(bc) / float64(n)
}

func consensusOf(files []*MatchFile, folder string) consensus {
	pick := func(get func(*MatchFile) string) (string, float64) {
		vals := make([]string, 0, len(files))
		for _, f := range files {
			vals = append(vals, get(f))
		}
		return plurality(vals, len(files))
	}
	var c consensus
	// "Album (Disc 1)" and "Album (Disc 2)" are one album.
	c.Album, _ = pick(func(f *MatchFile) string { return StripDiscSuffix(f.Tags.Album) })
	c.Artist, _ = pick(func(f *MatchFile) string { return f.Tags.AlbumArtist })
	if c.Artist == "" {
		c.Artist, _ = pick(func(f *MatchFile) string { return f.Tags.Artist })
	}
	c.ReleaseMBID, c.ReleaseShare = pick(func(f *MatchFile) string { return strings.ToLower(f.Tags.ReleaseMBID) })
	date, _ := pick(func(f *MatchFile) string { return f.Tags.Date })
	c.Year = yearOf(date)
	dt, _ := pick(func(f *MatchFile) string {
		if f.Tags.DiscTotal > 0 {
			return strconv.Itoa(f.Tags.DiscTotal)
		}
		return ""
	})
	c.DiscTotal, _ = strconv.Atoi(dt)
	c.Media, _ = pick(func(f *MatchFile) string { return f.Tags.Media })
	c.Country, _ = pick(func(f *MatchFile) string { return f.Tags.Country })
	c.Label, _ = pick(func(f *MatchFile) string { return f.Tags.Label })
	c.CatalogNumber, _ = pick(func(f *MatchFile) string { return f.Tags.CatalogNumber })
	c.Barcode, _ = pick(func(f *MatchFile) string { return f.Tags.Barcode })
	if folder != "" {
		rel := ParseRelease(folder)
		if c.Album == "" {
			c.Album = rel.Album
		}
		if c.Artist == "" && rel.Album != "" {
			c.Artist = rel.Artist
		}
		if c.Year == 0 {
			c.Year = rel.Year
		}
	}
	return c
}

// scoreCandidate weighs the files against one candidate: files are assigned to tracks by
// the Hungarian algorithm on the track distances, then the album-level components are
// added.
func scoreCandidate(c *MatchCandidate, files []*MatchFile, cons consensus, pol EditionPolicy) *Scored {
	tracks := c.trackList()
	sc := &Scored{Cand: c}
	cost := make([][]float64, len(files))
	notes := make([][][]string, len(files))
	for i, f := range files {
		cost[i] = make([]float64, len(tracks))
		notes[i] = make([][]string, len(tracks))
		for j := range tracks {
			best := 2.0
			var bestNotes []string
			// A file whose name could be read two ways is judged by the closer reading.
			titles := f.titles()
			if len(titles) <= 1 {
				best, _, bestNotes = trackDistance(f, &tracks[j], c.VariousArtists)
			} else {
				for _, t := range titles {
					g := *f
					g.Tags.Title = t
					if d, _, n := trackDistance(&g, &tracks[j], c.VariousArtists); d < best {
						best, bestNotes = d, n
					}
				}
			}
			cost[i][j], notes[i][j] = best, bestNotes
		}
	}
	assign := assignMin(cost, 1.0)
	d := newDistance()

	// Album-level components, each only when both sides know it.
	ed := c.Edition
	if cons.ReleaseMBID != "" {
		d.addBool(CompAlbumID, !strings.EqualFold(cons.ReleaseMBID, ed.MBID))
	}
	if !c.VariousArtists && cons.Artist != "" && c.Credit != "" && !isVariousArtists(cons.Artist) {
		d.add(CompArtist, artistDist(cons.Artist, c.Credit, c.ArtistAliases))
	}
	if cons.Album != "" {
		title := ed.Title
		if title == "" {
			title = c.AlbumTitle
		}
		sd := stringDist(cons.Album, title)
		if c.AlbumTitle != "" && c.AlbumTitle != title {
			sd = math.Min(sd, stringDist(cons.Album, c.AlbumTitle))
		}
		d.add(CompAlbum, sd)
	}
	if ed.Source != "" && ed.Source != "musicbrainz" {
		d.add(CompDataSource, 0.5)
	}
	if discs := c.discCount(); cons.DiscTotal > 0 && discs > 0 {
		if diff := cons.DiscTotal - discs; diff != 0 {
			for k := 0; k < absInt(diff); k++ {
				d.add(CompMediaCount, 1)
			}
		} else {
			d.add(CompMediaCount, 0)
		}
	}
	format := primaryFormat(ed)
	switch {
	case cons.Media != "" && format != "":
		d.addBool(CompMediaFormat, compactFold(cons.Media) != compactFold(format))
	case pol.Mode == PolicyPreferred && len(pol.Media) > 0 && format != "":
		d.add(CompMediaFormat, priority(format, pol.Media))
	}
	if y := yearOf(ed.ReleaseDate); cons.Year > 0 && y > 0 {
		switch {
		case cons.Year == y || (c.AlbumYear > 0 && cons.Year == c.AlbumYear):
			d.add(CompYear, 0)
		case c.AlbumYear > 0:
			d.addRatio(CompYear, math.Abs(float64(cons.Year-y)), math.Max(1, float64(nowYear()-c.AlbumYear)))
		default:
			d.add(CompYear, 1)
		}
	}
	switch {
	case cons.Country != "" && ed.Country != "":
		d.addBool(CompCountry, !strings.EqualFold(cons.Country, ed.Country))
	case pol.Mode == PolicyPreferred && len(pol.Countries) > 0 && ed.Country != "":
		d.add(CompCountry, priority(ed.Country, pol.Countries))
	}
	if cons.Label != "" && ed.Label != "" {
		d.add(CompLabel, stringDist(cons.Label, ed.Label))
	}
	if cons.CatalogNumber != "" && ed.CatalogNumber != "" {
		d.addBool(CompCatalogNumber, normCatalog(cons.CatalogNumber) != normCatalog(ed.CatalogNumber))
	}
	if b1, b2 := digitsOnly(cons.Barcode), digitsOnly(ed.Barcode); b1 != "" && b2 != "" {
		d.addBool(CompBarcode, b1 != b2)
	}

	usedTrack := make([]bool, len(tracks))
	for i, j := range assign {
		if j < 0 || j >= len(tracks) {
			sc.Extra = append(sc.Extra, i)
			continue
		}
		usedTrack[j] = true
		sc.Pairs = append(sc.Pairs, Pair{File: i, Track: j, Distance: round4(cost[i][j]), Notes: notes[i][j]})
		d.add(CompTracks, cost[i][j])
	}
	for j := range tracks {
		if !usedTrack[j] {
			sc.Missing = append(sc.Missing, j)
			d.add(CompMissingTracks, 1)
		}
	}
	for range sc.Extra {
		d.add(CompUnmatched, 1)
	}
	sc.dist = d
	sc.Distance = round4(d.value())
	sc.Fit = round4(d.value(CompMissingTracks))
	return sc
}

// editionWords names a candidate in a sentence: `"OK Computer" (GB CD 1997, 12 tracks)`.
func editionWords(c *MatchCandidate) string {
	title := c.AlbumTitle
	if title == "" {
		title = c.Edition.Title
	}
	return fmt.Sprintf("%q (%s)", title, EditionLabel(c.Edition))
}
