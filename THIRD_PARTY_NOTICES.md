# Third-party notices

Arrmada is MIT-licensed. This file lists third-party code whose licence asks for a notice
beyond the ordinary attribution in `go.mod` and `web/package.json`.

## Audio tags (`internal/audiotag`)

`internal/audiotag` reads and writes music tags. The music import reads downloads' tags
through it (the matcher) and writes its house tag style through it (placement, in a
staging copy), both from the music rebuild's Phase 2, so it and the libraries below are
linked into the `arrmada` binary, and this file ships in the Docker image at
`/usr/share/doc/arrmada/THIRD_PARTY_NOTICES.md`, beside Arrmada's own `LICENSE`.

By the owner's decision of 2026-10-19 the package writes FLAC, MP3 and MP4/M4A with
permissively licensed libraries, and uses TagLib only for Ogg Vorbis and Opus, which none
of them covers.

### go-taglib — GNU Lesser General Public License v2.1

- Module: `go.senan.xyz/taglib` (v0.15.0), source: https://github.com/sentriz/go-taglib
- It embeds TagLib 2 (https://taglib.org, LGPL-2.1 / MPL-1.1, source:
  https://github.com/taglib/taglib) compiled to WebAssembly, as the file `taglib.wasm`.
- Licence text: https://www.gnu.org/licenses/old-licenses/lgpl-2.1.html, and in full in
  `LICENSES/LGPL-2.1.txt` (shipped in the image at `/usr/share/doc/arrmada/LICENSES/`).

**Source for the LGPL part.** The exact source of the TagLib code in each Arrmada build is
go-taglib's `v0.15.0` tag (https://github.com/sentriz/go-taglib/tree/v0.15.0) with the
TagLib commit its `taglib` submodule pins at that tag; `go.mod` names the version. If you
can't get it from there, ask through the Arrmada repository's issues
(https://github.com/tristenlammi/Arrmada/issues) and we'll send it to you: this offer is
valid for at least three years from the release of the Arrmada build you have, to anyone
who has it.

**Replacing the LGPL part.** The TagLib code runs from that one WebAssembly file, which
can be swapped for a build of your own without changing Arrmada: build Arrmada with

```
go build -ldflags="-X 'go.senan.xyz/taglib.binaryPath=/path/to/taglib.wasm'" ./cmd/arrmada
```

and go-taglib loads the file at that path instead of its embedded copy. go-taglib's
repository has the build for `taglib.wasm` (CMakeLists.txt, taglib.cpp). Arrmada's own
source is public, so the whole program can also be rebuilt against a modified go-taglib
with a `replace` line in `go.mod`.

### wazero — Apache License 2.0

- Module: `github.com/tetratelabs/wazero`, source: https://github.com/tetratelabs/wazero
- The WebAssembly runtime go-taglib runs TagLib in.
- Licence text: https://www.apache.org/licenses/LICENSE-2.0
- Its NOTICE file reads:

  ```
  wazero
  Copyright 2020-2023 wazero authors
  ```

### The permissive tag libraries

- `github.com/go-flac/go-flac` and `github.com/go-flac/flacvorbis` — Apache License 2.0
- `github.com/bogem/id3v2/v2` — MIT
- `github.com/abema/go-mp4` — MIT

## PostgreSQL native clients (all container variants)

`pg_dump`, `pg_restore` and libpq are unmodified PostgreSQL 18 client packages
from the PostgreSQL Global Development Group's signed apt repository. They are
separate executables/libraries, not linked into the Go binary. PostgreSQL uses
the permissive PostgreSQL License: https://www.postgresql.org/about/licence/.
Package copyright/licence notices remain under `/usr/share/doc/postgresql-client-18/`
and `/usr/share/doc/libpq5/` in the image. Upstream source:
https://www.postgresql.org/ftp/source/ (package versions are recorded by dpkg).

## The Unraid all-in-one image (`stable-aio`)

The all-in-one image (the Dockerfile's `aio` target, which the Unraid template runs) carries
other programs beside Arrmada, each unmodified, as published by its authors or by Ubuntu.
They are separate programs that Arrmada starts and talks to over HTTP; none is linked into
Arrmada. The plain image and the Compose install contain none of them (the Compose install
runs each in a container of its own).

| Program | Licence | Where it comes from (exact version in the `Dockerfile`) |
|---|---|---|
| qBittorrent (`qbittorrent-nox`, static build) | GPL-2.0-or-later | https://github.com/userdocs/qbittorrent-nox-static (source: https://github.com/qbittorrent/qBittorrent) |
| FlareSolverr | MIT | https://github.com/FlareSolverr/FlareSolverr, with Chromium (BSD-3-Clause and others) from chromium-browser-snapshots |
| SABnzbd (optional) | GPL-2.0-or-later; its bundled parts under `/opt/sabnzbd/app/licenses/` | https://github.com/sabnzbd/sabnzbd (the source release itself is what's in the image, at `/opt/sabnzbd/app`) |
| SABnzbd's Python packages | Their own (MIT, BSD, Apache-2.0, LGPL and others), in each package's `*.dist-info` under `/opt/sabnzbd/venv` | PyPI, pinned by hash in `docker/aio/sabnzbd-requirements.txt` |
| par2cmdline-turbo (optional, for SABnzbd) | GPL-2.0-or-later | https://github.com/animetosho/par2cmdline-turbo |
| Prowlarr (optional) | GPL-3.0; its licence at `/opt/prowlarr/bin/LICENSE` | https://github.com/Prowlarr/Prowlarr |
| unrar (for SABnzbd) | **Non-free freeware**: may be redistributed freely, inside other packages too, but its code may not be used to re-create the RAR compression algorithm. Full text in `/usr/share/doc/unrar/copyright` | Ubuntu's `unrar` package (multiverse) |
| 7-Zip (`7zip`, for SABnzbd) | LGPL-2.1-or-later, some parts BSD-3-Clause or public domain (Ubuntu's build leaves out the unRAR code); `/usr/share/doc/7zip/copyright` | Ubuntu's `7zip` package |

**Source for the GPL programs.** Each is an unmodified upstream release: its source is at
the tag of that release in the repository named above (qBittorrent's static build: the
qBittorrent and libtorrent tags its release name gives). If you can't get it from there,
ask through the Arrmada repository's issues and we'll send it to you, on the same terms as
the TagLib offer above (at least three years from the release of the image you have).
