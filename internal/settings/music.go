package settings

// Music placement settings (MUS-33, MUS-34, MUS-36).
const (
	// KeyMusicWriteTags writes the house tag style into imported music (in a staging
	// copy; a seeding file is never written). Off keeps files exactly as downloaded:
	// hardlinked, no tag writes.
	KeyMusicWriteTags = "music_write_tags"
	// KeyMusicExtras is the allowlist of extra files (artwork, booklets, lyrics, logs)
	// brought along with an album: extensions separated by spaces.
	KeyMusicExtras = "music_extras"
	// The music naming templates.
	KeyNamingMusicFolder      = "naming_music_folder"
	KeyNamingMusicTrack       = "naming_music_track"
	KeyNamingMusicCompilation = "naming_music_compilation"

	// Optional post-import transcode: convert imported music to a compact, widely
	// compatible lossy format in staging. Off by default; never touches the download.
	KeyMusicTranscode       = "music_transcode"
	KeyMusicTranscodeFormat = "music_transcode_format"
	// KeyMusicTranscodeQuality is the MP3 quality: v0, v2, 320, 256, 192.
	KeyMusicTranscodeQuality      = "music_transcode_quality"
	KeyMusicTranscodeLosslessOnly = "music_transcode_lossless_only"
)

const (
	// MusicWriteTagsDefault is on: the owner's decision (2026-10-19).
	MusicWriteTagsDefault = true

	MusicTranscodeDefault              = false
	MusicTranscodeFormatDefault        = "mp3"
	MusicTranscodeQualityDefault       = "v0"
	MusicTranscodeLosslessOnlyDefault  = true
)
