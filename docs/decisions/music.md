# Decision: Music is rebuilt, to be better than Lidarr

_Decided by the owner on 2026-10-19. Closes [MUS-03](../roadmap/MUS.md#mus-03). The plan that follows from it is the rebuild in [docs/roadmap/MUS.md](../roadmap/MUS.md); this page records what was decided and why, so later work doesn't reopen it by accident._

## The decision

**Rebuild.** Music becomes a first-class module, built to beat Lidarr rather than copy it. The "Lidarr-lite" that MUS-04..24 described is not the target any more; its tasks are kept where they still fit, re-scoped where the new model changes them, and marked superseded where the new model replaces them.

The other two options are closed:
- **Park** would leave a 3/10 module sitting behind a Preview label indefinitely.
- **Cut** ([MUS-25](../roadmap/MUS.md#mus-25)) is not needed. It is marked won't-do.

Until the rebuild graduates ([MUS-12](../roadmap/MUS.md#mus-12)), Music stays off by default on fresh installs and keeps its Preview label. Everything ships to main behind the existing module toggle with additive migrations, because people already run Arrmada from main.

## Why Rebuild

The research (a report and five sets of notes, written 2026-10-19 and kept outside the repository) changed the question. It is no longer "can Arrmada match a mature competitor?". Lidarr's lead rests on integration, not on anything hard to build, and its worst failures are design choices a newcomer can simply not make:

| Lidarr choice | What it costs Lidarr's users | What Arrmada does instead |
|---|---|---|
| Every lookup goes through one proxy server its own team runs | The 2025 outage broke adds, searches and imports for about four and a half months, and was reopened in January 2026. An empty answer during an outage can delete an album and its files | Talk to MusicBrainz directly, cache everything, never delete on an empty answer |
| One edition per album, picked by "most files, then most tracks" | Deluxe and box-set bias; a correct standard-edition download fails "Album match is not close enough" | Store every edition and pick a target by a stated policy; any acceptable edition satisfies the album |
| Fixed 80% / 60% import gates, all or nothing | One odd track rejects a whole album; near-identical pressings are picked silently | A tiered matcher with settings, partial imports, a close-runner-up check, and Reviews for the doubtful ones |
| Quality guessed from the release name | VBR always reads "V0"; sample rate ignored; WAV ranked above FLAC | Probe the real files |
| One performer name per track; tags written in place | Featured artists lost; seeding torrents damaged by retagging | Multi-value artist tags, written in a staging copy |

Arrmada also has a head start Lidarr can't get: it already owns the household request flow, Plex monitoring (Insights already counts music plays), the movie and TV library that soundtrack suggestions need, and Books code for editions, field edits, naming templates and metadata-source upgrades. Today's Preview module already avoids several Lidarr mistakes (direct MusicBrainz, the earliest official edition rather than the biggest, never placing a file by its folder position). What it lacks is a schema wide enough for editions, credits and files, plus tag handling, a cache and real file facts. Those are what the rebuild adds.

The cost is honest: the rebuild is months of work, not the 3.5–4 weeks the Lidarr-lite was estimated at. The owner accepted that.

## Metadata: MusicBrainz direct, cached, never a central proxy

**MusicBrainz is the single source of truth** for which artists, albums, editions and recordings exist. It is the only large music database whose core data is CC0, the only one that can be self-hosted in full, and the spine that beets, Picard and Lidarr all use. Its IDs also connect everything else: AcoustID fingerprints resolve to MusicBrainz recording IDs, Cover Art Archive and Fanart.tv key art by MBID, and MusicBrainz stores links to Discogs, Deezer and Wikidata.

The rules:

1. **Direct, at most 1 request a second, through one app-wide limiter.** MusicBrainz allows about one request per second per IP, and going over gets *every* request refused with 503 until the rate drops. So every feature that talks to MusicBrainz (add artist, refresh, import, scan, Discover, requests) shares one limiter. No feature gets its own client.
2. **A User-Agent with contact details.** Every request names Arrmada, its real build version (v0.x; never "1.0") and a contact: the project URL, plus an optional contact address the admin can set.
3. **A permanent local cache.** Every MusicBrainz answer is stored in SQLite and kept. It is refreshed in the background on a slow schedule, and the old answer is served whenever MusicBrainz is slow or down. A failed or empty answer marks data "stale", never "gone": nothing is ever deleted, unmonitored or moved because an upstream call failed or came back empty.
4. **An optional self-hosted mirror.** A setting points Arrmada at a self-hosted MusicBrainz mirror instead of musicbrainz.org. With a mirror the limiter can be raised, because the limit is MusicBrainz's, not ours. (A mirror is a big box: about 100 GB of disk and 4 GB of RAM without search indexes, about 350 GB and 16 GB with them, plus a MetaBrainz token for daily updates. It must not be exposed to the internet. This is for owners who want it, never a requirement.)
5. **NEVER a central Arrmada-hosted proxy.** Arrmada will not run a metadata server that every install depends on. That single server is exactly how Lidarr lost search and import for months: one service, run by one volunteer in his spare time, serving tens of millions of requests a day. A one-person project would repeat that failure. Each install talks to MusicBrainz (or its owner's mirror) itself, and its cache keeps it working through upstream outages.

The trade-off is accepted: the first full load of a large library takes hours at 1 request a second (about three hours for 300 artists averaging 15 albums), and the cache means it is paid once.

## Fallback sources

MusicBrainz stays canonical. Other sources only fill gaps and supply art, and anything they supply is marked by source.

| Source | Policy | Why |
|---|---|---|
| **Deezer** | **On by default.** No key needed. Used for gaps: releases MusicBrainz doesn't have, plus ISRC and UPC to bridge sources | Keyless, broad coverage, carries the codes that link back to MusicBrainz |
| **Discogs** | **Opt-in**, with the user's own token entered in the app | Strong for vinyl, electronic and underground releases, but needs an account and has a 60-a-minute limit |
| **Cover Art Archive** | **First** for album art | Keyed by MBID, keyless. Art is cached locally once fetched, because CAA rides on the Internet Archive and has had outages |
| **Fanart.tv** | **Second** for album art, and the source for artist images | Keyed by MBID. The key is entered in the app, never shipped in code |
| **AcoustID** | **Second pass** for identifying files whose tags are poor or contradict the match | Fingerprints resolve to MusicBrainz recording IDs. Free for non-commercial use; the key is entered in the app |

Rules for gap-fill:
- An album that exists only on Deezer or Discogs is labelled "not on MusicBrainz" everywhere it shows.
- It is re-linked to MusicBrainz automatically once someone adds it there, without touching files, monitoring or profile (the Books metadata-source upgrade pattern).
- Sources are bridged by identifier, strongest first: MBID, then ISRC (recording), then UPC/barcode (release), then stored external IDs, then fuzzy title and duration.
- A candidate from another source competes in the matcher with a source-mismatch penalty, the way beets does it.
- Fallback sources never decide that something MusicBrainz has is wrong.

Other sources considered: iTunes Search is a possible last-resort art fallback later; Last.fm and ListenBrainz are for discovery only (R5); Spotify is not depended on (new apps get only a Premium-only development mode limited to five users, and its recommendation endpoints are closed to them).

## Tags, and copies while seeding

**Arrmada writes proper tags in one house style.** One style that Plex, Jellyfin and Navidrome all read well: every MusicBrainz ID, multi-value ARTISTS and ALBUMARTISTS, a COMPILATION flag, separate release and original dates, disc and track totals, the front cover embedded plus `cover.jpg` beside the tracks. FLAC, Ogg and Opus get Vorbis comments, MP3 gets ID3v2.4 (never ID3v2.3, which can't hold multiple values), and AAC/ALAC gets MP4 atoms. ReplayGain comes later. The full tag list is in [MUS-33](../roadmap/MUS.md#mus-33).

Tags are written in a **staging** folder on the same drive as the library, checked, and then moved into the library in **one move**. Players are never shown a half-written file, and Navidrome's "retag before you move" warning is honoured by construction.

**Retagging a file that is still seeding makes a copy, not a hardlink.** A hardlink shares the bytes with the torrent client's copy, so writing tags into it would change the seeding file and break its piece hashes. So when Arrmada needs to retag a file the download client still holds, it copies it into staging and tags the copy; the seeding file is never touched. The owner's words: *go with copies for now, see if it becomes an issue.* The cost is extra disk while the torrent seeds, and the disk guard counts it. If it becomes an issue, the options are a "don't retag, hardlink as downloaded" setting or tagging only after seeding ends; neither is built now.

Files already in the library are never retagged in place. A retag of an existing album stages a tagged copy, checks it, and swaps it in, with the original going to the recycle bin.

## Cue-sheet albums are split into tracks

_Built with [MUS-35](../roadmap/MUS.md#mus-35), 2026-10-20._ A download that is one audio image per disc plus a `.cue` sheet is split into one FLAC per track, because Plex comes first and Plex doesn't read cue sheets. The rules it follows:

- **Pregaps stay with the track before.** A track runs from its INDEX 01 to the next track's INDEX 01, so the next track's pregap (INDEX 00 to INDEX 01) is at the end of the earlier track. That's how a CD player plays the disc, what EAC, XLD and foobar2000 do by default, and what per-track rips of the same disc look like; played in order, the tracks are gapless.
- **Audio before track 1** (track 1's pregap) is imported only as a hidden track ("track 0"), and only when it is at least four seconds long and MusicBrainz lists a track 0 on that disc. Otherwise it's left out, with a log line saying so. Data tracks of an enhanced CD are left out the same way.
- **Exact, and proven.** ffmpeg decodes the image once to raw 32-bit samples on a pipe; Arrmada cuts that stream at the cue's INDEX points (CD frame × sample rate ÷ 75) and feeds each piece, one at a time, to an ffmpeg FLAC encoder at the image's own bit depth. Memory stays at ffmpeg's baseline (about 50–60 MB) however long the image is. The image's samples are hashed as they stream; then the pieces are decoded again (to 32-bit) and, joined in order, must be the image sample for sample, each with exactly its planned length. A FLAC image must also match the MD5 its own header carries. APE, WavPack and WAV images become FLAC this way too.
- **The audio must be the disc.** A cue sheet only says where to cut, so before splitting, the image's track audio must be the length MusicBrainz lists for the disc it matched (within a few seconds). With an AcoustID key set, two of the split tracks are also listened to, and the split is refused if AcoustID names other recordings for all it recognises.
- **Split late, in staging.** The image's tracks are matched first as "virtual" files named, tagged and timed from the cue sheet; the image is split only when the match is confident and placement takes its tracks. The split happens in the music folder's staging area, the download is only read, and each piece goes through the usual house-tag staging (tagged, checked, one move). A split interrupted by a crash is deleted at the next start.
- **To Reviews, never guessed:** a cue naming a file that isn't in the download, a cue that runs past the end of its image, two cue sheets that cut one image differently, a lossy image, a WAV whose header doesn't describe the file, an image whose length isn't the disc's, or a split that doesn't prove exact. A probe that merely timed out, or a disk that filled up mid-split, is tried again later instead.
- **Not split:** an image whose tracks are in the download as files already, and a cue sheet that describes per-track files. Those files are imported as they are.
- The image itself is never copied into the library; the cue sheet and rip log follow the album as extras when the extras list includes them.

## Sources excluded

Music comes from **Usenet and torrents only**, through the indexers and download clients Arrmada already has.

- **No Soulseek** (the owner's earlier ruling, 2026-10-17).
- **No YouTube** or other video-site downloaders.
- **No streaming-rip sources** (Deezer, Qobuz, Tidal, Spotify or Apple Music rippers, and services like Lucida or DABmusic). These are the subject of DMCA anti-circumvention notices, and some need account cookies to work at all.

Deezer appears above only as a **metadata** source. Arrmada never downloads audio from it.

## Players

- **Plex first.** The household listens through Plex and Plexamp (lifetime Plex Pass). Arrmada asks Plex to scan its music library after every import, and a music request is "ready" only once Plex has it. Plex mostly re-matches albums online by name even with good local tags, so clean folders and a consistent album artist matter most for Plex.
- **Tags that also work for Jellyfin and Navidrome.** The house style is chosen so that an owner on Jellyfin or Navidrome gets correct albums, compilations and multi-artist credits without changing anything: one folder per album, disc tags, `cover.jpg`, `.lrc` sidecars and multi-value tags.
- **No Subsonic server now.** Arrmada already serves audiobooks to apps; a Subsonic/OpenSubsonic server for music is a separate product. It waits until the household asks for it.

## What this changes in the roadmap

- [MUS-03](../roadmap/MUS.md#mus-03) is done (Rebuild); [MUS-25](../roadmap/MUS.md#mus-25) is won't-do.
- [docs/roadmap/MUS.md](../roadmap/MUS.md) becomes the rebuild plan in five phases: **1 Safety**, **2 Core model**, **3 Stay current**, **4 Household**, **5 Beyond Lidarr**. Existing task IDs are kept where they apply; new work is MUS-26 onward.
- The README's Music line changes only when [MUS-12](../roadmap/MUS.md#mus-12) graduates the module.

## Staying current: nothing is wanted by surprise

_Added 2026-10-20 with [MUS-13](../roadmap/MUS.md#mus-13) and [MUS-14](../roadmap/MUS.md#mus-14)._

Music re-reads every followed artist from MusicBrainz about once a day (through the cache). Lidarr's version of this regularly wants dozens of albums nobody asked for: a release-type change, a MusicBrainz import of an artist's back catalogue, or a merge. Arrmada's rule is that **a refresh only ever wants a new release**: a release group MusicBrainz didn't list for the artist before, dated within the last year, for a monitored artist whose New releases option says "Want them", and not one of more than ten for that artist in 30 days. Everything else a refresh finds is listed and not wanted, with the reason shown, and Music → New releases offers it with one click. The refresh after a change of options, and the one after MusicBrainz merges the artist's own entry, want nothing. An add that couldn't read the albums keeps its choice for the first refresh that can.

**Undated newcomers wait** (owner's decision, 2026-10-20). A release group MusicBrainz lists with no date could be an announcement or old music catalogued without one. It is listed and not wanted until a date appears; a date within the last year then makes it a new release.

**Albums no longer on MusicBrainz are never searched by themselves** (decided 2026-10-20). They stay in the library with their files and show "No longer on MusicBrainz", not as wanted. Nothing searches them automatically, though a person can search by hand.

Changing an artist's release types, or the library default, never wants or unwants an album by itself; it decides what new albums get and what is listed. Applying the types to the albums already listed is a separate "Want matching albums…" step that shows exactly what it would want or stop wanting first, and never unwants an album that holds files.

**The artist page shows the kinds the types list** ([#122](https://github.com/tristenlammi/Arrmada/issues/122), 2026-11-23). Narrowing the types (unticking EP, say) takes the albums of that kind off the artist's album list and its counts straight away, the ones listed before the change included, but only those that aren't wanted and hold no file (one the scan can't see right now counts). A wanted one, or one with files, stays, marked as outside the types. Nothing is deleted: the rows stay stored, and ticking the kind again brings them back without asking MusicBrainz. The New releases list and the Dashboard still show what a refresh found in the last 30 days, whatever its kind.

And the cache rule holds for refreshes: when MusicBrainz is down, slow, or answers with an empty list, nothing is added, removed, unmonitored or flagged. An album MusicBrainz merged follows its new id; one it removed is flagged "no longer on MusicBrainz" and kept, files and all.

## The library scan, the file check and aliases

_Added 2026-10-20 with [MUS-37](../roadmap/MUS.md#mus-37), [MUS-15](../roadmap/MUS.md#mus-15) and [MUS-38](../roadmap/MUS.md#mus-38)._

**The library scan only reads and records.** It never moves, renames, retags or deletes a file in the music folder; placing and retagging stay separate, explicit actions. It remembers each album folder's modification time, size and inode, so an unchanged folder isn't opened again, and it follows a renamed folder's files by inode instead of losing them. New folders go through the matcher against the target edition of the albums their MBIDs, tags or folder names point at; MusicBrainz is asked only for a missing track listing, or for an artist the library doesn't have (added unmonitored, only when exactly one artist has that name). A folder whose artist and album folder names exactly name a library album is judged as if it had been grabbed for that album. Anything the matcher won't place waits in Reviews, where Match… records it in place; such a folder can never be rejected, because that would delete library files.

**The file check never forgets a file on one miss, and never marks a library missing en masse.** A file that isn't found is noted; it counts as missing only when a check at least 20 hours later still can't find it, and its record stays so a remount links it back. **Missing is a flag, never a download** (owner's rule, 2026-10-20, as for movies): the album shows the files as gone from disk and nothing searches for it by itself, because the files may only be renamed or on a disk that's away; the album page's "Search for missing tracks" (and the Music page's bulk one) is a person's choice. When the folder a missing file should be in looks unmounted (the music folder empty or gone, or the nearest folder still there empty or on another device than the file was), or more than 5% of the files (and at least 20) vanish at once, nothing is marked and a health warning says so. The scan refuses an empty music folder for the same reason. The scan follows a renamed file by inode only when its recorded size and time match too, and not once it has been missing more than three days. A file changed outside Arrmada is measured again and its house-tags state rechecked. Nothing is ever deleted.

**An alias only counts as a release's whole artist part.** MusicBrainz aliases (other scripts, legal and old names) widen matching for the artist's own albums only: "Utada Hikaru - First Love" is 宇多田ヒカル's, but an alias found as words inside a longer name never matches ("Ye" names "Ye - Donda", never "Ye Vagabonds - Live"), and an alias that is another artist's name is never used. An album search tries at most two aliases, and only when the artist's own name found nothing.

## Revisit when

- Copies-while-seeding use more disk than the owner is happy with (see above).
- MusicBrainz changes its rate limit or User-Agent rules.
- The household asks to play music somewhere other than Plex (a Subsonic server).
- Deezer's keyless API becomes unreliable or changes its terms; it is a fallback, so turning it off loses gap-fill, never data.

## Owner answers (2026-10-19)

- Tag library: **TagLib via go-taglib (LGPL-2.1) approved**, behind `internal/audiotag`, with a notices file.
- Writing house-style tags is **on by default**; a setting can keep files exactly as downloaded.
- A song request imports **just that track** from whatever release carries it.
- Defaults taken without asking: cue-sheet images are split into per-track FLAC; edition policy "original"; AcoustID and Fanart.tv keys are each owner's own (features off until entered); music requests follow each person's existing auto-approve setting; Music stays off by default on fresh installs.
