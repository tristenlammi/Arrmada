# Changelog

What changed in each Arrmada release, written for the people running it. Read **Before you
update** first: it lists anything that behaves differently afterwards, or that you need to do
by hand.

Arrmada is pre-1.0 (v0.x), so a minor version can change behaviour. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions are
[semantic](https://semver.org/). How to update:
[Updating Arrmada](https://github.com/tristenlammi/Arrmada/blob/main/docs/updating.md).

## [Unreleased]

### Changed

- **A file in a language you avoid can be replaced by a lower resolution in a language you want** ([#137](https://github.com/tristenlammi/Arrmada/issues/137)). With Italian on Want and English on Avoid, an English-only 4K file is now upgraded to an Italian 1080p release; before, upgrades never dropped resolution, so it stayed English. It only happens when every audio language of the file is one you avoid — both in its audio tracks and in the name of the release it came from (an untagged name counts as the title's original language; a MULTi doesn't count, since it may carry others) — and the release's name itself states a language you want or require, and not when "Replace for" is set to resolution only. A language that is only assumed (an untagged release read as the original language, the original a MULTi carries) doesn't count. It happens automatically at most once per film version and per episode: after that, the file is only replaced by ordinary upgrades or by a release you pick yourself. Under a profile that avoids a language, an episode file that isn't all in avoided languages is also no longer replaced on the bitrate margin alone by a same-resolution release whose name says it carries only avoided languages. The upgrade sweep never grabs a season pack for this, but when a pack is imported, any of its episodes can still replace an avoided-language episode file with its lower-resolution copy in a language you want. Nothing else ever replaces a file with a lower resolution.

## [0.6.0] - 2026-11-23

### Highlights

- **Emby** connects through the Jellyfin card (#60): Watch links, library updates, Insights and sign-in with Emby users.
- **Convert can be told what to leave alone**: a film, a show or a season, a library folder, or a resolution such as 4K.
- **Find a smaller release** for a film instead of converting it: a search narrowed to releases that are already smaller and give up nothing your file has.
- **Convert on the GPU handles HDR10 and HLG**, on cards that prove at startup they keep the HDR; interlaced files (DVDs, broadcast TV) can be converted at last; and every HDR conversion is checked before it replaces the original.
- **An episode a release split into "Part 1" and "Part 2" is joined back into one** (Disney+ rips of Family Guy), so the season places correctly.
- **Bulk actions**: delete many requests at once (optionally stopping the search for their titles), and unblock many releases at once (#133).
- **Seerr imports** no longer bring back deleted or finished requests and search for them all at once (#130).
- **Fixes**: Settings no longer breaks after a library scan (#136), "ENG Sub ITA" isn't taken for Italian audio (#58), unticked music release types leave the artist page (#122), long single-file audiobooks play in Lissen, and the bundled SABnzbd answers over Tailscale.

### Before you update

- **Rolling back to v0.5.0 needs `--with-db`**: this version adds database changes (Convert's exclusions, the file a Convert skip is about, and what a "smaller release" grab must match).
- **Files that came in under "ENG Sub ITA"-style names are read as English audio** (#58). On a profile with an audio language rule for the subtitle's language, they now show a red badge and, with upgrades on, are replaced when a release with that audio turns up.
- **Convert test-encodes some files again.** Test results now record which encoder made them, and ones from before this update don't say, so files they covered show estimates until their next test encode (a few minutes each).
- **Convert's HDR check is stricter**, on the CPU too: a converted HDR file whose HDR kind, colour tags, mastering display or light level don't come back keeps the original.
- **New installs leave Dolby Vision files alone** in Convert. An install that has used Convert keeps dropping the Dolby Vision layer as before, unless you choose otherwise.
- **AV1 conversions keep to the cores you set**, so on a machine where they used every core they may take longer.
- **Very long single-file audiobooks** (32 hours or more, with chapters) reach listening apps as one track per chapter; apps that already downloaded one may download it again. Saved places carry over.
- **Music**: albums of a kind you unticked in Release types leave the artist page unless they're wanted or have files.
- **Bundled SABnzbd**: `update.sh` restarts it once to let Tailscale addresses reach its page.
- **Emby** is built from Emby's documentation and hasn't been tried against a real Emby server yet; tell us if something doesn't work.

### Added

- **Tell Convert what to leave alone.** Until now the only way to keep Convert off a file was to cancel it, which held it back for a month. Nothing is left out until you choose it, so a library converts as before.
  - **Don't convert this** in a film's or a show's ⋯ menu. While it's on, the page shows **Not converted** with a button to undo it. A file's plan card under Convert → Library has the same switch: the film, or the episode's show or just its season.
  - Settings → Convert → **Leave alone**: files that are SD, 720p, 1080p or 4K (by the picture's real size, as Convert read it from the file), and a switch for each Movies and TV library folder. The titles you've left out are listed there too, each with the way back.
  - Left-out files are never picked, aren't counted as worth converting or in the space still to save, and say why on their plan card. The Overview says how many files worth converting each rule is holding back.
  - Leaving a title out takes its files off the Convert-now queue. A conversion already running for it finishes (cancel it under Convert if you'd rather it didn't); converting it by hand is refused until you turn the rule off.
- **Find a smaller release, instead of converting.** A film Convert would re-encode now has **Find a smaller release** on its conversion plan card (Convert → Library, open the row), and every film's file has it on the movie page too. It searches your indexers and shows only the releases that would be a sensible replacement, each saying what it saves, what it keeps and what it gives up. A download takes minutes; a re-encode can take a day.
  - A replacement must be at least 20% smaller, the same resolution and edition, HEVC or AV1 that Convert would leave alone, and inside your profile's bitrate window. When the profile sets no floor for that resolution, a built-in one keeps out starved encodes (3 Mb/s at 1080p, 10 Mb/s at 4K).
  - It never gives up what your file has: HDR, Dolby Vision, HDR10+, Atmos, lossless audio, and the audio languages your profile asks for. What the search found that doesn't qualify is listed with the reason.
  - When the download finishes, Arrmada reads it before importing it. If it isn't what its name said — another resolution, no HDR or Dolby Vision, or a different length (another cut) — it waits in **Needs you** and your file stays as it is.
  - Once imported, the release your old file came from becomes the new file's baseline, so upgrades are measured against it and the big file is never fetched back.
  - Nothing happens on its own: you pick each one. The replaced file goes to the recycle bin, like an upgrade's old file — not to Convert's originals hold, so there's no Revert in Convert's History for it. Films only for now.
- **Emby support** ([#60](https://github.com/tristenlammi/Arrmada/issues/60)). Emby connects through the Jellyfin card in Settings → Connections: set **Server** to **Emby**, add its address and an API key (Emby: Dashboard → Advanced → API Keys).
  - Everything Jellyfin does works with Emby too: Watch links into Emby's web app, library updates after every change, requests counting as ready once Emby has them, Recently added, watch monitoring in Insights, and sign-in and account linking with an Emby username and password.
  - Emby has no Quick Connect, so it isn't offered. It's Jellyfin or Emby, alongside Plex, not both at once.
  - The app names and colours it as Emby wherever it shows. Nothing changes for a Jellyfin connection.
- **Fix sync, for one subtitle or a whole show.** Check sync could say a subtitle ran late, but the only way to move it was half a second at a time, one episode at a time.
  - **Fix sync** on a subtitle's card measures it against the speech and moves it into sync.
  - **Fix subtitle sync…** in a show's ⋯ menu does that for every episode's `.srt`, as one background job. Each change is listed under Subtitles → Logs.
  - It moves a subtitle only when the measurement is sure. Small offsets (from about 0.12 s) are settled by where lines start against where speech starts, so lines that linger on screen don't throw it. Where that can't be told (music under everything, loosely timed subtitles, audio in more than one language), only an offset over 0.3 s or a frame-rate mismatch is moved.
  - A show-wide fix passes over subtitles taken from the video itself (its own track, AI made from its audio, a hash-matched download). Subtitles inside the video file are never touched.
  - A subtitle that changes while it's being measured is left alone.
- **Finer timing buttons**: −0.1 s and +0.1 s beside the half-second and one-second steps.
- **Staff API keys can read history and a title's detail** (#123, thanks @zorcerer). A manager's or admin's personal API key can now call `GET /api/v1/history`, `GET /api/v1/movies/{id}` and `GET /api/v1/series/{id}`, so a media server's scanner or an autoscan tool can rescan only what changed without signing in with a password. A read-only key is enough.
  - Requesters' and read-only accounts' keys still get `403`, and a key loses this the moment its owner stops being staff.
  - Nothing else in the staff library opens to keys: adding, changing and deleting titles, the queue and wanted stay sign-in only.
- **Unblock many releases at once on the Blocklist** (#133, thanks to the contributor of #134). Tick entries, tick the whole page, or pick **Select all … matching this filter** to take every entry the current type and search match, on every page. One confirm unblocks them all. Anything blocked after you picked "all" stays blocked.
- **An episode a release split in two is joined back into one.** Some releases cut a double-length episode into "Part 1" and "Part 2" files and count each as an episode (Disney+ rips of Family Guy do), where the show's episode list has it once. Such a download used to be held in Needs you with "Titles disagree", and mapping it by hand couldn't keep both halves.
  - The parts are joined into one file, placed on the one episode they are. Nothing is re-encoded, and the parts in the download are never changed, so it keeps seeding.
  - The episodes after them are placed that much earlier, by their titles, or by the count alone where a title names nothing, as long as every title in the season that does name an episode agrees.
  - Parts are only joined when they have the same tracks, encoded the same way (subtitle styles included), and start on time; the result is checked to have the parts' tracks and be as long as them together. Otherwise the download is held in Needs you and says why.
  - The joined file keeps the first part's tracks in their order with their default and forced flags, every part's chapters at their place in the whole, and the title. Cover art and timecode tracks are left out.
  - A join that fails, or whose result doesn't check out, holds the download in Needs you with the reason. It isn't tried again by itself: a join rewrites the whole episode.
  - Joining needs ffmpeg and ffprobe (the Docker image has them). Without them the download is held and says so.
  - Joining needs free space in the TV folder for the joined file plus 1 GB, and the TV folder's disk or share to be there. When either isn't, nothing is written; the rest of the download is placed and the join is tried again later. It runs at low priority, like Convert.
  - What a join stopped by a restart leaves in the TV folder's hidden .arrmada-join folder is cleared within the hour.
  - A season with an extra episode the list doesn't have (Family Guy season 6's "100th Episode Special") is still held.
- **Reject on downloads held for "Titles disagree" or "Numbering".** Those cards only offered Map files…, Choose different show… and Dismiss; Reject & find another removes the download, blocklists the release for that show and searches again.
- **Delete many requests at once.** Staff can tick requests on any tab of the Requests page (not only Needs approval), or **Select all** to take every request the tab, type and search match, beyond the rows loaded so far, then **Delete** them in one go. As with deleting one request, the library is left as it is: titles they added stay in Movies and Series and are still looked for, downloads carry on and files are kept.
  - Tick **Also stop looking for these titles** in the confirmation (it starts unticked) to unmonitor their movies and turn searching off for their shows as well, the same as **Unmonitor** in Movies and **Searching off** in Series. Files are never touched.
  - A title is changed only when the deleted requests were all that held it. One with another request still waiting or approved, one none of whose deleted requests was ever approved, and books are left as they were. The message after says how many titles were stopped and why the rest were left.

### Changed

- **The Jellyfin (or Emby) card in Settings → Connections says what its public address is for** ([#129](https://github.com/tristenlammi/Arrmada/issues/129)): Watch links in notifications and "ready" messages use it, and left as the server URL they point at the connection address, which usually only opens on your home network.
- **Sub-second timing reads exactly**: "shifted −0.45 s", "runs 0.45 s late", where it used to round to a tenth.
- **Very long single-file audiobooks reach listening apps as one track per chapter.** This applies to an `.m4b`/`.m4a` of 32 hours or more that has chapter marks. Apps that already downloaded such a book will see it as changed and may download it again; saved places carry over.
- **Convert: new installs leave Dolby Vision files alone.** Dropping the Dolby Vision layer (keeping the HDR10 or HLG picture under it) is the one thing Convert does by default that removes something people choose their files for, so a new install now leaves those files' pictures as they are until you choose otherwise in Convert's settings. Their tracks are still tidied, and the Switch-on summary says how many were left alone.
  - An install that has already used Convert keeps what it did: dropping the layer, unless you chose something else. Nothing you saved changes.
- **Convert: "Always 10-bit" applies on the GPU too.** GPU encodes of 8-bit files stayed 8-bit whatever the setting said. Each GPU encoder is now checked at startup for 10-bit output; one that can write it follows the setting like the CPU does, and one that can't (older Intel and NVIDIA chips) keeps 8-bit files 8-bit, which Settings now says under **Use my GPU**.
- **Convert: HDR10 and HLG films can encode on the GPU.** With **Use my GPU** on, every HDR file used to go to the CPU, which for a 4K HDR10 remux can take a day or more. Each GPU encoder that writes 10-bit is now tested at startup on a small HDR10 clip and a small HLG clip, with the settings a real conversion uses, and the result is read back: the BT.2020 colour tags, the HDR curve, and for HDR10 the mastering display and light level (MaxCLL). Only an encoder that kept all of it gets those files; on any other GPU they encode on the CPU as before. Settings says under **Use my GPU** which kinds the GPU takes.
  - HDR10+ and Dolby Vision always encode on the CPU, as before.
  - Every converted HDR file is now checked before it replaces the original, whichever encoder made it: same kind of HDR, same colour tags, same mastering display and light level. A GPU's file is also checked without its container (the container can show the HDR when the video itself lost it). A file that fails keeps the original; if a GPU made it, that kind of HDR goes to the CPU for the rest of the run and the film is converted there next time.
  - On a GPU, HDR films are decoded by the CPU (VAAPI), as in the startup test, so the HDR information reaches the encoder.
  - An HDR film already part-encoded in pieces on the CPU finishes on the CPU.
  - Not yet tried on a real GPU: the startup test decides per card and driver.
- **Convert: a test encode just short of the 20% saving is tried again before the file is given up on.** The test encode samples a minute of the film, which can be a few points out either way. A file it puts under 10% smaller is still left alone at once; one between 10% and 20% now waits a week and is tested again on other scenes, and is left alone only after three tests in a row fall short. Problems lists them as waiting for a retest.
- **Convert: SVT-AV1 stays within the CPU cores you set.** The AV1 encoder bundled in the Docker image (SVT-AV1 3.1) no longer reads its old core setting as a number of cores, so AV1 encodes could use the whole machine. They are now held to the cores Convert is allowed (Settings → Convert → Advanced). With several conversions at once, AV1 encodes share those cores rather than taking that many each.

### Fixed

- **Convert: interlaced files (DVDs, broadcast TV) could never pass the quality check.** The conversion deinterlaces the picture, but the check compared it with the original's combed fields, so even a near-perfect encode scored about 0.96 and every interlaced file ended up in Problems after three test encodes. The original is now deinterlaced the same way before the comparison (measured on a 576i clip: 0.997 where it scored 0.960). Progressive files are checked exactly as before.
  - The check also compares with the film's own picture when a file has cover art stored in front of it.
- **Convert: a file marked "not smaller", "couldn't reach the quality bar" or blocked after three failures stayed that way after it was replaced.** These are now about the file, not the title: an upgrade, a remux or any other replacement is judged afresh, and the Overview counts it again. The file they were about stays skipped. Existing entries carry on exactly as before until their file changes.
- **Convert: a GPU's verdict kept files from the CPU.** A file a GPU test encode said wouldn't shrink enough stayed skipped after the GPU was turned off, though the CPU makes smaller files. Test-encode results now record which encoder made them: a GPU's "not smaller" holds only while the GPU is in use, and switching encoders measures files again. Results from before this update don't say which encoder made them, so the Library shows estimates for those files until they are next test-encoded.
- **Convert: HDR10+ could go unnoticed in a file with cover art in front of the film**, and the file was then converted as plain HDR10, losing it. The HDR10+ check now reads the film's own picture.
- **Convert: the mastering display of some HDR10 files was read wrongly.** Where a file's mastering display is stored only in its Matroska container and not in the video itself, the numbers come back as reduced fractions ("53/200"), were read off as nonsense, and a CPU conversion wrote those into the new file. They are now converted properly.
- **Convert: an audio track cut short in the converted file would have passed the checks.** Each copied audio track's length is now compared with the original's before the swap (Matroska and MP4 originals, which record it), and a short one keeps the original.
- **Downloads from Convert and Settings → System's Backups name the file safely.** Their download name went into the header as it was, so a name with a quote, a semicolon or a line break could cut it short or break it. They now build it the way book and audiobook downloads do, which also keeps accented and other non-English names intact.
- **Music: EPs stayed on an artist's page after EP was unticked in Release types** ([#122](https://github.com/tristenlammi/Arrmada/issues/122)). The types only decided which new albums a refresh added; albums listed before the change stayed.
  - An artist's album list (and the album counts on it and in the library) now shows only the kinds its release types list, as soon as you save. That's the library default, or the artist's own choice under Options.
  - An album of a kind you unticked stays when it's wanted or has files, marked "Not a kind this artist's release types list". To stop wanting the ones without files, use **Want matching albums…** under the artist's Options.
  - Nothing is deleted or unwanted: the hidden albums stay stored, and ticking the kind again brings them back. A note under the list says how many are hidden. If you unticked a kind before this update, its unwanted albums without files leave the page when you update.
- **"ENG Sub ITA" releases grabbed for a profile that requires Italian audio ([#58](https://github.com/tristenlammi/Arrmada/issues/58)).** Italian trackers name an episode that has English audio and Italian subtitles "ENG Sub ITA" (or "Sub-ITA"), and that was read as Italian audio, so an English-only file passed an Italian Must.
  - A language named after Sub, Subs or Subbed is now read as subtitles, and the language before it as the audio. "ENG.SUBS" alone still means English subtitles, and a name that also says ITA for the audio ("ITA.ENG.Sub.ITA") still counts as Italian.
  - Files already in the library are read the same way: one that came in under such a name now shows a red badge and, with upgrades on, is replaced when a release with Italian audio turns up. Only profiles with an Audio language rule for the subtitle's language are affected.
  - The "MULTi and Dual Audio releases count" switch lets more through, not less: on, a MULTi or Dual release that doesn't name Italian is taken as maybe having it. Leave it off for a strict Italian Must.
- **Settings stopped opening after a library scan until Arrmada was restarted** (#136). A TV folder the scan found nothing for went on the needs-review list without an empty list of matches, and the page crashed reading it; a reload brought the same list back. The list now always comes with one, the page shows such a folder as "No candidates", and if the needs-review list ever can't be shown, the rest of Settings → Library still works.
- **Importing from Overseerr, Jellyseerr or Seerr brought back requests for titles you had deleted, and searched for all of them at once** ([#130](https://github.com/tristenlammi/Arrmada/issues/130)). A request whose media was later deleted from the server (shown as removed or deleted in Seerr) came in as a new approved request, and every imported title was searched on the next sweep.
  - Requests whose media was deleted or blocklisted, and declined ones, are left out.
  - Finished requests (completed, or already available) come in as ready only when the title is already in Arrmada's library, and are never searched. Import your Radarr and Sonarr library first so they find their titles.
  - Pending and failed requests become pending requests for you to decide.
  - Approved requests still on their way are added to the library and searched a few at a time, like a Radarr or Sonarr import, not hundreds at once.
  - Already imported too much? Delete the requests in bulk from the Requests page and tick **Also stop looking for these titles**, so the titles they added aren't looked for.
- **"Source error" at every chapter of a very long audiobook in Lissen.** Lissen opens the file again for each chapter, and a 50-hour book in one file needs more memory than the phone gives it when two chapters are open at once.
  - Arrmada now cuts each chapter from the file the first time an app asks for it, without re-encoding. The file on disk is never changed, and Arrmada's own player still plays it whole.
  - The cut chapters are kept under `audioserver/chapters` in Arrmada's data folder: up to 8 GB, and a chapter nobody has asked for in a week is removed. They never take that disk's last 1 GB: short of room the oldest go first, and failing that the chapter isn't cut and the app is told it isn't available.
- **"External internet access denied" on the bundled SABnzbd's page, over Tailscale.** With Tailscale on the server and on a computer, even one on the same network reaches the server from a Tailscale address, which SABnzbd counted as the internet.
  - Its page now answers Tailscale's range (100.64.0.0/10) as well as home-network addresses. Anything else is still refused, and the page keeps its login.
  - New installs start that way. On an existing one, `update.sh` sets it and restarts SABnzbd once; the all-in-one image sets it at its next start. A SABnzbd where you set `local_ranges` yourself is left alone.
- **"Can't tell which folder … lives in" for a show holding another show's files.** An early library scan could match a folder to the wrong show ("Power" taken for "Blossoms of Power"), which then recorded that folder's files as its own. Once the right show was in the library too, the folder read as shared: nothing could be imported for the wrong one, and nothing in the app could clear it.
  - The show's page now says when files recorded for it look like another show's, lists them with the show they look like, and offers to forget them. Nothing is forgotten until you confirm, and no file is touched: the episodes go back to missing, and the show's history says so.
  - A file is listed when its name is another library show's and not this one's under any title or alias, and that other show has the same file recorded or keeps its own files in that folder. A sequel kept under the first show's name looks the same, which is why you are asked: add the name as an alias on the show instead if the files are its own.
- **`install.sh --with-sabnzbd` (or `--with-prowlarr`) on an install that's already there.**
  - Running it again after a failed first try starts the companion. It used to see the setting already saved, update Arrmada and start nothing.
  - An install set up by an older installer gets its media folder mounted into SABnzbd. The installer couldn't read that older file and skipped the mount without saying so, leaving SABnzbd's downloads in Arrmada's own storage. The mount is added to the file as it is, so lines of your own in it stay.
  - A download the registry refuses for a moment ("toomanyrequests") is tried again, and when Docker can't start something beside a running Arrmada the installer says that, where it said Arrmada didn't start.

## [0.5.0] - 2026-11-15

### Highlights

- **Delete a season's files**, and deleting a show or a season cancels its downloads in
  progress (#111).
- **Remove several downloads at once** from Activity's queue (#112).
- **Music looks like a library**: artist pictures and album covers (#113). Music imports
  are more forgiving too: a held folder can match another pressing (#116), bonus and demo
  tracks no longer hold an album back, and searches skip live albums, demos and best-ofs
  that share a studio album's name (#115).
- **Convert's "Same as Subtitles" follows each title's Subtitles profile** (#100), so the
  embedded subtitle tracks a profile doesn't want go too.
- **Forget missing files** for files you deleted on purpose outside Arrmada (#97).
- **Convert no longer waits 12 hours** on a fresh Usenet download (#99).

### Before you update

- **Convert may remove more embedded subtitle tracks** if its subtitle setting is
  **Same as Subtitles** (#100). For a language a title's Subtitles profile names, the full
  and SDH tracks the profile doesn't want now go too: with "Only when the audio is another
  language", an English film's full English track goes, and under "Never SDH" an SDH track
  goes when a plain one stays. Forced tracks always stay. Removing a track can't be undone
  once the original has left Convert's hold. With **Tidy tracks even when that's all a file
  needs** on, files already converted are tidied again by these rules. To keep every track
  of your languages, choose **These languages** in Convert's settings and list them.
- **Music searches no longer grab live albums, demos, best-ofs, remix albums, karaoke or
  instrumentals for a studio album of the same name** (#115). An album that is that kind
  on MusicBrainz still gets them.
- **Deleting a show or a season cancels its downloads in progress by default**, with their
  partial data (#111). Untick it in the dialog to let them finish.
- **A show with searching off no longer grabs a replacement by itself** when a download
  stalls, fails or disappears. Try another release in Needs you still does.
- **An already-imported season pack only refills monitored episodes.** It used to put back
  any missing episode of its season, monitored or not, from the pack still seeding.
- **Rolling back to v0.4.0 needs no `--with-db`**: this version adds no database changes.

### Added

- **Remove a book from Continue listening** on the Listen tab, with Undo. It's the same as
  the listening apps' "Remove from Continue Listening": only for you, your place is kept, and
  listening again puts the book back.

- **Forget missing files**, in a show's ⋯ menu ([#97](https://github.com/tristenlammi/Arrmada/issues/97)). If you deleted a show's files on purpose, outside Arrmada, a rescan refuses to clear them in case a disk is down. This is how you say it wasn't one: the episodes go back to missing, and nothing on disk is touched. A TV folder that looks unmounted is still refused.
- **Remove several downloads at once** ([#112](https://github.com/tristenlammi/Arrmada/issues/112)). Activity's queue has **Select**: tick downloads (or All, which picks what the current filters show), then Remove, keeping or deleting their files. Each one goes exactly as a single remove does. Blocking and "stop wanting" stay on the single remove.
- **Artist pictures and album covers in Music** ([#113](https://github.com/tristenlammi/Arrmada/issues/113)). Artists show as picture cards, and every album row on an artist's page has its cover. MusicBrainz has no artist photos, so an artist shows one of its album covers, preferring an album you have. A missing or broken picture shows the artist's initials or a record icon.
- **Delete a season's files** ([#111](https://github.com/tristenlammi/Arrmada/issues/111)). A season has **Delete files…** beside Monitor. Its files and their subtitles go to the recycle bin, and the dialog says how many and how big first. The season stops being monitored by default, so it isn't downloaded straight back. A file that also holds another season's episode stays, and the dialog names it.
- **Deleting a show or a season cancels its downloads in progress** ([#111](https://github.com/tristenlammi/Arrmada/issues/111)), with their partial data, as deleting a movie already did. It's on by default and the dialog names each download. A season only cancels downloads grabbed for that season, never a pack that holds other seasons. Left running, a removed show's download waits in Needs you when it finishes, and a season's imports into the season as before. Finished downloads that are seeding are left to your seeding rules, and a season pack still seeding no longer puts an unmonitored season's deleted files back.

### Fixed

- **Convert no longer waits 12 hours on a Usenet download** ([#99](https://github.com/tristenlammi/Arrmada/issues/99)). A file picked before SABnzbd's folder was cleared read as "still seeding". The first check now comes back after 25 minutes, and only a file still linked after that waits 12 hours.
- **Tidying tracks is no longer thrown away over a few extra bytes** ([#98](https://github.com/tristenlammi/Arrmada/issues/98)). Rewriting a file can add a little even with tracks removed, so a tidy may now come out up to 1% bigger. Files already skipped for this stay skipped until you choose Try again in Convert's Problems.
- **A show with searching off no longer re-grabs on its own** when a download stalls, fails or disappears. Try another release in Needs you still works, because that's you asking.
- **A film named with its saga number imports** ([#74](https://github.com/tristenlammi/Arrmada/issues/74)): `Star Wars The Force Awakens Episode VII (2015)` grabbed for Star Wars: The Force Awakens (2015) is no longer held. It has to be the film it was grabbed for, with the same year.
- **Matching a held music folder tries every edition of the album** ([#116](https://github.com/tristenlammi/Arrmada/issues/116)). Match… on a folder the library scan couldn't place weighed only the album's chosen edition, so another pressing with different track lengths failed with "no file in the folder fits a track". Now, for an album with no files yet, the edition that fits best takes the files, and the album moves to it unless it's pinned. An album that already holds files stays on its edition unless you choose one with "Match as…". Nothing is moved or renamed on disk, and the album keeps its edition if nothing is recorded.
- **Bonus and demo tracks no longer hold a whole album back** ([#115](https://github.com/tristenlammi/Arrmada/issues/115)). When a download grabbed for an album has every track of it plus extras, the extras stay in the download and the album imports. Before, enough extras pushed the match score over the limit and the whole download went to Needs you.
- **Music searches skip live albums, demos, best-ofs and remixes that share a studio album's name** ([#115](https://github.com/tristenlammi/Arrmada/issues/115)). "Album (Live at Donington)" no longer gets grabbed for "Album". It's still allowed when the album is that kind itself on MusicBrainz, when its own title or artist says the word ("Live Through This", the band Live), or when the words only describe bonus tracks ("Deluxe incl. Live Tracks", a "2019 Remix" reissue). Interactive search shows the reason.
- **Convert's "Same as Subtitles" follows each title's Subtitles profile** ([#100](https://github.com/tristenlammi/Arrmada/issues/100)), not just its languages. With English set to "Full + forced", "Never SDH" and "Only when the audio is another language", an English show kept its full English and SDH tracks. Now the full and SDH tracks go there, and only the forced one stays. "Never SDH" removes an SDH track only when a plain full track of that language stays. Forced tracks always stay, including ones titled signs, songs or foreign. The audio only counts as English when every audio track kept is English, so a dub beside the original keeps the subtitles. A language you also typed in by hand keeps all its tracks. With "Tidy tracks even when that's all a file needs" on, files that now carry unwanted tracks are tidied again.
- **The subtitle chips on a show's episodes no longer vanish** when you come back to the
  show within a minute.
- **Every season can be reached with a mouse.** A show's season row scrolled sideways only by swipe or shift + wheel, so seasons past either edge looked missing. An arrow now shows at whichever end has more.
- **Prowlarr link on Unraid** ([#101](https://github.com/tristenlammi/Arrmada/issues/101)): the Prowlarr panel and the Unraid guide now explain the two port settings that have to match.

## [0.4.0] - 2026-10-31

### Highlights

- **Shows with `S01`-style season folders, or kept inside a category folder, work fully
  again** (#56). New episodes go into the folders the show already has, and files v0.3.0
  and v0.3.1 put in the wrong place are listed on the show's page to put back.
- **"Released" now waits for the digital or disc release**, not the cinema date, when
  almost every release is a cam. Films show their cinema, digital and physical release
  dates, and the Calendar lists the digital and disc releases (#93).
- **Untagged audio tracks are identified** (#90). Where the local AI (whisper) is set up,
  Arrmada listens to tracks tagged `und` or nothing, so a Must audio language can judge them.
- **A global seed goal** (#78) for indexers set to use it, never applied to anything
  you already have.
- **The disk guard works per drive** (#70). With two download clients on different drives,
  only the full drive's downloads pause. It can also measure a different drive, for Unraid
  cache pools and ZFS.
- **Manual import suggests the downloads that match**, and can browse or search any
  downloads or library folder you gave Arrmada.
- **Imports wait for qBittorrent's move** (#94). A season pack is no longer blocklisted and
  grabbed again while qBittorrent moves it out of its incomplete folder.
- **Two shows with the same name no longer get each other's episodes** (#81), and episodes
  named like `1x8` are read.
- **A show's search fills from releases you don't avoid first**, so a starved
  complete-series pack no longer beats good season packs.
- **Arrmada never throws away a video it doesn't track.** Convert and imports keep it, and
  a film's page shows **+1 other file** when its folder holds a video Arrmada doesn't use.
- **Music (Preview) is rebuilt**: editions, a matcher that scores downloads like beets,
  single-file albums split by their cue sheet, clean tags and naming, new releases followed,
  and a fast library scan.
- **Books**: KEPUB downloads for Kobo, Calibre libraries scanned from `metadata.opf`, and a
  book's details editable by hand.
- **SABnzbd and Prowlarr on Unraid**, built into the Unraid container and off until you
  switch them on. There's also a guide to downloading through a VPN with Gluetun (#92).
- **A round of security fixes** from a full audit, covering sign-in, saved passwords and keys,
  the bundled download clients and file handling, and Arrmada is built with Go 1.26.10.
  Some of the fixes change behaviour, so read **Before you update**.

### Before you update

- **"Released" now waits for the digital or disc release** (the cinema date + 90 days when
  neither is known). It's the default for new films and requests, and every film already
  on Released waits too: the scheduled search, RSS, search on add and a request approval's
  search wait for that day. **Search** on a film doesn't. To keep grabbing at cinema time,
  set the film's minimum availability to **In cinemas**. See Changed below.
- **A release that doesn't say which of two same-named shows it's for is no longer grabbed
  automatically** (#81). This only matters for a show that shares its title with another in
  your library (Scrubs 2001 and 2026). A year-less release that could be either is skipped
  by searches, and a grabbed one waits in **Needs you** as **Which show?** instead of
  importing. See Fixed below. Separately, a library scan no longer guesses which show a
  year-less folder is when more than one show has its exact title: it goes to review.
- **A rescan no longer replaces an episode's file with one that isn't named as the show**,
  and the duplicate check no longer offers the show's own copy for deletion beside it.
  Episodes an earlier rescan already switched to another file aren't changed by the
  update: check them and import the right file by hand. See Fixed below.
- **Untagged audio tracks are listened to in the background** (#90). It's on by default
  wherever a whisper model is installed, and uses the GPU or CPU whisper uses, one file at a
  time and never alongside a subtitle. Turn it off under Settings → Subtitles → **Local AI
  (whisper)** → **Identify untagged audio**. Without whisper it does nothing.
- **Imports wait for qBittorrent to finish moving a torrent** (#94). With qBittorrent's
  incomplete-torrents folder on, a finished download imports once it's in its final folder.
  A download folder Arrmada can't read is held and shown in **Needs you**, never
  blocklisted or deleted.
- **An import never replaces a file no record owns.** It lands beside it with the version's
  label (`Heat (1995) - 1080p BluRay - Default.mkv`), and a conversion whose new name is
  taken waits under Convert's Problems until you rename or remove that file. See Fixed below.
- **Refresh & rescan keeps a film's file while it's on disk.** It used to switch a film to
  the biggest video in its folder whenever that was a different file, even an extra
  version's file, a trailer or another film's, so two records could hold one file and
  removing the version recycled the film's file. Now:
  - While the recorded file is on disk (or can't be checked, as when a network share is
    down), Refresh leaves the film's file as it is. A bigger copy beside it shows as
    **+1 other file** on the film's page instead.
  - To switch a film to another file in its folder, remove or move the old file, then
    Refresh: the biggest video left in the folder (or two folders down) becomes the film's
    file. This works only in a folder that is plainly the film's own (named for it, with no
    other film's file in it); anywhere else, import the new file by hand.
  - It never picks a file another version or film records (a hardlink of one included), a
    sample, a trailer, featurette or anything in an extras folder, anything in Arrmada's
    hidden folders, or a file in a subfolder named as another film.
  - A film with several versions whose main file is gone has that record cleared, not
    replaced by one of its versions' files. Its other versions keep their files.
  - A film loose in a Movies folder, or in a folder it shares with other films, whose file
    is gone has the record cleared; nothing else in that folder is taken for it.
  - If the folder can't be read, or holds too many files to look through, nothing changes.
  - A file renamed only in case (`Heat (1995).mkv` to `heat (1995).mkv`) is still found.
  - A recorded file under 50 MB is no longer cleared as if there were no video.
- **What a show's rescan and duplicate check read is narrower** (#56). They no longer read a
  folder inside the show's that holds another show (hidden, nested, or under Other or
  Bonus), Plex extras folders, a file named as another show in your library, a video
  loose in the folder of a show kept in season folders, or a file with no title in its name
  outside the folders the show already has files in. A show with no files yet reads only
  its own `Title (Year)` folder. If a rescan used to attach one of these, attach it with a
  hand import instead.
- **A show whose folder also holds another show's episode waits in Needs you** (#56). An
  episode of another show loose in the show's folder (`Peppa Pig - S01E01.mkv` in
  `Kids`), another show's folder, or a link inside it means the show's folder can't be
  told: downloads wait in Needs you, and **Refresh & rescan**, **Rename**, the duplicate
  check, **Move to folder** and **Map files…** are refused for it (Map files uses the same
  check as the import). Move the other show out, then use **Refresh & rescan**.
- **Move to folder refuses a show folder holding an episode the show doesn't have** (#56).
  If anywhere in the show's folder there's an episode Arrmada hasn't recorded for it whose
  name doesn't say it's this show's (`Other Show - S01E03.mkv` in its `S01` or `Season 1`),
  or one naming another show in your library, the move is refused and the message names the
  file: moving the folder would carry it along. Import it, or move it out, then move the
  show. Themes, backdrops and trailers don't count; a small file or a sample named as an
  episode does.
- **The way out for a show whose files were moved** now needs a folder named with the
  show's year (`Show (2019)`) or one already holding its files; see Fixed below.
- **A Must audio language now has to be in the release name** (#58). A release is grabbed for
  a Must language only when its name says it (ITA, iTALiAN, VFF, TRUEFRENCH…), or when the
  language is the title's original and the release names no language or is MULTi/DL/Dual
  Audio, which always carry the original. A MULTi or Dual Audio release that doesn't name the
  language used to count as "might have it" and was grabbed; it's now skipped, and interactive
  search says why ("Doesn't name Italian audio…"). If your trackers label releases MULTi when
  they carry your language (French trackers often do), turn on **MULTi and Dual Audio
  releases count as having a required language** under the profile's Audio language row.
  It's off on every existing profile. A release that names no language, for a title whose
  original language isn't known, is still not refused. This changes which releases are
  grabbed only: files already in your library are judged by the same rules as before (the
  newly read spellings below count for releases, not for library files).

- **Hong Kong films get an original language.** TMDB codes Cantonese as `cn` (or `yue`),
  which Arrmada didn't know, so those films had none and the Audio language rules skipped
  them. They now read as Chinese: "Original language" rules apply to them, an untagged
  release of one counts as Chinese (so a Must English refuses it, as for any foreign film),
  a profile that requires the original marks such a film whose file has only a dub as not
  fitting, and one that prefers it can upgrade that file, within the per-sweep upgrade
  limit. A file whose audio is tagged or heard as Cantonese now reads as Chinese too, so an
  English Must or Prefer can upgrade it (within the same limit). TMDB's `xx` (no spoken
  language) is treated as no original language.

- **Missing music files are flagged, not downloaded again.** When a track file is gone on two
  checks a day apart, the album says "N track files gone from disk" and isn't searched by
  itself, like movies. Use **Search for missing tracks** on the album page, or Music → Library
  tools for all of them at once (each run takes 25 albums, the ones not searched yet first, and
  asks first). Moved or retagged the files yourself? The check then starts a library scan, which
  links them again at their new place instead of downloading. A folder that looks empty or
  unmounted is skipped and named in a health note; if the music folder itself looks unmounted, or
  too many files vanish at once, nothing is marked at all. An album on a share that's away is never
  re-linked to another copy of it.

- **A music library scan of an empty or unmounted music folder is refused** and changes nothing.
  Untagged folders matched only by their name are placed only as a whole album (or exactly the
  tracks still wanted); anything else goes to Reviews.

- **Music upgrades are off until you turn them on.** The update switches automatic upgrades off
  on every music profile, and new profiles and presets start with them off. Turn them on per
  profile (Quality → Music → Automatically upgrade).

- **Artists with no music profile now use "Lossless, or the best MP3",** created once if you have
  no music profile. They were on "Any quality" before, so MP3-192 and below, OGG and Opus are no
  longer grabbed for them. Pick another profile on the artist to change it.

- **Adding an artist searches its wanted albums straight away** (up to 25, in the background),
  following Settings' "search on add". The add dialog has a "Start searching now" box.

- **Homepage widget: `wanted.total` now includes albums** (released albums still missing) while
  Music is on. New fields `artists`, `albums`, `tracks` and `wanted.albums` are 0 while it's off.

- **Music imports are copies now, not hardlinks,** because Arrmada writes clean tags (Settings →
  Naming & metadata → Music naming & tags → Write house tags, on by default). While a torrent
  seeds, its album takes up space twice. Turn Write house tags off to keep hardlinks.

- **A show's "Monitored" switch is now called "Searching: On / Off".** Library scans add
  shows with searching off (nothing is searched until you turn it on), and so does the Sonarr
  import for shows Sonarr had unmonitored. Picking a "Monitor: …" option now also turns
  searching on.

- **"Stalled" now means half an hour without progress,** in the Queue, the Dashboard tile,
  the Homepage widget's `queue.stalled` and Needs you. A torrent that's only briefly waiting
  for peers counts as downloading. Time a download spends queued or paused in qBittorrent
  no longer counts toward trying another release.

- **Only an admin can connect Plex now.** Changing the Plex server or signing in with Plex
  for the connection is admin-only, like Jellyfin. Managers keep watch monitoring and Test,
  and see read-only Plex and Jellyfin cards.
- **Changing a connection's address needs its key typed again.** For Plex, Jellyfin, Prowlarr,
  indexers and download clients, a saved key, token or password is only sent back to the
  address it was saved for. Change the address and the form asks for the secret again.
  Switching an indexer to another kind also asks for it. If you set `ARRMADA_JELLYFIN_API_KEY`
  in `.env` and haven't connected Jellyfin yet, type the key with the address the first time.
- **Scripts that use a session cookie must send `Content-Type: application/json`.** Writes
  (POST, PUT, PATCH, DELETE) made with a browser session are refused if they come from
  another site, and a request body must be JSON. Personal API keys (`X-Api-Key` or
  `Authorization: Bearer`) are unaffected.
- **Reverse proxies must send `X-Forwarded-For`** (or `Forwarded`). Without it, every
  visitor looks like they're on your home network and they all share one sign-in limit.
  Caddy, Traefik and Nginx Proxy Manager send it already. Plain nginx needs
  `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`. Cloudflare's visitor
  header is now trusted only where Cloudflare can have set it. Cloudflare Tunnel needs
  nothing, and `ARRMADA_TRUST_CF_HEADER=1` or `0` overrides the automatic rule
  ([Who a visitor is](https://github.com/tristenlammi/Arrmada/blob/main/docs/configuration.md#who-a-visitor-is)).
- **First-run setup only works from your home network.** A fresh install that's reachable
  from the internet can no longer be claimed by whoever finds it first. To set up from
  outside, add `ARRMADA_ALLOW_REMOTE_SETUP=1` to `.env`, run `docker compose up -d`, and
  take the line out again afterwards. Installs that already have an admin aren't affected.
- **Staff who sign in with Plex or Jellyfin are asked for their two-factor code.** That
  applies to admins and managers with two-factor on (a trusted device still skips the code),
  and "Require two-factor for admins" now covers these sign-ins too. Requesters are
  unchanged.
- **Setting the audiobook password needs your Arrmada password**, plus your two-factor code
  if that's on. An account that only signs in with Plex or Jellyfin needs to have signed in
  within the last 15 minutes. Changing your Arrmada password, or an admin's "Sign out
  everywhere", now signs listening apps out too. They sign in again with the audiobook
  password.
- **The bundled qBittorrent (Compose installs) is always locked with a password,** even
  when its card in Arrmada was removed or switched off. The one exception: it's left alone
  while another qBittorrent of yours is switched on, and Settings → Status says so. New
  installs no longer let home-network devices in without a password. Its own web page is
  published on the server only (`127.0.0.1`) once its container is recreated. To keep it
  open to your network, add `ARRMADA_QBIT_WEBUI_BIND=0.0.0.0` to `.env`. The update script
  prints a note until you choose
  ([qBittorrent's own page](https://github.com/tristenlammi/Arrmada/blob/main/docs/configuration.md#qbittorrents-own-page)).
- **The bundled SABnzbd's page gets a login** (user `arrmada` and a random password) if it
  had none. Admins see it on SABnzbd's card in Settings → Connections. A login you set
  yourself is never changed.
- **The database and backups are readable by Arrmada's user only.** The data folder
  becomes `0700`, and the database and backups become `0600`, at the next start. A new
  `.env` is `0600`, and the update script tightens an existing one that you own.
- **Archive extraction has limits.** An archive may unpack to at most 1.5 times its size
  plus 1 GiB, and hold at most 20000 entries. One that goes over is skipped and reported,
  and the rest of the download still unpacks.
- **Rolling back to v0.3.3 or earlier needs `./update.sh --rollback --with-db`.** This
  version adds database migrations (0334 to 0351: book edits, the music library, the seed
  goal, films' other files and more), so v0.3.x refuses to start on the upgraded database. `--with-db` puts back the backup from before the update, which loses anything
  recorded since. On Unraid, restore that backup before changing the Repository tag
  ([Updating](https://github.com/tristenlammi/Arrmada/blob/main/docs/updating.md#rolling-back-by-hand)).
- **Subtitle sync-check refuses unknown language codes** (`POST /api/v1/subtitles/sync-check`
  answers 400). It used to accept any tag.
- **Alerts and webhooks name people without their email.** "New request", "Auto-approved
  request" and "Problem reported" now carry the person's Plex or Jellyfin name, or the part
  of their account name before the @. Check any n8n workflow that matched on that field.
- **Deleting a movie with its files now empties its own folder** (#59). Its artwork,
  `.nfo`, subtitles, theme music and similar files go to the recycle bin with the video, and
  the empty folder is removed. This only happens when the folder is clearly the movie's own:
  inside a Movies folder, named with the movie's title and year, not a link and not linked to
  from beside it, holding no other
  video, no file of a type Arrmada doesn't know and no file named for something else, and no
  other film in your library has a file recorded there (even a missing one). Otherwise only
  the movie's files and subtitles go, as before, and the delete dialog says why the folder
  stays. Only the files the dialog listed can go: one that turns up after you opened it stays.
  With the recycle bin off, the artwork and metadata are never deleted; they stay in the
  folder. Deleting several movies at once, deleting through the API without the dialog's
  list, and deleting one version or one file work as before.

### New

- **Untagged audio tracks are identified** (#90). Plenty of releases tag their audio `und`,
  or nothing, so the language badges showed **?** and a profile's **Must** audio language
  couldn't judge the file. Where the local AI (whisper) is installed, Arrmada now listens to
  every such track in your library once, in the background: three 30-second samples from a
  quarter, half and three quarters through, and the language most of them hear wins. When
  the samples don't agree, or whisper isn't sure, the track stays unknown rather than taking
  a guess. Tracks with a real language tag are left alone, and so are commentary tracks
  and tracks tagged `zxx` (no spoken language). A sample that can't be listened to means
  the track is tried again on a later pass.
  - What it hears is kept in Arrmada's database, never written into the file. The badges
    show it as detected (a dashed chip, "English (detected)").
  - **A detected language never makes Arrmada replace a file.** It counts when it's a
    language your profile asks for: a file whose untagged track is heard as your **Must**
    language fits, and its badge is green. A language it only seems to lack stays unknown,
    as before: it's never upgraded, re-grabbed or counted in the save dialog for it, and
    its badge has no colour (as for an untagged track nobody has heard) rather than red or
    orange, nor is it counted among the season's misses.
  - It runs one file at a time, only while no subtitle is being made, at idle priority, and
    newly imported files go first. It never runs whisper alongside a subtitle: when one
    is waiting, it stops after the current sample and finishes the file later. Switch it
    off under Settings → Subtitles → **Local AI (whisper)** → **Identify untagged audio**.
    It's on by default, but does nothing until a whisper model is installed.
  - The AI subtitles' own check of an untagged track (before choosing transcribe or
    translate) now takes the same three-sample vote instead of one sample.
- **A film's cinema, digital and physical release dates** (#93). The movie page and the
  Discover sheet show when a film reached cinemas, digital stores and disc, as Radarr and
  Overseerr do, and the Calendar and your calendar feed list a library film's digital and
  physical releases as entries of their own (**Digital release**, **Physical release**). The
  dates come from TMDB, for your region (Settings → Connections → **Discovery region**).
  Where your region has no date for one of them, the US date is shown, then the earliest
  anywhere, with the country beside it. Films you already have get them in the background
  after you update, or straight away from **Refresh & rescan**. After you change the region,
  they're read again in the background from the next restart (or from **Refresh & rescan**).
  They're only shown: when a film is searched for still goes by its minimum availability and
  its main release date, as before.
- **Other files in a film's folder.** When a film's folder holds a video Arrmada doesn't use
  (another cut, an old copy, a second film left there), the film's page shows **+1 other
  file** by its file, and the Movies library has a **Has other files** filter and a small
  **+1** on the poster. A library scan or **Refresh & rescan** notes them, and the chip's
  list looks again. It only says they're there and not tracked: nothing is moved, renamed,
  recorded or deleted, and there's nothing to do with them from the list.
  - Not counted: the film's own files and any other film's, samples (named so, in a Sample
    folder, or under 50 MB), Plex and Jellyfin extras (names ending `-trailer`,
    `-featurette`, `-behindthescenes`, `-deleted`, `-interview`, `-scene`, `-short`,
    `-other`, and folders such as Extras, Featurettes, Trailers and Deleted Scenes), and
    anything that isn't a video. Subfolders are looked in two levels deep, except one
    named as another film (`Alien (1979)/Aliens (1986)`).
  - A folder that isn't plainly the film's own (not named for it, or another film in your
    library has a file there) is looked at only at its top level, and only files no film
    uses count. A film loose in a Movies folder has nothing listed.
- **Manual import can look anywhere you gave Arrmada, not just the downloads folder.** On a
  movie, show or book, **Manual import** now opens with the finished downloads whose names
  match it (in your download clients, Not managed ones included, or sitting in a downloads
  folder), one click to use. **Choose a folder…** browses your downloads and library folders
  with how many videos (or book files) each folder holds and a filter, searches every
  downloads folder by name, or takes a typed path. It only ever shows folders inside your
  downloads and library folders, never Arrmada's own data folder, and doesn't follow links
  that lead outside them.
- **A global seed goal** (#78). Settings → Downloads → **Seeding** sets a ratio and a
  seeding time for indexers set to **Use the global default**, so you don't have to type the
  same limits on each indexer. It only ever applies going forward, and only where you choose
  it. Every indexer you already have keeps its own goal, and one set to seed forever (both
  at 0) still does: switch an indexer in its Seed rules (**Global default**, **Own goal** or
  **Seed forever**) to use the global goal. An indexer added while the goal includes a
  time, by hand or from Prowlarr, starts on it; otherwise a new one still gets 14 days. A
  ratio-only goal is never given to anything automatically, because it could end a private
  tracker's torrent before its minimum seeding time; choose it for an indexer and it
  applies. Prowlarr rows you already have aren't changed.
  - Torrents grabbed from an indexer on the global default follow it as it is now, so
    raising, lowering or clearing it reaches them. With it cleared, they seed for the
    standard 14 days. Torrents grabbed before this release keep the goal they were grabbed
    with. A torrent you add by hand or upload follows the global goal while it includes a
    time, and gets the standard 14 days otherwise.
  - A torrent is removed at whichever limit it reaches first, so a ratio can end a seed
    before its time is up. For a private tracker's minimum seeding time, set a time goal
    alone. Removal works as an indexer's goal always has: only once Arrmada has imported the
    torrent, with its data, since the library keeps its copy; a torrent you dismissed in
    Review is removed with its files kept.
  - The fields save only when you press **Save**. **Check finished torrents** shows what the
    typed goal would do before you save it. For each finished torrent it lists the ratio and
    seeding time against the goal that applies, whether that goal is the live global default
    or one recorded when it was grabbed, and what the next check will do. It only reads;
    nothing on it removes anything. It pairs older downloads with their grabs the same way
    the next check will, without saving the pairing.
- **Seed cleanup removes less** (#78).
  - At most 10 torrents go per check (every 10 minutes), whatever ends them — a goal, the
    global goal, or seeding switched off for the indexer. The rest wait for the next check,
    and the log says so.
  - A torrent is matched to its grab by its hash only, so a cross-seed or a copy you added
    with the same name is no longer removed with its data. A download grabbed before hashes
    were kept is matched by name only when no other download in the client has that name,
    and one grab never removes two torrents.
  - A season pack whose import left files it couldn't match to an episode keeps seeding
    instead of being removed with those files, whether it was imported automatically, from
    Review or by a manual folder import.
  - An indexer edit checks its seed values only when they change, so an old goal over the
    new five-year limit doesn't block other edits.
- **A Missing episodes filter on Series** (#82): shows with aired, monitored episodes that
  have no file, whether searching is on or off.
- **Put misplaced files back** (#56). Episode files that v0.3.0 and v0.3.1 imported into a
  folder named like a season right inside the TV folder (`<TV folder>/S13/Season 12/…`) are
  listed on their show's page, with a preview of where each goes. Nothing is ever replaced:
  where the show's folder already has the episode, the misplaced copy goes to the recycle
  bin only when it's identical or lower quality, and otherwise both are left for you.
  Emptied folders are removed, and the show's History records what moved. Settings →
  Status lists every show affected.
- **Built-in SABnzbd and Prowlarr in the Unraid container**, the same ones the Compose
  install adds with `--with-sabnzbd` and `--with-prowlarr`. Both are off unless you set
  **Built-in SABnzbd** or **Built-in Prowlarr** to `true` under Show more settings (a blank
  field counts as off). SABnzbd is connected to Arrmada by itself, with Arrmada's categories,
  and saves to `usenet/` in your Downloads folder so imports stay on the same drive; you add
  your Usenet provider on its page (port 8085). Switch it off again and its client in
  Settings → Connections is switched off with it. Prowlarr's page is on port 9697: add your
  indexers there, then its API key in Arrmada; it gets the built-in FlareSolverr like the
  Compose install's Prowlarr. Their settings live in Arrmada's appdata. The container image
  is about 220 MB larger on disk (about 85 MB more to download) whether or not you use them.
  Already installed from the template? Unraid keeps your old template, so add the switch and
  port lines yourself: [Arrmada on Unraid](https://github.com/tristenlammi/Arrmada/blob/main/docs/unraid.md#updating) lists them. The docs no
  longer suggest SABnzbd or Prowlarr were inside before. Thanks to @aleonenko-us, whose PR #66
  started this.
- **KEPUB for Kobo.** EPUBs can be downloaded as KEPUB from My shelf and the book page
  ("Download for Kobo (KEPUB)"), and the OPDS catalogue offers a KEPUB link beside each
  EPUB. The KEPUB is made on download from a copy (the library file is never changed) and
  cached. If the conversion fails, you get the EPUB. A switch in Settings → Book server turns it off. The e-reader panel gains a
  "Kobo (stock reader)" row.
- **Calibre folder scan.** The library scan reads Calibre's `metadata.opf` to tell which
  book a folder holds: its ISBN first, then its title and authors. Series and publication
  date fill gaps the catalogue leaves, and `cover.jpg` is used only when the catalogue has
  no cover. Calibre's " (123)" is taken off a folder name only when the folder really is
  Calibre's, so your own "Saga (2)" keeps its number. Nothing is written to the Calibre
  folder.
- **Edit a book's details** (book page → Fix → Edit details…): title, subtitle, author(s),
  series and position, description, year, language, tags, ISBN, narrator(s) and publisher.
  Each edited detail is marked, shows the catalogue's value, and has Reset to matched (or
  Reset all). Refreshes, the Hardcover upgrade and merges never undo an edit. Change match
  asks whether to keep your edits. Edits show everywhere: Books, ⌘K search, series,
  requests, listening apps and OPDS. Files are never renamed by an edit. When an edit would
  change the files' names, the page offers Preview rename for that book.
- **The disk guard can measure a different drive.** Settings → Downloads → Download disk
  guard → "Measure a different drive". On Unraid, the downloads share reports the whole
  array while torrents fill the cache pool. Map the pool read-only (for example `/cache`)
  and the guard watches that. A new "Also pause when free space drops below (GB)" limit
  helps on ZFS pools, where the percentage counts only the root dataset. A drive that can't
  be read, or that turns out to be a RAM folder (a mistyped Unraid pool name), is a clear
  warning, and nothing is paused or released on its account
  ([Download disk guard](https://github.com/tristenlammi/Arrmada/blob/main/docs/configuration.md#download-disk-guard)).
- **Staff choose a quality profile when requesting** (#50). On a Discover movie or show,
  admins and managers get a split Request button ("Request · HD 1080p") with the profiles
  for that kind of media; for a show it's on the season picker's confirm button. Leave it
  alone and the default is used. Requesters see no change, and a profile a requester sends
  through the API is ignored (staff still choose when approving).
- **Order the library your way** (#36). Movies and TV list views get sortable Year, Added and
  Rating columns (click a header, click again to reverse), the Sort menu and the headers share
  one remembered order, and a new ↑/↓ button reverses it in grid view and on phones. The Books
  table sorts from its headers too, with a new Added column.
- **Browse one library folder** (#34). With several Movies or TV folders, the library page gets
  a Folder picker (`?folder=` in the address, so a filtered view can be bookmarked). Search,
  counts and Select all stay inside the chosen folder, and Add from a filtered view puts the
  new title in that folder.
- **Descriptions in your language** (#35). With Settings → Library → "Show titles as" set to
  **My language**, movie and show descriptions on the title pages and in Discover come in that
  language when TMDB has a translation, and in English otherwise. English and Original modes
  keep English descriptions; season and episode descriptions stay English. Existing libraries
  catch up in the background, about one title every two seconds.
- **Rename fills in what the file name lost** (#48). The Rename preview for movies and episodes
  now takes quality, source, codec and release group from the release Arrmada recorded, so a
  naming scheme that gains `{codec}` or `{group}` works for files named by an older, shorter
  scheme. Safeguards: a release that contradicts the file name, or belongs to a file replaced
  outside Arrmada, is ignored; the codec the file actually has wins (Convert); editions never
  come from the release; renames after a TV renumber still use the file name only. Nothing is
  renamed until you apply the preview.
- **A Stalled filter on Activity, and which client each download is in.** The Queue gets a
  Stalled pill (no data for half an hour while trying to download, the same rule Arrmada
  uses before trying another release) with a "Try another release" button, and the
  Dashboard, Homepage widget and Needs you now count stalled the same way. With more than one
  download client, each row says which client it's in, with a client filter.
- **"Searching: On / Off" for shows** (#52, #53). The show-level switch that decides whether
  Arrmada searches for a show now says so, and "Monitor" only means which episodes you want.
  Wanted episodes of a show with searching off say "Searching off" instead of "Unmonitored",
  with a banner to turn it on. Picking a Monitor option turns searching on and the menu
  remembers it (or shows Custom). The Series list counts Searching on / off, and turning
  searching on for many shows at once is searched a few at a time, like an import.
- **SECURITY.md**: how to report a vulnerability privately, and which versions get fixes.
  The README now explains how Arrmada is built and tested.

- **Music (Preview) on the Dashboard and Calendar.** With Music on, staff get an Albums tile and a
  Music card (library, missing and upcoming albums, new releases, recent imports), album release
  dates on the Calendar and in their personal calendar feed, and music counts in the Homepage
  widget. Requesters don't see albums.
- **Music (Preview) keeps up with new releases.** Every few hours Arrmada checks your artists
  (through the cache and MusicBrainz's polite limit); a genuinely new album from an artist set to
  follow new releases is wanted and searched once, and listed under New releases on the Music page.
  Release types work properly ("Album + Live" means live albums), singles already on an album
  aren't wanted, and "Latest" never picks an unreleased album. Per-artist Options on the artist
  page, a library default in Settings. Nothing old is ever wanted by surprise, changing settings
  never wants anything by itself ("Want matching albums…" shows a count first), and albums
  MusicBrainz removes are kept, files and all, but not searched automatically.
- **Music (Preview) splits single-file albums.** A rip that's one big FLAC, WAV or WavPack image
  plus a `.cue` sheet is matched first, then cut into one FLAC per track at the exact sample
  positions (gaps stay where CD players put them), proven sample-exact against the image, and
  imported like any other album. It streams, so even a 24/192 image needs well under 100 MB of
  memory. A cue that doesn't fit the image, or the album's length on MusicBrainz, goes to Reviews.
- **Music (Preview) writes proper tags and follows your naming.** Each import is copied into a
  hidden staging folder, given clean tags (every artist, MusicBrainz IDs, compilation flag,
  embedded cover plus `cover.jpg`), checked so the audio is unchanged, then moved into place in one
  step; your downloads are never written to, and a crash at any point recovers on the next start.
  Settings → Naming & metadata has music naming templates with a live preview, and albums get
  Rename files… (preview first) and Write house tags. Artwork, booklets, cue sheets, logs and
  lyrics move with the album.
- **Music (Preview) matches downloads like a person would.** Every edition of an album is known,
  with the earliest official release picked by default and a Change edition… picker. Downloads are
  scored against each edition (the beets method), partial albums import what fits, and close calls
  go to Reviews with the reason in words ("length off by 45 s") and an Import as <edition> option.
  Thresholds are in Settings → Naming & metadata → Music matching. Badly tagged files can be
  identified by sound with your own free AcoustID key (Settings → Connections).
- **Music (Preview) has a new library model:** albums have editions, discs and credited artists,
  and each track file keeps its measured quality. Your existing music moves over on update with
  nothing lost; anything that can't map one-to-one (two tracks claiming one file) shows as a note on
  the album page with a Resolve button, and that album isn't searched automatically until then.
- **Music (Preview) is safer while it's rebuilt.** Artist and album names fold properly
  (Björk, AC/DC, R.E.M., "The The") without matching the wrong band ("Fall Out Boy" is no
  longer The Fall); words like "Escape" no longer read as lossless; quality is measured from
  the files; track and disc numbers are read safely; an upgrade never lowers quality and old
  files only ever go to the recycle bin; files that match no track are left in the download
  instead of being cleaned up; albums show as complete again. MusicBrainz lookups share one
  polite limit and a permanent local cache, so music keeps working when MusicBrainz is down, and
  Settings → Connections → Metadata can point at your own MusicBrainz copy.
- **Music (Preview) searches and upgrades on its own terms.** Album and artist pages say why an
  album isn't downloading (last outcome, next try) and have **Search now**; an artist's Search now
  takes up to 25 albums a click, never-searched first, and a second click moves on to the rest.
  Interactive search lists every release with the reason it would or wouldn't be grabbed, and a
  manual grab imports into the album you picked (the same release twice is refused while it's
  still downloading). Video and implausibly sized releases are dropped. With upgrades turned on
  for a profile, a paced sweep replaces albums with strictly better quality, never a downgrade,
  old files to the recycle bin.
- **Music (Preview) scans big libraries fast and keeps an eye on your files.** The library scan
  reads tags (files with MusicBrainz IDs match straight away), runs in the background, and a
  rescan only opens folders that changed. It never moves, retags or deletes anything; folders it
  can't place go to Reviews with **Match…**. Albums moved or retagged outside Arrmada (Picard,
  beets) are linked again at their new place. A daily "Check music files" task notices files that
  vanished or changed outside Arrmada and shows them as gone from disk; it never deletes anything
  and never downloads them again by itself. Artists are found under their other names too
  ("Beyonce", "宇多田ヒカル").
- **Actors on a library page open their Discover page** (#55). Click or tap someone in a
  movie's or show's Cast row to see the other movies and shows they're in; Close or Back
  returns to the library page. Titles added before this update link their actors after
  their next metadata refresh (**Refresh & rescan** on the page; monitored shows also
  refresh on their own schedule).
- **Add the year to a show folder only when it's needed** (Settings → Naming → Series naming),
  the way Sonarr's TheTVDB names look: "Breaking Bad", but "Dark Matter (2024)" because another
  Dark Matter exists. The year is kept when another show in the library has the same title, a
  folder with that title is already in a TV folder, or TMDB lists another show with that title
  (titles compare ignoring case, punctuation and a leading "The"). If a TV folder or TMDB can't
  be checked, the year is kept. It only names new show folders, once per download, so a
  download never splits across two folders. A show that already has a folder keeps it, and
  Rename names files inside that folder (it only gathers a show that's split over two folders
  into the one it uses). It's off by default, so nothing changes until you turn it on.
- **A rename never gathers a show into another show's folder.** A move into a folder where
  another show keeps its files is refused and shown as a conflict in the preview. Season
  folders named "S01", "Staffel 1" or "Saison 1" are recognised as part of the show.
- **A guide to downloading through a VPN** (#92,
  [docs/vpn.md](https://github.com/tristenlammi/Arrmada/blob/main/docs/vpn.md)): qBittorrent
  behind Gluetun with a `docker-compose.override.yml`, keeping Arrmada and FlareSolverr on your
  normal connection, and what to do on the Unraid all-in-one container. Arrmada doesn't test it.

### Changed

- **"Released" now waits for the digital or disc release** (the cinema date + 90 days when
  neither is known); pick **In cinemas** for cinema-time grabs. The earliest such release in
  any country counts, so a film out on US digital isn't held back by a later local date. Before, "Released" counted
  from the cinema release, when almost every release is a cam. The scheduled search, RSS,
  search on add and a request approval's search all wait; **Search** on a film doesn't. The
  film's page says what it waits for ("Waiting for the digital release on 12 Dec"), and
  Wanted, the Calendar ("search starts"), Discover and request cards show the same day;
  approving one says "searches from 12 Dec". A cinema re-run or a new 4K disc never holds
  an older film back, and the dates of films still waiting are read again daily, so a
  digital release TMDB learns of later counts.
- **A library scan no longer guesses which show a year-less folder is** when more than one
  show has exactly its title ("Dark Matter": 2015 and 2024). The folder goes to review with the
  candidates, so you pick. A folder with a year, or a title only one show has, matches as
  before.
- **Built with Go 1.26.10**, because Go 1.25 is out of support. The Docker build pins Go to
  the patch release. Building from source outside Docker needs Go 1.26.
- **Vulnerability scanning in CI.** Every change runs govulncheck (it fails when our code
  can reach a known-vulnerable function) and `npm audit` (high or critical, in the UI's
  production dependencies). CodeQL code scanning runs on Go and TypeScript. Go modules with
  known advisories were upgraded (`golang.org/x/net`, `x/crypto`, `x/sys`,
  `golang-jwt/jwt`).
- **Sign-in limits are shared.** The audiobook server and the OPDS catalogue count wrong
  passwords with the main sign-in limiter (10 per address in 15 minutes, then a pause per
  username from outside your network). Your own devices that signed in recently aren't held
  up, and an e-reader repeating the same stale password counts once.
- **Web Push** only sends to the browsers' own push services, and an account keeps at most
  10 devices (the oldest goes).
- The image's Apprise is pinned and installed with hashes.
- Release notes on GitHub now come from this changelog, with a Before you update section
  for each version (#31).

### Fixed

- **Two shows with the same name no longer get each other's episodes** (#81). With Scrubs
  (2001) and the 2026 Scrubs both in the library, a release named `Scrubs.S02E03…` doesn't
  say which show it's from. A search for one show grabbed it, and the import trusted the
  grab, so the revival's S02E03 could replace the original's. This only changes anything
  for a show that shares its name with another show in your library. Releases that name
  the year (`Scrubs.2026.S02E03…`) or the country (`The.Office.US…`) work as before. For a
  release that names neither:
  - Searches use the release's publish date. A release posted before a show's season
    began can't be from that season, so `Scrubs.S02E03` posted in 2015 is grabbed for the 2001 show
    and never for the revival. A release for a season the other show doesn't list yet
    only counts as the older show's if it was posted before the other show first aired.
  - When a release could belong to either show, or the indexer gave no date, the
    automatic searches, upgrades and quick **Grab** buttons skip it. The show's last
    search says so, and **Choose release** still lists it with the reason, so you can
    grab it by hand. In practice, the newer show only grabs releases that name its year.
  - A grabbed download that could belong to either show goes to **Needs you** as **Which
    show?**, naming both shows. It isn't imported or used to replace a file. **Import
    anyway** puts it in the show it was grabbed for; **Choose different show…** puts it in
    the other one. Either way, it only replaces a file it's better than; when it isn't
    better, Needs you says **Kept the existing file** and leaves the download as it is.
  - A release you pick by hand from **Choose release** imports as before.

- **Library episodes named "1x8" are read** (reported in #81). A show whose files are
  named with a single-digit episode — "11.22.63 - 1x8- The Day in Question [HDTVMux1080p
  Ita 5.1].mkv" in an "S01" folder — had none of them read: the "1x08" form needed a
  two-digit episode so an aspect ratio ("16x9") or a dimension ("4x4") is never taken for
  one, and **Refresh & rescan** found 0 of 8 episodes. The library scan, a rescan and the
  duplicate check now read a single-digit "NxE" in a file already in a show's folder when
  it can't be anything else: after the show's title and set off by dashes ("Title - 1x8 -
  Episode", "Title - 1x8- Episode", "Title – 1x8"), or after the title inside the season
  folder of the same season ("S01", "Season 1", "Stagione 1"…). Never inside brackets or
  after the release tags; never at the start of the name, where it is usually the show's
  own title ("3x3 Eyes 05"); never "0x5" outside a Specials or Season 0 folder; never
  against a season folder saying another season; never two candidates ("Kids 2x2 - 2x5");
  never "1x8x9" or "1x8-9", whose other episodes can't be told; and "16x9", "4x3", "4x4",
  "2x4" or the same number twice ("3x3", "1x1") only when both dashed and in that season's
  folder. A file read this way only fills an episode nothing else holds: it never replaces
  an episode's recorded file or another copy read by its name, and stays on disk listed as
  a duplicate instead. Release names (searches, grabs, download imports) and **Manual
  import** read exactly as before.
- **A rescan no longer swaps an episode's file for one that isn't named as the show.**
  Another show's episode dropped in a show's season folder (`Beta - 1x08 - Other.mkv` in
  `Show (2010)/Season 1`), or a file with no title in its name (`S01E08.mkv`), could take
  the place of the episode's recorded file on **Refresh & rescan** when it was the bigger
  or sharper copy, and the duplicate check then offered the show's real file for deletion.
  Now:
  - A file whose name isn't the show's never replaces the file an episode has. It still
    fills an episode with no file in the show's own `Title (Year)/Season N` folder, as
    before, so episodes named in another language or in romaji keep being found; when an
    import later brings the episode's file, the filled one goes to the recycle bin like any
    replaced file. When several files could fill it, the one named as the show is taken
    first, then one with no title, then one named otherwise, and only then the better copy.
  - A copy of the show's own episode whose name doesn't match the show (a romaji title the
    show has no alias for, a rip with no title) is no longer swapped in on rescan for being
    the better copy, and isn't listed as a duplicate either. Import it by hand, or add the
    name under the show's **Alternate titles** and rescan.
  - The duplicate check never offers the show's own copy for deletion beside a file named
    otherwise, and never offers a file named otherwise. It never offers a two-episode file
    while it is the other episode's only copy: when that episode has no file recorded, or
    its recorded file is gone, deleting it would have removed that episode too (this
    happened in v0.3.3 as well).
  - A file recorded under a path that differs only in letter case is still the episode's
    recorded file on a disk that ignores case.
  - Episodes an earlier rescan already switched to another show's file are left as they
    are: check the duplicate list and the episode's file, and import the right one by hand.
- **A season pack is no longer blocklisted and grabbed again while qBittorrent moves it**
  (reported in #94 by cpetzel). With qBittorrent's **Default Download Path (incomplete
  torrents)** on, a finished torrent is moved from that folder to its save folder, and
  until the move is done its path still names the incomplete folder, which Arrmada usually
  doesn't mount. A TV import that ran during the move found no folder, took that for a
  release with no video, blocklisted it and failed its grab, and the next search grabbed
  another copy of the same season. Now:
  - Imports (films, TV, books and music) wait while qBittorrent is moving a torrent, or
    says its files are missing, and pick it up from its final folder once it's there.
    Import suggestions, unmanaged downloads and **Check paths** wait for the move too.
  - A TV download whose folder Arrmada can't read is tried again every sweep. It is never
    blocklisted, and its grab stays as it was. If it's still unreadable after five
    minutes it shows once in **Needs you** with the folder and the error. A season pack
    that was already imported and is only being re-read for the season's gaps is just
    skipped. Only a folder that was read and holds no video is still treated as junk.
  - A download with a folder inside it that Arrmada can't open (the wrong owner, say) is
    held, and shown in **Needs you**, rather than partly imported. It is never judged to
    hold no video, so it is never blocklisted or deleted for it. The folder it couldn't
    open may hold the episodes, or the film itself beside a featurette that would have
    been taken in its place. A file that disappears while the folder is read (the
    download being moved, rechecked or renamed) means the folder is changing: nothing is
    decided, and it's read again on the next sweep.
  - Importing such a download from **Review** says Arrmada can't read the folder, instead of
    "no video files were found". Choosing files by hand for a show lists what could be
    read and says part of the folder couldn't be.
  - A torrent qBittorrent has been moving for over two hours shows in **Needs you** as a
    move that's taking too long, since it won't import until the move finishes. It isn't
    counted as a stalled download and doesn't send a "stalled" alert.

  A release already blocklisted this way stays blocklisted. Remove it from the show's
  blocklist, and if its torrent is still in qBittorrent the next import picks it up.
- **Convert and imports no longer remove a video Arrmada doesn't track.**
  - Converting `Heat (1995).mp4` writes `Heat (1995).mkv`. A file you kept under that name
    (a remux beside the film, say) used to be moved into the originals hold with the
    original, and deleted with it when the hold expired. Now the conversion waits under
    Convert's Problems until you rename or remove that file, and nothing is encoded until
    then. An empty file there is still moved aside, as before.
  - A film import whose name was taken by a file no record owns used to send that file to
    the recycle bin, or overwrite it with the bin off. Now the file stays, and the import
    lands beside it with the version's label (`Heat (1995) - 1080p BluRay - Default.mkv`).
    An upgrade still replaces the film's own file under the same name, through the bin,
    and re-importing the same file still does nothing.
  - A file earlier versions moved into the originals hold beside a converted film is no
    longer deleted when the hold is released. It goes back to the film's folder. If its
    name there is taken (usually by the converted file), it goes back as
    `<name> (kept by Convert).mkv`, and Convert's log says so.
  - A revert that fails part-way and can't put its files back keeps both copies, and
    Arrmada tries again within the hour. Releasing the hold in the meantime used to delete
    both copies.
  - An import that records a file already on record under another spelling (another case
    on a case-insensitive disk, or a symlinked folder) no longer recycles the file it has
    just imported. This applies to films and episodes.
  - On a case-insensitive disk, converting `Heat (1995).MKV` no longer waits forever because
    `Heat (1995).mkv`, which is the same file, seemed taken, and the conversion can be
    reverted.
- **A show's search no longer picks a release you avoid over good season packs.** A
  release under your bitrate floor, or with a format, language or Unwanted item you avoid,
  is meant to be a last resort. For shows it wasn't: the search took the release that
  covered the most, so a starved complete-series pack (14.4 GB for five seasons, under a
  5 Mbps floor) beat good season packs for every season. Now the search fills everything
  it can from releases you don't avoid first — complete packs, season packs, then single
  episodes — and only turns to an avoided release for what's still missing. If that is
  the only release that covers a gap it's still grabbed. A torrent with no seeders is
  likewise only used for what nothing seeded covers. An oversized pack is still the last
  resort after all of that, so one missing episode is never filled with a whole series
  when a single episode exists, avoided or not. An avoided pack that would mostly bring
  episodes a better release is downloading right now waits until that download finishes,
  so the better files land first; then it fills the rest. The import normally keeps the
  better files over its copies. A download that stops moving, or is still going a day
  after it was grabbed, holds nothing back. This applies to the missing sweep, the Search
  and Grab buttons and the request preview; a Grab that leaves a pack waiting says so.
- **A request's release preview searches the seasons asked for by number**, as the
  missing sweep does: up to six, plus the broad search. It searched only the first, so a
  show whose season packs only turn up when asked for by season was previewed — and, on
  **Approve & grab**, grabbed — with a worse pack instead. The note under it says what was
  found for the other seasons, and which weren't checked.
- **A request's release preview names every season a pack covers.** A complete-series
  pack showed as "Season 1: 1 pack". It now reads "Seasons 1–5: 1 pack". When the pick is
  a release you avoid, **Why** says so plainly: that nothing better was found, or that
  better releases were found but none covered everything asked for, and what it falls
  short on.
- **A season pack whose episode titles disagree with the show's episode list waits in Needs
  you instead of landing on the wrong episodes.** A show's episode list can be out of date
  when a whole season is released at once. Moving a file to the episode its title names
  then put it on an episode another file in the same pack was numbered for, and that file
  was skipped as already imported. Two episodes ended up with the wrong file and two with
  none. Now Arrmada first refreshes the show's episode list, and if that settles it the
  pack imports as normal. If it doesn't, nothing from the pack is imported. Needs you shows
  it as **Titles disagree**, names the files that disagree and what each one's number and
  title say, and **Map files…** lets you place them by hand, starting from their
  filenames. Packs whose whole numbering is shifted from the episode list (Parks and
  Recreation's two-part "London"), and a REPACK or PROPER beside the file it replaces,
  still import by title as before.
- **Dismissing a held season pack sticks.** If the season still had missing episodes, the
  pack was checked again, held again and alerted on again.
- **Grabbing an already-imported release again by hand imports it again.** It used to be
  added to the download client and then ignored, because Arrmada had recorded it as
  imported and often blocklisted it for the show. The new grab now clears both, so the
  release imports like any release you pick yourself. The earlier grab no longer counts
  for seed cleanup, so if you dismiss the new grab in Needs you, its files are kept when
  the torrent is removed at its seed goal. Files the earlier import couldn't match still
  keep the torrent seeding.
- **Map files… keeps a download seeding while files are left unmapped.** Files you leave
  out exist only in the download, so the torrent is no longer removed with its data at its
  seed goal.
- **The Activity search shows only what matches** (#71). With the same qBittorrent added
  twice, every torrent was listed once per client under one hash, and a search left
  unrelated cards on screen and added copies each time you retyped. Cards are now told
  apart by client, and Needs you lists such a torrent once.
- **A download never reads 100% before it's finished** (#73). Below 100% the queue, a
  film's page, posters and episode chips show one decimal (99.6%), and request cards round
  down.
- **Built with Go 1.26.10**, which fixes a Go security advisory in certificate checking
  (GO-2026-6809), and with `golang.org/x/net` 0.62.0 (GO-2026-6860).
- **With two download clients, the disk guard only pauses the full drive's downloads**
  (#70). A download placed on a drive of its own client's (its folder as Arrmada sees it,
  through the client's path mappings, or SABnzbd's incomplete folder for a job still
  downloading) is judged by that drive, so a second client on a volume of its own keeps
  downloading while the first one's drive is full, and the other way round. Anything the
  guard can't place is judged by the Downloads folder's drive, as before, and named once in
  the log. Settings → Downloads lists each client's drive with the guard's reading. With one
  download client switched on, nothing changes.
- **Episode thumbnails from TVDB load again** (#76). TVDB can give an episode image as a path
  rather than a full address, and the browser then asked Arrmada for it ("Page not found").
  New and refreshed episodes get TVDB's artwork address, and the update fixes the ones
  already stored.
- **Shows with `S01`-style season folders work fully again** (#56). A show stored as
  `<TV folder>/Show/S12/` (an `S{season00}` season folder template, `Stagione 1`, `Saison 1`…)
  is read as living in `Show`, as v0.3.2 and v0.3.3 couldn't: new episodes go into the
  season folder the show already has for that season, whatever it's called (never a new
  `Season 1` beside a `Stagione 1`; while that folder is unreachable, the download waits
  instead), a new season gets its own folder inside `Show` named by your template, and
  **Refresh & rescan**, **Rename**, the duplicate check and **Move to folder** work again.
  Shows v0.3.2 and v0.3.3 refused for their season folders' names read normally after the
  update, with nothing to do. One whose folder also holds another show — another show's
  folder, a video loose in it that isn't the show's, a link — still waits in Needs you, and
  **Move to folder** refuses it. If you started a show over as v0.3.2's notes describe,
  nothing changes for it.
- **Shows kept inside a category folder stay there** (#56). A show in
  `<TV folder>/Anime/Show/` (or under a TV folder set one level too high) gets its new
  episodes in its own folder, `S01`-style season folders included, where v0.3.1 put them in
  a new `<TV folder>/Show/` beside the category. A show's folder is never a folder above the
  one holding its files (other than the show's folder above its season folders), never a
  folder named like a download or disc unless it's right inside the TV folder, and never a
  folder that also holds another show, tracked or not; when it can't be told, the download
  waits in Needs you.
- **Refresh & rescan, the duplicate check and the library scan read only the show's own
  files** (#56). They read exactly the same files: those right in the show's folder, in its
  season folders (and disc or download folders inside them), or in its own download
  folders. They never read a folder inside it that holds another show, whether hidden,
  nested deep, or under a folder called Other or Bonus. In a show folder right in a TV
  folder that's provably the show's own, an episode right in it or in a `Season N` folder is
  the show's however it's named (`Shingeki no Kyojin`, `Frieren`,
  `Top Gear 2023 S33E01`) unless its name says it's another show in your library
  (`Bluey - S01E02.mkv`, or `The Office (2001) - …` beside the 2005 show) — that one is never
  claimed, nor offered as a duplicate to delete. Elsewhere (`S01`-style season folders, a
  show inside a category) a file's name must fit the show. The duplicate check offers for deletion
  only files the show has recorded or whose name says they're the show's. A file whose
  name has no title
  (`S01E01.mkv`, `01.mkv`) counts only in a folder where the show already has files. A
  video loose in the folder of a show kept in season folders isn't taken for an episode.
  Featurettes in Plex extras folders (Extras, Featurettes, Behind The Scenes, Deleted Scenes,
  Interviews, Scenes, Shorts, Trailers, Other) are never taken for episodes. Files the show
  already has always count. A show with no files yet reads only its own `Title (Year)`
  folder right in a TV folder, and only when that's provably its own — never a year-less
  `Title`, which may be a namesake's. Adding a show with **Scan library** marks only the
  files a rescan would read. A theme song, trailer or sample loose in a show's folder, and a
  media server's `backdrops` or `theme-music` folder, are ignored.
- **A link inside a show's folder keeps it from being taken for the show's alone** (#56). A
  link to another folder or to a video means the download waits in Needs you, and Move to
  folder won't move the folder.
- **A show whose folder can't be told has a way out** (#56). Move its files by hand out of
  the folder no show should own (one named like a season, one other shows are in, the TV
  folder itself) into a folder named with the show's year (`Show (2019)`), then use
  **Refresh & rescan**. Each such file is found by its name in that TV folder, as long as the
  name has the show's title and an episode number, exactly one file has it, and no other
  show has that file. A folder without the year may be a namesake's, so nothing is followed
  into it (unless the show's other files are already there). Nothing is changed unless the
  show then reads as living in one sound folder holding every file found; otherwise the
  rescan is refused and the show's records stay exactly as they were. A file gone from the
  show's own folder is never looked for elsewhere (it's deleted, or on a share that's
  away), and nothing is followed while the TV folder looks unmounted. Deleting the show in
  Arrmada keeping its files and using **Scan library** works too.
- Movies: deleting a movie left its artwork and `.nfo` files behind, so its folder stayed
  (#59). The delete dialog now lists every file that goes and whether the folder is removed.
  If the recycle bin won't take one of those extra files, it stays and the delete still
  finishes.
- Movies: deleting a movie or its file no longer removes a folder that is a link to another
  folder. Before, the link itself could be removed, however much was in the folder it
  pointed to.
- Movies: deleting a movie no longer takes a subtitle named for another film in the same
  folder whose video is missing ("Movie.Directors.Cut.en.srt" beside "Movie.mkv").
- Movies: deleting a movie on a large library over a network share no longer takes tens of
  seconds, which could make the delete dialog time out behind a reverse proxy.
- Movies: **Move to folder** could take a whole folder that wasn't the film's own. A film
  sitting loose in a folder such as `Movies/Marvel` took everything in `Marvel` with it,
  including films Arrmada doesn't track and the folder's artwork. Onto another drive, those
  originals then went to the recycle bin. A film's folder now moves only when it's clearly
  the film's own: named for the film, not reached through a link, with nothing in it but the
  film's files, its subtitles, and artwork or `.nfo` files named for it. If the folder holds
  anything else, the move is refused and the dialog says why. Use **Organize** to give the film
  a folder of its own first. The Move dialog now lists every file that will move before you
  press Move, and the move never takes more than that list. TV shows move as before.
- Audio languages: a profile with Italian as Must no longer grabs MULTi or Dual Audio releases
  that don't name Italian (#58). Release names that write FRA, DEU, NLD, ESP, JAP or VFB (in
  capitals, outside an episode title) are read as French, German, Dutch, Spanish, Japanese
  and French.
- Movies: renaming a movie (Rename files or Organize) took the artwork from the folder it
  sat in, even when that folder wasn't the movie's own. A film sitting loose in a franchise
  or collection folder carried the collection's `folder.jpg` and `poster.jpg` off with it.
  Now a movie takes its artwork and `.nfo` files only from a folder that is clearly its
  own. That means a folder named for the movie, holding no other video, with no other
  film's file recorded in it, no links, and only files Arrmada recognises. Files named for
  the old file name get the new name. They go only into a new folder, or one that holds no
  other film: with a naming scheme like "{title}", Heat (1995)'s poster no longer lands in
  the "Heat" folder of Heat (1986). Anything else stays where it is, and the rename
  preview lists every file that moves and says why the others stay. A subtitle that
  belongs to another film's file is no longer taken along. A folder that is a link, or one
  of your library or downloads folders, is never removed.
- Library folders: a show, artist or album whose name is only dots can no longer reach
  outside its library folder. Names like "..." are still allowed.
- Archive extraction: a refused archive no longer stops the rest of the download. Zips of RAR
  volumes and subtitles nested in zips unpack.
- Books: a merge keeps edits honest, and a title edited into a different one ("Dune" →
  "Dune Messiah") no longer picks up the old book's releases.
- Books: the Calibre scan no longer replaces a cover or series set while it was running.
  Symlinked or odd `metadata.opf` files are ignored.
- Books: the Kobo download shows for a Calibre folder that holds an AZW3 and an EPUB.
- Managers no longer get a Jellyfin editor that fails on save.
- CodeQL fixes: the all-in-one image refuses a `PUID`/`PGID` past 32 bits instead of
  running as root, and file names are built only from checked values.

### Security

- The fixes listed under **Before you update**: CSRF protection, client address and
  Cloudflare header, LAN-only first-run setup, two-factor on staff Plex and Jellyfin
  sign-in, the audiobook password step-up, saved secrets kept with their address,
  admin-only Plex connection, locked bundled qBittorrent and SABnzbd, private database and
  backups, and archive limits.
- Cover images: a requester-supplied cover is kept only from a catalogue image host. The
  image cache fetches only public addresses and real raster images (no SVG), and serves
  them sandboxed. Before, a book request's cover could carry a script or reach into your
  network.
- Jellyfin: saving a new server address with the API key left blank sent the saved key to
  the new address. It's refused now, and the form asks for the key again, as the other
  connections do.
- Keys and tokens sent in headers never follow a redirect to another server.
- The Plex token only goes to servers that say they belong to the account, and a manager can
  no longer repoint Plex sign-in at their own server.
- Sign-in checks take turns, so a burst of guesses can't slip past the limit.
- `.env` is `0600` on new installs, and edits keep its mode.
- The release workflow's third-party action is pinned to a commit, and CI's token is
  read-only.

## [0.3.3] - 2026-10-20

### Fixed

- **A season pack whose name gives the season in another language now imports** (#56).
  For a show whose season folders aren't named "Season N" (such as `S01`, `S02`), a pack
  named like `Show.Stagione.2.ITA.1080p…` was refused with "Can't tell which folder…", even
  with Import anyway, because Arrmada couldn't read the season from the name. Now it looks
  at the episode files inside: when every one is named as an episode of the same season
  (`S02E05`, `2x05`), they go into that season's existing folder, next to its other
  episodes. Anything else is still refused, and the message says why, for example
  "its files are from seasons 1 and 2". The automatic import does the same for a download
  grabbed for such a show. Other shows import exactly as in v0.3.2.
- **Map files… is offered when a show's folder can't be told.** If the files inside don't
  say their episode either, the download's card in Activity → Needs you offers
  **Map files…** once the import is refused, so you can say which episode each file is.
  Each file still goes only into its season's existing folder.

## [0.3.2] - 2026-10-20

### Before you update

- **Check your TV folders for episodes in the wrong place (#56).** v0.3.0 and v0.3.1 could
  put new episodes and upgrades of a show whose season folders aren't named "Season N"
  (`S01`, `Stagione 1`, `Saison 1`…) into a folder named like a season right inside the TV
  folder, such as `<TV folder>/S13/Season 12/`, instead of the show's own folder. This
  release stops that. To put files back by hand:
  1. Look directly inside each TV folder for folders named like a season (`S01`, `S13`,
     `Stagione 2`…). The files in them are named after their show.
  2. Move each file into its show's own season folder. If that episode is already there,
     keep the copy you want and delete the other. Delete the season-named folders in the
     TV folder once they're empty.
  3. Arrmada still has the moved files where they were. For each show you moved files
     into, follow **Starting a show over** below, if it applies to that show.

- **Shows whose season folders aren't named "Season N" are handled carefully now.** For a
  show stored like `The Pitt/S01/`, `Gomorra/Stagione 1/` or `Taskmaster/Series 1/`
  (or with your own season folder template's names), Arrmada no longer guesses the show's
  folder:
  - a new episode of a season you already have goes into that season's folder, next to
    the season's other episodes — as long as all of them are in that one folder, it isn't
    right inside the TV folder, and it holds nothing but the show's own files;
  - a new season, or anything unclear, waits in Activity → Needs you;
  - **Refresh & rescan**, **Rename**, the duplicate check and **Move to folder** are
    refused for these shows, and change nothing. **Refresh all** lists them separately.

  You can keep such a show as it is. To have it handled like any other show, start it
  over (below) — but only when its season folders are inside the show's own folder right
  in the TV folder (`<TV folder>/The Pitt/S01/`). When they sit directly inside a category
  or franchise folder (`<TV folder>/Kids/S01/`, `<TV folder>/Star Trek/S01/` next to other
  shows), or the show is inside one (`<TV folder>/Kids/The Pitt/S01/`), don't rename
  anything: keep the show as it is.

- **Shows whose own folder is named like a season** (`Special`, `Series 7 (2001)`,
  `S1 (2020)`), and episodes loose in the TV folder, are refused too: all their new
  episodes wait in Needs you, and the same actions are refused. Renaming their season
  folders doesn't help. Rename the show's own folder so it doesn't read like a
  season (or move loose episodes into a folder named after the show), then start the show
  over (below) without the renaming step.

- **Starting a show over.** For a show whose own folder is right inside a TV folder:
  1. Delete the show in Arrmada, leaving **Also delete files from disk** unticked. Its
     files stay where they are.
  2. Rename its season folders to `Season 01`, `Season 02`… (and `Specials`).
  3. On the Series page, use **Scan library**. The show is added back with its files.

  It comes back with searching off and no quality profile: turn searching on and pick its
  profile again. Its history in Arrmada starts over, and so do its other settings: an
  Anime switch you set by hand, its numbering source, aliases, a display title you chose,
  "monitor new seasons" and per-episode monitoring. New episodes then go into its
  `Season 01`, `Season 02`… folders. The library scan only looks at the folders right
  inside a TV folder, so this doesn't work for a show inside a category folder.

### Fixed

- **TV episodes no longer import into a folder named like a season** (#56). Arrmada never
  takes a folder named like a season (`S01`, `Stagione 1`, `Saison 1`, `Staffel 1`,
  `Temporada 1`, `Series 1`…) for a show's own folder, and never makes one in the TV folder.
  A show stored as `Show/Season 1/` or `Show/` imports exactly where v0.3.1 put it, except
  that an existing `Season.01`-style season folder (a dot, dash or underscore for the
  space) is now joined rather than doubled by a new `Season 1`. Shows whose season folders
  or own folder read like a season, and episodes loose in the TV folder, are handled as
  described under Before you update.
- **An upgrade no longer makes a second folder for a show** whose season folders are named
  `S01` (for example by an `S{season00}` season folder template). This was the other half of
  #56, seen with The Pitt.
- **Arrmada never imports into a folder another show in the library has files in.** It used
  to warn in the log and import anyway. Now the download waits in Activity → Needs you
  saying "Can't tell which folder *the show* lives in".
- **Deleting a show with its files never removes a symlinked folder**, such as a category
  folder on another disk, or a folder above the show's own.

## [0.3.1] - 2026-10-18

### Highlights

- **Two-factor sign-in** (TOTP) for password accounts (#21).
- **Personal API keys** for scripts, n8n and Home Assistant.
- **Display titles**: show films and shows in English, in their original language, or in
  yours (#27).
- **Command palette**: Ctrl K / ⌘K searches titles, pages and settings from anywhere (#17).
- **Book naming templates** with a safe rename for books already in the library (#25).
- **A downloads folder per library folder**, so libraries on separate disks hardlink
  instead of copying (#11).

### Before you update

- **Rolling back to v0.3.0 needs `./update.sh --rollback --with-db`.** This version adds
  database migrations 0329 to 0333, so v0.3.0 refuses to start on the upgraded database.
- **Send to Kindle email moved.** The "Outgoing email" card under Settings → Alerts is now
  "Send to Kindle email" in Settings → Book server (the renamed audiobook server section).
  It only ever sent ebooks to Kindles, never alerts (#10). Old links still land on it.
- **Arrmada no longer creates library folders by itself at startup.** A missing folder may
  be a share that isn't mounted yet. New Compose installs get their default folders from
  the media setup container. On an existing install, use **Create it** on the folder in
  Settings → Library. It refuses a folder that held files, because its share is probably
  not mounted (#24).
- **qBittorrent downloads with their own save path turn off its Automatic Torrent
  Management**, so re-filing a torrent never moves its files.
- **The Unraid template now installs the all-in-one image** (`:stable-aio`, with
  qBittorrent and FlareSolverr built in). Containers installed from the earlier template
  keep running `:stable` until you change the Repository
  ([Unraid → Updating](https://github.com/tristenlammi/Arrmada/blob/main/docs/unraid.md#updating)).

### New

- **Two-factor sign-in (TOTP)** on the Me page: QR code setup, ten recovery codes, and
  "trust this device for 30 days". Admins can reset someone's two-factor and require it for
  admins. The `reset-2fa` command helps a locked-out owner. Repeated wrong codes raise an
  alert (#21).
- **Personal API keys** on the Me page, read-only or full, with optional expiry. A key can
  never do more than its owner's current role. Keys go in `X-Api-Key` or
  `Authorization: Bearer`, never in the address. Admins see and revoke everyone's keys in
  People & access. The guide is in [docs/api.md](https://github.com/tristenlammi/Arrmada/blob/main/docs/api.md).
- **Display titles** (Settings → Library → Display titles): English, original title, or a
  language of your choice, plus original titles for chosen languages. A title can be set
  one at a time from its ⋯ menu. Matching, metadata and file names keep using the canonical
  title. API responses add `display_title` beside `title` (#27).
- **Command palette** (Ctrl K / ⌘K): titles, pages, Settings cards and a few safe actions.
  Requesters get their own pages and Discover (#17).
- **Book naming templates** in Settings → Naming & metadata: audiobook and ebook folder and
  file templates with a live preview, an Audiobookshelf preset, and "Preview book rename…"
  for books already in the library. Listening places and app downloads survive a rename.
  The default naming is exactly the old one (#25).
- **A downloads folder per Movies or TV folder** (Settings → Library). Torrents for that
  folder's titles save there and import as hardlinks (#11).
- **Scan one library folder.** Adding a folder that already holds something offers a scan.
  Each extra folder has its own Scan button and says how much isn't in the library yet (#3).
- **TV searches use translated titles** in the "Also match titles in" languages. A German
  tracker's "Haus des Geldes" is found as Money Heist, but only with evidence beyond the
  name (language, year or country).
- **Homepage dashboard widget** for gethomepage.dev: counts only, with its own key
  (Settings → System → Dashboard widget) (#18).
- **Media folder on the host through `.env`** (`ARRMADA_MEDIA_HOST`), with no override file
  needed (#19).
- **Library scans** get 12 hours instead of minutes, stop cleanly, and have a Stop button
  (#22).
- **GPU guide** ([docs/gpu.md](https://github.com/tristenlammi/Arrmada/blob/main/docs/gpu.md)) for Intel, AMD and NVIDIA. AMD cards now use
  VAAPI automatically (#20).

### Fixed

- Upgrades: the bitrate ceiling no longer stops the search for a file that's still missing
  a Must (for example an H.264 file under "Must HEVC").
- Book renames are journaled and recover after a crash. Audiobook file ids stay stable, so
  Lissen downloads keep working (#25).
- Create it makes the folder that's in use instead of answering "unchanged" (#24).
- The setup wizard no longer warns about managed storage when `/media` is a host folder
  (#19).
- A folder scan of a dead network share says "not responding" instead of hanging the page
  (#3).

## [0.3.0] - 2026-10-17

### Highlights

- **More than one Movies and TV folder**, such as Kids, Animation or Anime, with rules for
  where new titles go and a Move to folder job (#3).
- **Import from Radarr and Sonarr**, files and all, without re-downloading.
- **64-bit ARM** (Raspberry Pi 4/5, ARM NAS boxes) and an **Unraid all-in-one image** with
  qBittorrent and FlareSolverr built in.
- **Audio language rules** in quality profiles, with language badges on files.
- **n8n and webhook alerts** with signed JSON.
- **Release previews**: admins see which release a waiting request would grab.

### Before you update

- **Ordered ranking (v2) is now the default** for choosing and upgrading releases. Upgrades
  follow the same rules as grabs. If you preferred the old behaviour, pick Classic in the
  Ranking switch on the Quality page (Advanced). A saved Classic choice is kept.
- **The bundled qBittorrent (Compose installs) gets a password.** Its own page now asks for
  a login. Admins find it on the qBittorrent card in Settings → Connections (Show its
  login). Torrents and settings are kept
  ([qBittorrent now asks for a password](https://github.com/tristenlammi/Arrmada/blob/main/docs/updating.md#qbittorrent-now-asks-for-a-password)).
- **Indexers, Prowlarr and download clients now see the User-Agent
  `Arrmada/<version>`** instead of Go's default. Cloudflare-protected indexers that blocked
  the old one now work.
- **Rolling back to v0.2.0 needs `./update.sh --rollback --with-db`**, because of database
  migrations 0321 to 0328.

### New

- **Several Movies and TV folders**, each with an optional rule (animation, anime, kids),
  folder pickers when adding or approving, and Move to folder (#3).
- **Radarr / Sonarr import** in Settings → Import & migrate: preview, then import titles,
  profiles, monitoring and existing files where they are. Imported wanted titles are
  searched at a gentle pace.
- **arm64 images** alongside amd64. The installer accepts 64-bit ARM. On ARM, Convert and AI
  subtitles use the CPU.
- **Unraid all-in-one image** (`-aio` tags): Arrmada with qBittorrent and FlareSolverr in
  one container. On arm64 it has qBittorrent only.
- **Audio language** row in quality profiles (Must / Prefer / Avoid per language, or
  "Original language"), judged by the file's real tracks once downloaded. Language badges
  appear on movie files and episodes.
- **Titles in other languages** (Settings → Library): downloads and folders named in
  another language match their title.
- **n8n / Webhook alerts** in Settings → Alerts: signed, versioned JSON, with sample events
  for testing ([docs/n8n.md](https://github.com/tristenlammi/Arrmada/blob/main/docs/n8n.md)).
- **Release previews** for requests waiting for approval, and "Approve & grab this".
- **SABnzbd fetches the NZB itself** when an indexer blocks Arrmada's own download.
- An **Update** label beside the sidebar version when a new release is out.

### Changed

- Needs-you imports (Import anyway, Choose different…, Map files…, Import into…) run as
  jobs with copy progress, instead of timing out behind a proxy (#15).
- Files from a release group the profile avoids (YTS and the like) are replaced under
  every "Replace for" setting, not only "Any improvement".

### Fixed

- AI subtitles: whisper now runs on any x86-64 CPU. v0.2.0's crashed with "Illegal
  instruction" on CPUs without AVX2 (Celeron J4125/N5105, older i5/i7).
- A film released without a year in its name gets a real title, instead of being held as
  "doesn't match".
- An indexer that blocks Arrmada says so, instead of claiming the API key was refused.

## [0.2.0] - 2026-10-15

### Highlights

- **Jellyfin** alongside Plex: Watch buttons, library updates, Insights and Sign in with
  Jellyfin.
- **Usenet** through SABnzbd, optional and alongside qBittorrent.
- **Release channels**: installs follow published releases (`stable`) by default.

### Before you update

- **Updates now follow the stable channel.** `ARRMADA_CHANNEL=stable` (the default)
  downloads the published release. `main` compiles the latest code as before. An install
  without the line gets one on its next update and never moves backwards. An install
  running newer code than the release keeps its build
  ([Stable or main](https://github.com/tristenlammi/Arrmada/blob/main/docs/updating.md#stable-or-main)).
- **Plain `docker compose up -d` runs the published image** (`ARRMADA_APP_IMAGE`, default
  `ghcr.io/tristenlammi/arrmada:stable`) instead of compiling. Existing installs are pinned
  to their own build, so nothing switches silently.
- **Settings → Plex became Settings → Media servers**, covering both servers. Old links
  redirect.
- **Rolling back to v0.1.0 needs `./update.sh --rollback --with-db`**, because of database
  migrations 0310 to 0320.

### New

- **Jellyfin**: connection, library updates with path mappings, watch history in Insights
  (with a server filter), Watch links per server, and Sign in with Jellyfin (password or
  Quick Connect) with account linking.
- **Usenet (SABnzbd)**: add your own SABnzbd in Settings → Connections, or
  `install.sh --with-sabnzbd` for a bundled one. Newznab indexers are searched once a
  usenet client exists, with a Usenet-or-torrents preference. Failed and stalled jobs are
  handled, and `.torrent` and `.nzb` files can be uploaded.
- A version badge at the foot of the staff sidebar.

### Changed

- The image builds in about 2 GB of memory (it needed about 7 GB) and explains when it
  runs out.
- The example compose file works as a Portainer stack.

## [0.1.0] - 2026-10-14

The first tagged release (early development).

### Highlights

- **One app for movies, TV, books and requests.** Movies and TV (anime numbering
  included), ebooks and audiobooks, and music as a preview that's off by default. Searching,
  downloading, importing, renaming and upgrading, driven by quality profiles that describe
  the file you want.
- **Requests**: Discover, Overseerr-style requests with auto-approval, and Overseerr import.
- **Audiobook server** for Lissen and other Audiobookshelf apps, OPDS for e-readers, and
  Send to Kindle.
- **Plex**: library updates, Watch buttons, Plex sign-in, and Insights (Tautulli-style
  watch history and stats).
- **Media tools**: subtitles (embedded, OpenSubtitles or local Whisper AI) and Convert
  (HEVC/AV1 re-encodes checked against the original).
- **Bundled qBittorrent and FlareSolverr**, Torznab indexers and Prowlarr sync.
- **Safety nets**: hardlinked imports, a recycle bin, nightly backups, a blocklist, and
  `update.sh` with automatic rollback.

[Unreleased]: https://github.com/tristenlammi/Arrmada/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/tristenlammi/Arrmada/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/tristenlammi/Arrmada/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/tristenlammi/Arrmada/compare/v0.3.3...v0.4.0
[0.3.3]: https://github.com/tristenlammi/Arrmada/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/tristenlammi/Arrmada/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/tristenlammi/Arrmada/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/tristenlammi/Arrmada/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/tristenlammi/Arrmada/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/tristenlammi/Arrmada/releases/tag/v0.1.0
