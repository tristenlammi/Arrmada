<div align="center">

# ⛵ Arrmada

**One app instead of the whole `*arr` stack.**

</div>

---

Arrmada is a self-hosted app that does the jobs of Radarr, Sonarr, Readarr, Bazarr,
Overseerr, Tautulli and Tdarr in one place, with one database and one web UI. It ships
with qBittorrent and FlareSolverr already wired up.

> **Status:** early and under active development. Expect rough edges.

## Features

**Library and downloads**

- **Movies** — search, grab, import, rename and upgrade. Keep several versions of a movie (say 4K and 1080p).
- **TV** — episodes and season packs, anime numbering (TheXEM and TheTVDB), and airing shows kept up to date.
- **Books** — ebooks and audiobooks with Hardcover or Open Library metadata, series tracking, several audiobook versions per book (standard and full cast), and multi-file audiobooks merged into one M4B.
- **Audiobook server** — listening apps built for Audiobookshelf (Lissen) connect to Arrmada directly. Places sync reliably across devices and restarts, a glitch can't reset anyone to the start, and earlier places can be put back. Everyone gets their own audiobook password, per-device sign-out and a page showing where they're up to and how much they've listened. The admin sees how much and when people listen, never what. Imports everyone's places from Audiobookshelf.
- **Music** (preview, off by default) — artists, albums and whole-discography grabs. Switch it on in Settings → System → Modules.
- **Quality profiles** — a bitrate ceiling, required formats (Atmos, HDR, Dolby Vision), and preferred or rejected words.
- **Indexers** — any Torznab indexer, one-click sync from Prowlarr, and built-in MyAnonaMouse, TorrentLeech and 1337x. FlareSolverr handles Cloudflare-protected trackers.
- **Downloads** — the bundled qBittorrent sets itself up. Imports hardlink instead of copying, seeding rules clean up, stalled downloads fail over, and mismatched downloads wait in a review queue.
- **Safety nets** — a blocklist, a recycle bin, and a full history for every item.

**Requests and people**

- **Discover and requests** — browse and request movies, TV and books, Overseerr-style. Auto-approve per user, and import your existing Overseerr requests.
- **Users** — admin, manager, requester and read-only roles, with optional Plex sign-in. Requesters get Discover, Calendar, Books and Audiobooks; from outside your network, everything but Calendar.
- **Books shelf** — requesters can download any ebook in the library.
- **Calendar** — what's coming up.
- **Notifications** — Apprise alerts for admins; an in-app inbox and web push for requesters.

**Media tools**

