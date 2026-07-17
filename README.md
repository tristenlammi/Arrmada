<div align="center">

# ⛵ Arrmada

**One app instead of the whole `*arr` stack.**

</div>

---

> **Status:** early and under active development. Working today: Movies, Series,
> Books, Subtitles, and Convert (HEVC/AV1 transcoding). Music and analytics are
> still on the way. Expect rough edges.

Arrmada is a single self-hosted app that aims to do the job of Radarr, Sonarr,
Readarr, Bazarr, and more — one coordinated system instead of a pile of
containers. It bundles qBittorrent, Prowlarr, and FlareSolverr alongside it.

## Tech

- **Backend:** Go — one static binary with the web UI embedded, served on one port.
- **Frontend:** React + TypeScript + Vite + Tailwind.
- **Database:** SQLite.
- **License:** [MIT](LICENSE).

## Install

One command. It asks **two** things — where your media lives and where to
transcode — and handles everything else (run user, free host ports, database
location, and GPU pass-through) automatically.

```sh
git clone https://github.com/tristenlammi/Arrmada
cd Arrmada
./install.sh
```

That's it. When it finishes it prints the URL — open it and create your admin
account. Grab a free [TMDB API key](https://www.themoviedb.org/settings/api)
for Movies/TV metadata (the installer asks for it, or add it later in `.env`).

<details>
<summary>What the installer does for you</summary>

- **Free ports** — it checks what's already running and picks host ports that
  don't clash with Radarr (7878), an existing qBittorrent (8080), etc. The
  chosen ports are printed at the end and saved in `.env`.
- **Run user** — auto-detected from your media folder's owner (falls back to
  `99:100` on Unraid, else `1000:1000`), so the app can read/write your files.
- **GPU** — if `/dev/dri` exists on the host it wires up hardware transcoding
  automatically; no manual device mapping. No GPU? Convert falls back to CPU.
- **Database** — stored in `/mnt/user/appdata/arrmada` on Unraid, else `./data`.
- **Media + transcode** — written into `docker-compose.override.yml` for you.

Optional: `./install.sh --with-prowlarr` also starts a Prowlarr indexer
manager (Arrmada has its own indexers, so this is only if you want it).
</details>

## Update

Pull the latest and rebuild — your `.env`, database, downloads, and media are
all preserved:

```sh
./update.sh
```

Only the app image is rebuilt and restarted. The bundled companions
(qBittorrent, FlareSolverr) and the one-shot init containers are **left
untouched** — they only run once, at install, so an update never re-runs them.

## Ports

The installer picks **free** host ports automatically, but by default:

| Service              | Host port                 | Notes                                             |
| -------------------- | ------------------------- | ------------------------------------------------- |
| Arrmada web UI       | `ARRMADA_PORT` (7878)     | The app. Auto-moved if 7878 is taken.             |
| qBittorrent WebUI    | `ARRMADA_QBIT_WEBUI_PORT` (8080) | Optional peek — Arrmada manages qBit for you.    |
| BitTorrent (qBit)    | `ARRMADA_QBIT_PORT` (random) | Forward this on your router (TCP + UDP).       |
| FlareSolverr         | *(not published)*         | Internal-only — reached over the Docker network.  |
| Prowlarr (opt-in)    | `ARRMADA_PROWLARR_PORT` (9696) | Only with `--with-prowlarr`.                   |

All are overridable in [.env](.env.example) — change a value, then `./update.sh`.

## Point it at an existing library

The installer's media prompt covers the common case. For read-only trials or
custom per-library paths, see
[docker-compose.override.example.yml](docker-compose.override.example.yml).

> ⚠ Never mount media at `/data` — that path holds Arrmada's own database + config.

## Develop

```sh
# backend (Go 1.25+) — http://localhost:7878
go run ./cmd/arrmada

# frontend (Node 20+) — http://localhost:5173, proxies /api to the backend
cd web && npm install && npm run dev
```

Production build bakes the UI into the binary:

```sh
cd web && npm run build      # → internal/webui/dist/
cd .. && go build -o arrmada ./cmd/arrmada
```

## License

[MIT](LICENSE) © 2026 Tristen Lammi
