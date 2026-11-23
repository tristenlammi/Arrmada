// Package jellyfin is a thin client for a Jellyfin server's HTTP API: the connection check,
// libraries and their items, partial scans, live sessions, users, and the sign-in calls
// (password, Quick Connect, logout). It mirrors internal/plex; the services that keep
// settings, an index and health on top of it live elsewhere.
//
// What the server actually does was checked against jellyfin/jellyfin 12.2 by the live rig
// (scripts/jellyfin-it.sh, the TestIT_* tests), not taken from the OpenAPI spec:
//
// Auth. Every call sends `Authorization: MediaBrowser Token="…", Client=…, Device=…,
// DeviceId=…, Version=…`; values are URL-escaped when they hold a quote or comma, and the
// server decodes them. The legacy X-Emby-Token, X-MediaBrowser-Token and
// X-Emby-Authorization headers and the api_key query parameter are refused (401); an ApiKey
// query parameter still works, but a key in a URL ends up in logs, so it's never used. An
// API key needs no userId: /Items lists every library's items without one.
//
// Partial scans. POST /Library/Media/Updated is debounced by the server's "library monitor
// delay" (60 s by default): a reported folder appears ~60 s after the last report under the
// same library, and every new report restarts the wait (two reports 30 s apart → both
// visible ~90 s after the first). Steady imports therefore postpone each other.
//
// Naming. Jellyfin folds several files in one folder into one movie with versions only when
// every video file's name starts with the folder's exact name followed by " - <label>"
// (the label becomes the version's name):
//
//	Memento (2000)/Memento (2000) - 2160p.mkv + … - 1080p.mkv          → one movie, 2 versions
//	Tenet (2020) {tmdb-577922}/Tenet (2020) {tmdb-577922} - 2160p.mkv … → one movie, 2 versions
//	Zodiac (2007)/… - 2160p.mkv + … - 1080p {edition-Director's Cut}.mkv → one movie, 2 versions
//
// Anything else in the folder breaks the group for all of its files, and each file becomes
// its own movie, named from the folder, with the folder's movie.nfo and id tag ignored:
//
//	Dune (2021) {tmdb-438631}/Dune (2021) - 2160p.mkv   (folder tag not repeated in the file)
//	Arrival (2016)/Arrival (2016) {edition-Extended} - 1080p.mkv   (edition before the " - ")
//
// A single-file folder always matches, and its {tmdb-N} or [tmdbid-N] tag sets the TMDB id
// even with every internet fetcher off. Folders starting with a dot are skipped, as is any
// folder holding a .ignore file.
//
// Sessions. NowPlayingItem carries the source's MediaStreams (Codec, Profile, BitDepth,
// BitRate per stream) but no MediaSources and no overall bitrate; PlayState.PlayMethod is
// whatever the player reported. TranscodingInfo appears only while a transcode job runs and
// carries Bitrate (bits/s) and the configured HardwareAccelerationType ("none" for software).
// For a multi-version movie, PlayState.MediaSourceId says which version plays.
//
// Sign-in. A non-admin token can still list /Users but not admin calls (403). Quick Connect
// is on by default; an unknown or unapproved secret is 404; an approved secret keeps signing
// in until the request expires, so a caller must forget it after its first use.
//
// Emby. NewFor(ServerEmby, …) speaks to an Emby server, the one Jellyfin was forked from.
// None of this was checked against a running Emby; it follows Emby's REST API reference
// (dev.emby.media) and the old open-source server, and the fakes in testdata/emby mirror
// those shapes. The differences the client handles: every path goes under /emby; the
// credentials go in X-Emby-Authorization ("Emby Client=…, Token=…") and X-Emby-Token, never
// Authorization; libraries and users come from the /Query routes ({Items, …}), falling back
// to Jellyfin's bare lists on a 404 (older Emby); a user's picture is
// /Users/{id}/Images/Primary; item ids are numbers, not GUIDs; a transcode says per stream
// whether the decoder and encoder ran on hardware (folded into HardwareAccelerationType);
// the Watch link is /web/index.html#!/item?id=…&serverId=…; and there is no Quick Connect
// (ErrUnsupported, nothing sent).
package jellyfin
