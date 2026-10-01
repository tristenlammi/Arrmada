package convert

// Plan is what a conversion does to one file, separated from how it's executed: the
// compiler (compileOutputArgs) turns it into an ffmpeg command, and the estimates read it
// directly. Built per file by planFor (decide.go) from the settings and what's in the file.
//
// The output is always Matroska: it carries every audio codec (TrueHD/Atmos, DTS-HD),
// every subtitle format and the attached fonts untouched, which is the whole promise.
type Plan struct {
	// VideoCodec is the target: "hevc" | "av1", or "" to copy the video untouched (a file
	// whose picture is already right but whose tracks need cleaning up).
	VideoCodec string
	Quality    int  // CRF target; 0 = the codec's own default (maxQualityCRF)
	VFRToCFR   bool // normalize variable frame rate when present

	Audio AudioPlan
	Subs  SubPlan
}

// AudioPlan is which audio tracks a file ends up with. Audio is always COPIED — Atmos,
// TrueHD and DTS-HD pass through bit for bit; only the choice of tracks changes.
type AudioPlan struct {
	// KeepLangs keeps only these languages (empty = keep every language). "en" and "eng"
	// both match. An untagged track is kept rather than guessed at, and if nothing at all
	// would survive, every track stays: a file with no audio is broken, not tidy.
	KeepLangs []string
	// OriginalLang is the title's original language (from TMDB), kept alongside KeepLangs
	// when set — a Japanese film keeps its Japanese track even if you only listed English.
	// Per file; empty when the option is off or the language isn't known.
	OriginalLang string
	// DropCommentary removes commentary tracks (flagged as such, or titled "Commentary").
	DropCommentary bool
}

// Image-subtitle policies (SubPlan.ImageSubs).
const (
	ImageSubsKeep     = "keep"      // leave PGS / VobSub tracks alone
	ImageSubsWhenText = "when_text" // drop an image track only when a text one covers its language
	ImageSubsRemove   = "remove"    // drop every image track
)

// SubPlan is which subtitle tracks a file ends up with.
//
// A WEB-DL commonly ships thirty-odd subtitle tracks for languages nobody in the house
// reads. They cost little space, but they clutter every player's track menu.
type SubPlan struct {
	// KeepLangs keeps only these languages (empty = keep all), matched the same way as audio.
	// If the filter would remove every subtitle, none are removed: the tags are wrong.
	KeepLangs []string
	// ImageSubs is what happens to image-based tracks (PGS, VobSub, DVB). Plex can't send
	// those to most clients as-is: it burns them into the picture, transcoding the video
	// every time that subtitle is on. A text subtitle for the same language direct-plays.
	ImageSubs string
	// TextSidecarLangs lists the languages with an external .srt beside THIS file (they
	// count as a text version for ImageSubsWhenText). Per file, filled in by withSidecars.
	TextSidecarLangs []string
}