- **Subtitles** — pulls embedded subtitles out, fetches them from OpenSubtitles, or writes them with local Whisper AI (GPU-accelerated on Intel).
- **Convert** — switch it on and it works through your library during the hours you choose, re-encoding only wasteful video to HEVC (or AV1, when a quick test shows it's clearly smaller and your devices play it). Every result is checked against the original and must look the same and save at least 20%, or the original stays. Atmos and every audio track are copied untouched, HDR10 and HDR10+ are kept, and Dolby Vision keeps its HDR10 base. Pauses while someone is watching Plex, can use the GPU, and trims audio and subtitle tracks to your languages.
- **Insights** — Plex watch history, stats and buffering diagnostics, Tautulli-style.

## Install

You need Docker with Compose v2. On Unraid, the Compose Manager plugin provides it.

```sh
git clone https://github.com/tristenlammi/Arrmada
cd Arrmada
./install.sh
```

The installer asks three things: the folder that holds your media and downloads, where to
transcode, and your timezone (it suggests the one your machine uses; find yours in the "TZ
identifier" column of [this list](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones)).
It works out the rest itself — free ports, the user to run as, where the database lives and
your GPU. The first build compiles everything, so it takes a while.
It waits until Arrmada is ready and prints the address.

Open that address and create your admin account. A short setup then asks for your free
[TMDB key](https://www.themoviedb.org/settings/api) and lets you pick each library folder.
You can skip it and do both later in Settings.

`./install.sh --with-prowlarr` also starts Prowlarr, if you'd rather manage indexers there.

## Update

```sh
./update.sh
```

This pulls the latest code, rebuilds only the app, and waits until it's running again. Your
`.env`, downloads and media files aren't touched, and the companions keep running. The
database is kept too, but a new version may upgrade its tables when it starts, so a
snapshot is taken automatically before any schema change (`<data>/backups`, newest 5 kept),
and another every night after 04:00 local time (newest 7 kept). If the data disk is too full
for the pre-upgrade copy, Arrmada refuses to upgrade and says so; free some space, or set
`ARRMADA_SKIP_MIGRATION_SNAPSHOT=1` in `.env` to upgrade without one.

Before it rebuilds, the script also backs the database up with the running app's own
`arrmada backup --kind pre-update` (`<data>/backups/arrmada-pre-update-*.db`, newest 3 kept),
and stops if that fails; `./update.sh --no-backup` updates without it. Builds older than
this feature can't take one, so the first update after it says so and carries on.

Restarting abandons a conversion in progress, so if one has been running for over two
hours the script says so and asks before it changes anything. `./update.sh -y` (or
`ARRMADA_UPDATE_FORCE=1`) goes ahead without asking, and so does a run with no terminal,
such as a scheduled one: it prints the warning and updates.

If the pull fails (local edits, or a branch that has diverged), the script stops without
rebuilding anything; `git status` shows what's in the way. `./update.sh --force-local` skips
the pull and rebuilds the code that's in the folder.

The Dashboard and `/api/health` show the version and commit that's running.

If you deploy with Komodo or another tool instead of `update.sh`, mirror these steps there:
pass `ARRMADA_VERSION` (`git describe --tags --always --dirty`) and `ARRMADA_COMMIT`
(`git rev-parse --short HEAD`) as build environment, run
`docker exec -u <PUID>:<PGID> Arrmada-app arrmada backup --kind pre-update` and tag the
running image `arrmada:previous` before each build. Otherwise none of this protection applies.

`docker exec Arrmada-app arrmada help` lists the maintenance commands built into the app
(`version`, `backup`, …). With no command, `arrmada` is the server.

### Rolling back an update

The build that was running before an update is kept as the Docker image `arrmada:previous`,
so `./update.sh --rollback` can start it again in a few seconds (the build you leave is kept
as `arrmada:rolled-back`, and the next `./update.sh` goes forward again). If the previous
build doesn't come up, the script starts the newer one again rather than leave Arrmada down.
Keeping the old image costs one extra image of disk, several GB with the subtitle and GPU
tooling.

An older build won't start on a database a newer one has upgraded: it stops with a message
saying so, because running old code on tables it doesn't know can damage them. So before it
stops anything, `--rollback` asks the previous build whether it can run on the database as
it is, and if the update changed the database it stops and says to use
`./update.sh --rollback --with-db`. That also puts back the pre-update backup, which means
everything Arrmada recorded since the update is lost, so it asks first (`-y` skips the
question). If the previous build can't restore a backup itself, the script changes nothing
and prints the steps to do it by hand. `ARRMADA_ALLOW_NEWER_SCHEMA=1` in `.env` starts an
older build on a newer database anyway; it's a last resort.

## Locked out?

If you've forgotten the admin password, set a new one from the server:

```sh
docker exec -it Arrmada-app arrmada reset-password you@example.com
```

It prints a new password once, in your terminal only (never in a log), and signs that
account out everywhere. To choose the password yourself, pipe it in with
`--password-stdin`. A name that matches no account lists the admin accounts. Anyone with
shell access to the server can do this, the same as they could edit the database.

## Backups

Arrmada copies its database into `<data>/backups` (on Unraid,
`/mnt/user/appdata/arrmada/backups` when the data dir is in appdata):

| Kind | When | Kept |
|---|---|---|
| Nightly | once a day after 04:00 server time (changeable), or at once after a long downtime | newest 7 (changeable) |
| Before update | before any schema change | newest 5 |
| Pre-update | when `update.sh` runs | newest 3 |
| Manual | **Back up now** | newest 10 |
| Before restore, Before user delete, Uploaded | automatically | newest 3 each |

Admins manage them in **Settings → System → Backups**: see every copy with its size and
schema version, take one now, change the nightly schedule, delete one, or **Download** it as a
`.db.gz` (decompress it with `gunzip` to get a plain SQLite file). The copies sit on the same
disk as the database, so they cover a bad update, corruption or a mistake, not a failed disk;
download one now and then to keep a copy somewhere else. Backups contain your API keys, the
Plex token and password hashes, so keep downloaded copies somewhere private.

**Restore** on a row puts that backup back (type `RESTORE` to confirm). The backup is checked
first (a damaged file, or one from a newer Arrmada, is refused), then staged, and swapped in
the next time Arrmada starts — never while it's running. Inside Docker the app restarts itself
straight away; otherwise restart the container (`docker restart Arrmada-app`), or cancel the
staged restore on the card. The database it replaces is kept as a "Before restore" backup, and
if the swap fails at start-up Arrmada starts on the database as it was and the card says why.
Everything since the backup was taken is lost: requests, watch history, listening places,
users and settings.

**Restore from file…** takes a `.db`, or a `.db.gz` from Download (up to 4 GB). It's checked
the same way, listed as "Uploaded", and restored with that row's Restore. Behind Cloudflare,
request bodies over 100 MB are refused, so upload big backups from your home network.

### Restoring when Arrmada won't start

The same restore works from the command line, without the web UI. It only stages the
backup; the swap happens when Arrmada next starts:

```sh
docker compose run --rm --no-deps arrmada-app backups          # list them
docker compose run --rm --no-deps arrmada-app restore arrmada-nightly-20261008T040012Z.db
docker compose up -d arrmada-app
```

If the container is running, `docker exec Arrmada-app arrmada restore <name>` followed by
`docker restart Arrmada-app` does the same. `restore` also takes a path to a `.db` or `.db.gz`
the container can see (it's copied into the backups folder first), and
`restore --cancel` drops a staged restore that hasn't run.

## Ports

The installer picks free ports so nothing clashes with apps you already run. It prints them
at the end and saves them in `.env`.

| Service           | Default | Setting                   |
| ----------------- | ------- | ------------------------- |
| Arrmada           | 7878    | `ARRMADA_PORT`            |
| qBittorrent WebUI | 8080    | `ARRMADA_QBIT_WEBUI_PORT` |
| BitTorrent        | random  | `ARRMADA_QBIT_PORT` — forward this on your router (TCP and UDP) |
| Audiobook server  | 13379   | `ARRMADA_AUDIOBOOK_PORT` — for listening apps; off until switched on in Audiobooks |

Change a value in `.env`, then run `./update.sh`.

Behind a reverse proxy or tunnel, give Arrmada its own hostname (for example
`arrmada.example.com`) and proxy it from the root. Serving it under a path such as
`example.com/arrmada` isn't supported: the old `ARRMADA_BASE_URL` setting never worked with the
web app and has been removed. If your `.env` still sets it, it's ignored and the app runs at the
root.

## Existing library

Give the installer the folder that contains your media, and pick each library inside it
during setup. Keep downloads on the same drive or share as your libraries so imports
hardlink. For read-only trials or unusual layouts, see
[docker-compose.override.example.yml](docker-compose.override.example.yml).

> ⚠ Never mount media at `/data`. That path holds Arrmada's own database.

Changing a library or the Downloads folder in Settings → Library takes effect straight
away: the next import, grab and disk-guard check use the new folder, and qBittorrent's
save path follows. Files already imported stay where they are.

### Recycle bin

Deleted and replaced files go to a hidden `.arrmada-recycle` folder at the top of the
library folder they came from (Movies, TV, and so on), so a delete is a quick move on the
same drive rather than a copy. Each of those folders has a `.plexignore`, so Plex doesn't
list what's in it, and Arrmada's own library scans skip hidden folders. Restore, size and
age limits are in Settings → System → Recycle bin; the size limit counts every bin together.

Older versions kept one shared bin in the app's library volume. It stays listed (marked
"Legacy") until you empty it, and only takes files that sit outside every library folder.

`ARRMADA_RECYCLE_DIR` in `.env` is now just an override: set it to a folder to use that one
bin for everything, or to `off` to delete files straight away.

## Develop

```sh
go run ./cmd/arrmada                  # backend (Go 1.25+) on :7878
cd web && npm install && npm run dev  # UI (Node 20+) on :5173, proxies /api
```

For a single binary with the UI inside it:

```sh
cd web && npm run build && cd .. && go build -o arrmada ./cmd/arrmada
```

## License

[MIT](LICENSE) © 2026 Tristen Lammi
