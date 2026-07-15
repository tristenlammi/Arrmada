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

## Run it

You'll need a free [TMDB API key](https://www.themoviedb.org/settings/api) for
movie/TV metadata.

```sh
cp .env.example .env     # then paste your TMDB key into .env
docker compose up --build
```

Open <http://localhost:7878>. See [.env.example](.env.example) for the handful
of settings you can configure.

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
