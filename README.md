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
- **Music** (early) — artists, albums and whole-discography grabs.
- **Quality profiles** — a bitrate ceiling, required formats (Atmos, HDR, Dolby Vision), and preferred or rejected words.
- **Indexers** — any Torznab indexer, one-click sync from Prowlarr, and built-in MyAnonaMouse, TorrentLeech and 1337x. FlareSolverr handles Cloudflare-protected trackers.
- **Downloads** — the bundled qBittorrent sets itself up. Imports hardlink instead of copying, seeding rules clean up, stalled downloads fail over, and mismatched downloads wait in a review queue.
- **Safety nets** — a blocklist, a recycle bin, and a full history for every item.

**Requests and people**

- **Discover and requests** — browse and request movies, TV and books, Overseerr-style. Auto-approve per user, and import your existing Overseerr requests.
- **Users** — admin, manager, requester and read-only roles, with optional Plex sign-in. Visitors from outside your network only see Discover.
- **Books shelf** — requesters can download any ebook in the library.
- **Calendar** — what's coming up.
- **Notifications** — Apprise alerts for admins; an in-app inbox and web push for requesters.

**Media tools**

- **Subtitles** — pulls embedded subtitles out, fetches them from OpenSubtitles, or writes them with local Whisper AI (GPU-accelerated on Intel).
- **Convert** — HEVC or AV1 transcoding on Intel or AMD (VAAPI), Intel Quick Sync, NVIDIA NVENC, or the CPU. Dolby Vision and HDR10+ are kept.
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
settings, database, downloads and media are untouched.

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

## Existing library

Give the installer the folder that contains your media, and pick each library inside it
during setup. Keep downloads on the same drive or share as your libraries so imports
hardlink. For read-only trials or unusual layouts, see
[docker-compose.override.example.yml](docker-compose.override.example.yml).

> ⚠ Never mount media at `/data`. That path holds Arrmada's own database.

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
