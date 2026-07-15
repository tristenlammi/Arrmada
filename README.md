<div align="center">

# ⛵ Arrmada

**One fleet. Every media job. Zero container sprawl.**

A single self-hosted app that replaces the entire `*arr` stack — Sonarr, Radarr, Readarr,
Lidarr, Bazarr, Unpackerr, Tautulli, Overseerr/Jellyseerr, and Prowlarr — with one coordinated,
professionally designed system.

</div>

---

> **Status:** early development (Milestone M0 — foundation). Not yet usable for media management.

## Planning docs

| Doc | What it covers |
|---|---|
| [ROADMAP.md](ROADMAP.md) | Strategy: vision, architecture, and phases. |
| [FEATURES.md](FEATURES.md) | The complete feature chart for all nine replaced apps. |
| [BUILD-PLAN.md](BUILD-PLAN.md) | The execution roadmap: milestones M0–M8 and the v0.1→v1.1 release train. |

## Tech

- **Backend:** Go (single static binary, embedded web UI, one port).
- **Frontend:** React + TypeScript + Vite + Tailwind, served from the Go binary.
- **Database:** SQLite (default) → PostgreSQL (later).
- **Architecture:** modular monolith — shared platform services + enable/disable modules.
- **License:** [MIT](LICENSE).

## Project layout

```
cmd/arrmada/          # main entrypoint
internal/
  buildinfo/          # version/commit stamped at build time
  config/             # env-driven runtime config
  httpapi/            # HTTP server: JSON API + middleware
  webui/              # embeds & serves the built web UI (dist/)
web/                  # React + Vite frontend source
```

## Run with Docker (easiest)

```sh
docker compose up --build      # builds UI + binary, starts on http://localhost:7878
docker compose down            # stop
```

One container, ~33 MB, data persisted in the `arrmada-data` volume. Auth is off by default for local
testing — set `ARRMADA_AUTH_ENABLED=true` in `docker-compose.yml` to require login.

## Development

**Backend** (Go 1.25+):

```sh
go run ./cmd/arrmada           # starts on http://localhost:7878
curl http://localhost:7878/api/health
```

**Frontend** (Node 20+) — hot-reload dev server, proxies `/api` to the backend:

```sh
cd web
npm install
npm run dev                    # http://localhost:5173
```

**Production build** (UI baked into the binary):

```sh
cd web && npm run build        # outputs to internal/webui/dist/
cd .. && go build -o arrmada ./cmd/arrmada
./arrmada                      # one binary serves API + UI on one port
```

### Configuration (env)

| Variable | Default | Purpose |
|---|---|---|
| `ARRMADA_HOST` | `0.0.0.0` | Bind interface. |
| `ARRMADA_PORT` | `7878` | HTTP port (API + UI). |
| `ARRMADA_BASE_URL` | _(root)_ | Reverse-proxy sub-path, e.g. `/arrmada`. |
| `ARRMADA_DATA_DIR` | `./data` | Config, database, logs, backups. |
| `ARRMADA_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error`. |

## License

[MIT](LICENSE) © 2026 Tristen Lammi
