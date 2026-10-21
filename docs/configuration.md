# Configuration

Almost everything is set in the app (Settings). A few things have to be known before the
container starts; those live in `.env` in Arrmada's folder. Change a line, then run
`sh update.sh` to apply it.

## Ports

The installer picks free ports so nothing clashes with apps you already run. It prints them
at the end and saves them in `.env`.

| Service           | Default | Setting                   |
| ----------------- | ------- | ------------------------- |
| Arrmada           | 7878    | `ARRMADA_PORT`            |
| qBittorrent WebUI | 8080    | `ARRMADA_QBIT_WEBUI_PORT` — on the server itself only, unless `ARRMADA_QBIT_WEBUI_BIND` says otherwise ([below](#qbittorrents-own-page)) |
| BitTorrent        | random  | `ARRMADA_QBIT_PORT` — forward this on your router (TCP and UDP) |
| Audiobook server  | 13379   | `ARRMADA_AUDIOBOOK_PORT` — for listening apps; off until switched on in Audiobooks |
| Prowlarr          | 9696    | `ARRMADA_PROWLARR_PORT` (only with `--with-prowlarr`) |
| SABnzbd           | 8085    | `ARRMADA_SABNZBD_PORT` (only with `--with-sabnzbd`) — its own web page, where your Usenet provider goes |

## qBittorrent's login

The bundled qBittorrent asks for a password, like any other web page. Arrmada makes one when
it first starts (a long random one), signs in with it, and keeps it in its data folder
(`qbittorrent-login.json`, readable only by the user Arrmada runs as). To open qBittorrent's
own page, an admin goes to **Settings → Connections**, opens the qBittorrent card and clicks
**Show its login**. The login is never in the client list, the logs or a support bundle.

Until then, a new install's qBittorrent lets in, without a password, only the Docker network
it shares with Arrmada (the installer writes that network's own subnet into its first
config), so Arrmada can sign in to give it the password. Installs from before October 2026
were set up with every private range instead (`10.0.0.0/8`, `172.16.0.0/12`,
`192.168.0.0/16`), which covers your home network too; Arrmada replaces that with the
password at its next start. It does so even if you removed qBittorrent's card (or switched
it off), as long as the bundled container still runs and lets Arrmada in, and no other
qBittorrent client is switched on in Arrmada: one may be reaching this same qBittorrent
under another address (`localhost`, its container's name, the server's own address), and a
password it doesn't have would shut it out. Then it's left for you to do. While a whitelist
that broad is still on, the log says so at every start and **Settings → Status** shows a
warning that says why it was left.

Nothing else on the Docker network gets in without the password, including FlareSolverr's
browser, which opens tracker pages. CSRF protection is on (Arrmada switches it on once for
installs that had it off), so another site can't send your browser's qBittorrent session
commands either. If you put qBittorrent's page behind a reverse proxy, it has to pass the
original `Host` header through (most do; plain nginx needs `proxy_set_header Host $host;`).
qBittorrent's host header validation stays off: with it on, qBittorrent refuses any page
whose address names another port than its own 8080, and the installer publishes it on
whichever port is free.

You can change the password in qBittorrent (Tools → Options → WebUI). Then enter the new one
on its card in Arrmada (**Edit**), or Arrmada can't get in; **Show its login** keeps showing
the one Arrmada made.

The "Bypass authentication for clients in whitelisted IP subnets" option is yours to use,
with one exception: turned back on with the old private ranges above, Arrmada takes it for
its old login-free access and switches it off again at its next start. The Docker network's
own subnet is yours too once Arrmada has given qBittorrent its password (to let your other
containers in, say); Arrmada takes that one back only when `update.sh --rollback --with-db`
or the [troubleshooting steps](troubleshooting.md#qbittorrent-login) switched it on, which
leave a marker (`qbittorrent-reopened` in the data folder) saying so. Even then it switches
a whitelist off only while qBittorrent still takes the login Arrmada kept: a password
changed since is never overwritten (the log and Settings → Status say so instead). Use
your own subnets (just your LAN's, say) and they're left alone. An install that already had
its own subnets there before this update is left alone too. **Settings → Status** warns
about any whitelist that covers a `/16` or more beyond the Docker network.

### qBittorrent's own page

Arrmada drives qBittorrent over the Docker network, so its page isn't needed day to day. On a
new install it's published on the server itself only (`127.0.0.1`, port
`ARRMADA_QBIT_WEBUI_PORT`): open `http://127.0.0.1:<port>` there, or from another computer
through an SSH tunnel (`ssh -L 8080:127.0.0.1:<port> you@server`, then
`http://localhost:8080`). To open it from any computer on your network, add
`ARRMADA_QBIT_WEBUI_BIND=0.0.0.0` to `.env` and run `docker compose up -d arrmada-qbittorrent`
(it restarts qBittorrent once). Its card in Settings → Connections links the page when it's
open to your network, and says where it is when it isn't.

An install from before this keeps its page open to your network until qBittorrent's container
is recreated: `update.sh` only restarts Arrmada, and says so at the end of each update until
`.env` has `ARRMADA_QBIT_WEBUI_BIND` either way (or the container has been recreated).

## SABnzbd's login

The bundled SABnzbd's own page is on your network (that's where your Usenet provider is
added), so it asks for a login: user `arrmada` and a long random password, made when it's
first installed, or given by Arrmada at its next start to a SABnzbd from before that. An
admin sees it on SABnzbd's card in **Settings → Connections** (**Show its login**); it's also
in SABnzbd's own settings, `docker compose exec arrmada-sabnzbd grep -E '^(username|password) ='
/config/sabnzbd.ini`. Arrmada itself talks to SABnzbd with its API key, which works with or
without a login. A login you set in SABnzbd (Config → General) is yours and never changed.

## `.env` settings

| Setting | What it's for |
|---|---|
| `ARRMADA_CHANNEL` | What updates bring: `stable`, the published releases (the default), or `main`, the newest code compiled on the server ([Updating](updating.md#stable-or-main)) |
| `ARRMADA_IMAGE` | The published image `update.sh` downloads on the stable channel (`ghcr.io/tristenlammi/arrmada:stable` when unset) ([Updating](updating.md#published-image-or-compiled-here)) |
| `ARRMADA_APP_IMAGE` | The image `docker compose` runs. `arrmada:dev` (what the installer writes) is whatever `update.sh` built or downloaded — leave it. Without the line, a plain `docker compose up -d` runs the published image ([Updating](updating.md#upgrading-from-v010-or-earlier)) |
| `ARRMADA_DATA_HOST` | Where the database, its backups and settings live on the host |
| `ARRMADA_STORAGE_HOST` | The media folder the installer mounted at `/storage` (for reference; the mount itself is in `docker-compose.override.yml`, so change it with `install.sh --media`) |
| `ARRMADA_MEDIA_HOST` | A host folder for `/media`, Arrmada's own downloads and library, in place of the managed Docker volume; every service that shares media follows it ([below](#media-in-a-folder-of-your-own-env-only)) |
| `ARRMADA_PUID`, `ARRMADA_PGID` | The user and group Arrmada and qBittorrent run as. On Unraid, 99 and 100 |
| `ARRMADA_QBIT_WEBUI_BIND` | The address qBittorrent's own page is published on: `127.0.0.1` (the server itself, the default) or `0.0.0.0` (every computer on your network). Applied with `docker compose up -d arrmada-qbittorrent` ([above](#qbittorrents-own-page)) |
| `TZ` | Timezone. Schedules — the Convert window especially — run on this clock ([list](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones)) |
| `COMPOSE_PROFILES` | The optional companions to run, a comma list: `prowlarr` (Prowlarr), `sabnzbd` (SABnzbd, for Usenet), e.g. `prowlarr,sabnzbd`. `update.sh` never changes it |
| `ARRMADA_RECYCLE_DIR` | Override for the recycle bin (see below) |
| `ARRMADA_DATABASE_BACKEND` | `sqlite` (default) or opt-in `postgres`; no automatic data conversion |
| `ARRMADA_POSTGRES_URL` | Secret connection URL to an existing PostgreSQL 16–18 UTF8 database; use verified TLS remotely |
| `ARRMADA_POSTGRES_SCHEMA` | Pre-provisioned dedicated schema owned by the application role; never `public` |
| `ARRMADA_POSTGRES_MAX_CONNECTIONS` | Application pool limit, 1–100 (default 4); allow extra ownership/backup/CLI connections |
| `ARRMADA_POSTGRES_BIN` | Optional native client directory for a bare binary; clients are bundled in the image |
| `ARRMADA_SKIP_MIGRATION_SNAPSHOT` | SQLite only: `1` to upgrade without the safety copy (a full disk) |
| `ARRMADA_ALLOW_NEWER_SCHEMA` | SQLite only: `1` to start an older version on a newer database — last resort |
| `ARRMADA_EXTERNAL_HEADER` | The header a tunnel or proxy stamps on outside requests (default `Cf-Connecting-Ip`); such requests are limited to the requester pages |
| `ARRMADA_LIBRARY_DIR`, `ARRMADA_DOWNLOADS_DIR`, `ARRMADA_MOVIES_DIR`, `ARRMADA_TV_DIR`, `ARRMADA_EBOOKS_DIR`, `ARRMADA_AUDIOBOOKS_DIR`, `ARRMADA_MUSIC_DIR` | Starting values for the library folders. The folders picked in the app win; most people never set these |
| `ARRMADA_PLEX_URL`, `ARRMADA_PLEX_TOKEN` | An older way to point Insights at Plex — connect Plex in Settings → Connections instead |
| `ARRMADA_ALLOW_REMOTE_SETUP` | `1` lets the first admin account be created from outside your home network. Off by default, so a fresh install reachable from the internet can't be claimed by whoever finds it first; take it out once you're in. Installs that already have an admin aren't affected |
| `ARRMADA_TRUST_CF_HEADER` | Leave unset for the automatic rule ([Who a visitor is](#who-a-visitor-is)). `1` believes Cloudflare's visitor header from any local proxy, `0` never |

After changing `.env`, run `docker compose up -d` (a plain restart doesn't pick it up). On
Unraid, add the setting as a Variable on the container instead.

API keys (TMDB, Hardcover, OMDb, OpenSubtitles…) and passwords are entered in the app, in
Settings → Connections — never in `.env`. Older installs may still have
`ARRMADA_TMDB_API_KEY` and friends there; they keep working as a fallback.

## Your media folder

Give the installer the folder that contains your media, and pick each library inside it
during setup. Changing a library or the Downloads folder in **Settings → Library** takes
effect straight away: the next import, grab and disk-guard check use the new folder, and
qBittorrent's save path follows. Files already imported stay where they are.

To mount a different folder later, run the install line again with `--media /new/folder`
([Install → Changing things later](install.md#changing-things-later)).

> ⚠ Never mount media at `/data`. That path holds Arrmada's own database.

### More than one Movies or TV folder

Movies and TV can each have extra folders — a Kids or Animation movies folder, an Anime or
Cartoons TV folder, often its own Plex or Jellyfin library. Add them in **Settings → Library →
More Movies and TV folders**. Each can have a rule (Kids, Animation or Anime): a new title goes
to the first folder whose rule matches, otherwise to the main folder, and you can pick another
when adding a title or approving a request. A scan walks every folder and files each title
under the folder it's in. **Move to folder** on a movie or show moves its files (a rename on the
same drive; to another drive each file is copied and checked, then the originals go to the
recycle bin). A folder still holding titles can't be removed. Extra folders must be inside a
folder mounted into the container — under `/storage` with the rest, or added to
`docker-compose.override.yml` (see the example file). Moving from Radarr or Sonarr with several
root folders? **Settings → Import & migrate → Import from Radarr / Sonarr** sets up the same
folders as part of the import (below). With one folder per library, nothing about folders shows
anywhere. A folder on another disk can have its own downloads folder, so its imports hardlink
([Libraries on separate disks](#libraries-on-separate-disks)).

### Moving from Radarr and Sonarr

**Settings → Import & migrate → Import from Radarr / Sonarr** (admins) brings a library over.
Enter the app's address and API key (Settings → General in Radarr or Sonarr; it's used for the
import and never saved) and Arrmada reads it without changing anything there. The preview shows:

- **The titles**: how many are already in Arrmada (films by TMDB id, shows by TMDB or TVDB id),
  how many are new, and any it can't match.
- **Folders**: each root folder and the Arrmada folder its titles go to — matched by path or by
  name, or added on the spot as an extra folder.
- **Where the files are**: Radarr and Sonarr often see your share at another path (`/movies`
  there, `/storage/media/movies` here). Map the start of their paths to Arrmada's and press
  **Check again** until the files are found. A file must be inside one of Arrmada's library
  folders to be used.
- **Quality profiles**: an Arrmada profile for each of theirs — the one of the same name, else
  one reaching the same resolution, else your default.

**Import** runs in the background. Each title is added with its profile, monitoring and minimum
availability (films), or its monitored seasons and episodes, anime type and "monitor new
seasons" (shows); the files already on disk are recorded where they are — nothing is moved or
downloaded again. A title whose files can't be found is skipped and named in the summary (fix
the mapping and run it again), unless you ask for such titles to come over anyway and be
downloaded again. Nothing is searched when you import: wanted titles join the regular missing
searches ten at a time per run, so a big library doesn't start hundreds of downloads at once.
Running the import again — or for a second instance, like Sonarr and Sonarr-anime — adds only
what's new; a title already in Arrmada keeps its settings and only gets files it was missing.
Tags, blocklists, history and download-client settings aren't brought over.

### Hardlinks

For hardlinks, the library and the download folder must sit under **one** mount: one share
(say `/mnt/user/data`) with `media/movies`, `media/tv` and `torrents` inside, mounted once at
`/storage` in both Arrmada and qBittorrent. Then a finished download appears in your library
instantly, takes no extra space, and keeps seeding. The installer sets it up this way.

On Unraid, `/mnt/user` merges disks: a hardlink only works when the download and the library
land on the same physical disk; otherwise the import falls back to a copy (the log says
"imported by COPY"). If a share suddenly refuses new folders ("permission denied" on a
library that used to work), one disk's copy of the folder is owned by root — fix it through
the share path: `chown 99:100` and `chmod 775` on the library parent folders.

### Libraries on separate disks

With Movies on one disk and TV on another, one Downloads folder means one of them always
imports by copying, and the copy takes the space twice while the torrent seeds. Give that
library folder a downloads folder **on its own disk** instead: in **Settings → Library → More
Movies and TV folders**, use **Download to a folder on this disk…** on the folder (the main
Movies and TV folders have it too; with one folder per library, open it with **Libraries on
different disks?** first). Torrents for titles in that folder then save there and import as
hardlinks. Anything without one keeps using the Downloads folder.

1. Make a downloads folder on each disk, next to its library — for example
   `/mnt/disk1/media/movies` with `/mnt/disk1/torrents`, and `/mnt/disk2/media/tv` with
   `/mnt/disk2/torrents`.
2. Mount both disks into Arrmada's container (the installer's `--media` folder, or extra
   mounts in `docker-compose.override.yml`).
3. **If qBittorrent runs in its own container, mount the same folders into it at the same
   paths.** Arrmada tells qBittorrent where to save by path; qBittorrent has to see that path,
   and Arrmada has to see the finished files at the same one. (Different paths need a path
   mapping on the qBittorrent card in Settings → Connections.)
4. Pick each downloads folder. It must exist, be writable, and sit outside every library
   folder and Arrmada's data folder. If it isn't on the same filesystem as its library folder
   the save goes through with a warning, and the health panel keeps a note of it — imports from
   it would still copy.

What to expect:

- **Torrents only.** SABnzbd files jobs by category, not by folder, so usenet downloads keep
  going to SABnzbd's own folders. A usenet download is deleted once it's imported, so it never
  takes the space twice for long.
- The disk guard watches every downloads folder and pauses only the downloads saving onto a
  full disk; the health panel checks each one exists, is writable and has free space.
- **Move to folder** moves the title's library files, not a download that's still seeding:
  that keeps seeding where it is (its hardlink keeps the data). New downloads and upgrades for
  the title go to the new folder's downloads folder.
- qBittorrent's own "keep incomplete torrents in" folder, if you've switched it on, is one
  folder for everything: a download sits there until it finishes and is then moved to the
  library's downloads folder. Arrmada's bundled qBittorrent has it off.
- Arrmada adds its torrents with automatic torrent management off, so a qBittorrent whose
  default mode is Automatic still saves them where Arrmada asked, and re-filing one into
  another category never moves its files.
- Clearing or changing a folder's downloads folder (or removing the library folder) doesn't
  strand what's already there: those torrents keep seeding, and they can still be imported
  by hand or from Needs you.

### Media in a folder of your own, `.env` only

Arrmada has two places for media inside the container:

- `/media` is its own: the downloads and library it makes when you give it no media folder
  (`/media/downloads`, `/media/library`). By default it's a Docker volume, `arrmada-media`.
- `/storage` is your media folder, when the installer was given one (`--media`). Its mount is
  in `docker-compose.override.yml`, which the installer writes.

To keep `/media` in a host folder instead of the volume, without any override file, add one
line to `.env` and run `sh update.sh`:

```sh
ARRMADA_MEDIA_HOST=/mnt/user/arrmada-media
```

Arrmada, qBittorrent, SABnzbd and the one-shot that makes the folders all mount that line, so
a file has the same path in each and imports hardlink. The paths inside the container don't
change (still `/media/...`), so the folders already saved in the app keep working. When the
folder is new or empty it's handed to the user Arrmada runs as; when it already holds files,
only the `downloads` and `library` folders Arrmada makes are, and your own files keep their
owners. Which of the two `/media` is comes from what's really mounted, so a folder named
some other way (written into the compose file, a network volume of your own) is handled as
yours too. Use a full path: a relative one is refused. A folder that is, holds or sits inside
the database's folder is refused: media never goes with the database. Don't point it at the folder already
mounted at `/storage` either: that works, but two mounts of one folder can't hardlink between
them, so pick one place for each library and its downloads.

`update.sh` moves qBittorrent (and SABnzbd) to the new folder along with the app. What was in
the volume stays there. To bring it across, do the move with everything stopped instead, in
Arrmada's folder:

```sh
docker compose stop
docker volume ls | grep arrmada-media          # its full name, e.g. arrmada_arrmada-media
docker run --rm -v arrmada_arrmada-media:/from -v /mnt/user/arrmada-media:/to alpine cp -a /from/. /to/
# now add the ARRMADA_MEDIA_HOST line to .env, then start everything on the new folder:
docker compose up -d
```

**A share that isn't mounted yet.** If the folder is a mount point (an NFS or SMB share, an
Unraid share before the array starts) and the share isn't there when Arrmada starts, Docker
mounts the empty folder underneath, and Arrmada makes its `downloads` and `library` there,
on the wrong disk. Start Arrmada after the share is mounted (on Unraid, the array first).

Remove the line (and run `update.sh`) to go back to the volume. Leave it out entirely and
nothing changes: `/media` stays the `arrmada-media` volume it has always been.

`/storage` has no `.env` switch of its own: Compose can't leave a mount out when a variable
is empty, and an empty `/storage` would look like a media folder to the setup. Use
`install.sh --media` (or the override file below) for it, or put everything in `/media` with
`ARRMADA_MEDIA_HOST`. The [plain Compose file](install.md#docker-compose-without-the-installer)
always has a media folder, so there `/storage` is set from `.env` too (`ARRMADA_STORAGE_HOST`).

### Other layouts

For read-only trials or unusual layouts, see
[docker-compose.override.example.yml](../docker-compose.override.example.yml).

## GPU

If your server has `/dev/dri` (Intel or AMD graphics), the installer passes it through, and
Convert and Whisper subtitles use it (Convert once **Use my GPU** is on in Settings → Convert).
`docker exec Arrmada-app /usr/lib/jellyfin-ffmpeg/vainfo` shows what the container sees.
NVIDIA cards can do Convert's encoding through the NVIDIA Container Toolkit. [GPU](gpu.md)
has the setup for each, and where Arrmada shows which encoders passed its start-up test.

On ARM (arm64) there's no GPU acceleration: the image carries no GPU encoders or GPU subtitle
build, Convert and Whisper use the CPU, and the installer doesn't pass `/dev/dri` through.
Passing it through by hand does no harm; Convert tests every encoder at start-up and only
offers the ones that work.

## Convert's scratch folder

Convert encodes into a scratch folder before swapping the result in. The installer puts it at
`transcode/` next to the database; a fast SSD or NVMe pool is better than the array
(`--transcode /mnt/cache/transcode`, with the share set to the pool only so the mover skips
it). Settings → Convert can also point it somewhere else and shows the free space.

## Download disk guard

Settings → Downloads → **Download disk guard** pauses downloads that are still pulling data
once their drive is too full (85% by default) and resumes them when it drops back (80%). It
only ever resumes what it paused itself, and never pauses seeding torrents. It's on by
default.

It measures the drive the **downloads folder** is on. That's only right if the downloads
folder's drive is the one that fills up. When it isn't, set **Measure a different drive**:
the guard then reads that path's drive instead, and downloads still go to the downloads
folder as before.

The usual case is Unraid. The downloads folder sits in a user share (`/mnt/user/...`), which
reads as the whole array, but new torrents land on the cache pool and only reach the array
when the mover runs. The guard sees the array's percentage, never triggers, and the cache
fills. Pointing the downloads folder at `/mnt/cache/...` instead isn't the answer: downloads
and media would be on different mounts, so imports would copy instead of hardlinking. Give
Arrmada a read-only view of the pool and measure that:

- **Unraid template:** edit Arrmada, choose **Add another Path, Port, Variable, Label or
  Device**, and add a Path with Container Path `/cache`, Host Path `/mnt/cache` (or your
  pool's name, such as `/mnt/fastpool`) and Access Mode **Read Only**. Apply, then pick
  `/cache` under Measure a different drive.
- **Docker Compose:** in `docker-compose.override.yml`, add `- /mnt/cache:/cache:ro` to the
  `volumes:` of the `arrmada-app` service (`services:` → `arrmada-app:` → `volumes:`), run
  `docker compose up -d` to recreate the container with it, then pick `/cache`.

The path must exist inside the container, be a folder whose disk usage can be read, not be
`/` or on a RAM or container filesystem (tmpfs, overlay and the like), and not be on the same
filesystem as the downloads folder (that's already what the guard measures). The RAM check
catches a pool name typed wrong on Unraid: Docker then creates the missing Host Path as an
empty folder in RAM, which would otherwise pass and never fill up. The card then shows the
path it reads, how full it is and its size and free space.

**Free-space limit.** **Also pause when free space drops below (GB)** adds a second line,
off (0) by default: downloads pause when the drive is past the percentage **or** has less
free space than this. Held downloads resume once the percentage is back under its resume
point **and** free space is back above the limit plus 10% (at least 1 GB more), so a drive
hovering at the limit doesn't pause and resume every few minutes. It applies to whatever is
measured, the downloads folder or a different drive. GB here is 1024³ bytes, as the card
shows sizes.

**ZFS pools.** On a ZFS pool (the default for new Unraid pools since 6.12) every share is a
dataset of its own, and the pool's root only reports its own dataset's usage: the percentage
reads near 0% however full the pool is, while its free space is right. When the measured
drive is ZFS the card says so, and Settings → Status warns until a free-space limit is set.
Set one, sized to what your downloads need to finish (say 100–200 GB).

**More than one downloads folder.** When library folders have downloads folders of their own
([Libraries on separate disks](#libraries-on-separate-disks)), the drive you set stands in
for the Downloads folder and for every other downloads folder on the **same filesystem** as
it. On Unraid that's every downloads folder inside `/mnt/user`, since the user shares are
all one filesystem there. A downloads folder on a disk mounted on its own keeps its own
measurement.

**If the drive goes away.** If the path can no longer be read (the mapping was removed, or
the pool isn't mounted), or it now reads as a RAM or container filesystem (Docker recreated a
missing Host Path), the guard doesn't fall back to the downloads folder: that's exactly the
reading the setting replaces. It pauses nothing new and resumes nothing for the folders it
measures there (downloads folders on other drives are still guarded), and says so in the card
and in Settings → Status. Anything it already paused stays paused for now; you can resume it
by hand, and turning the guard off releases it. Clearing the setting releases it too, unless
the downloads folder's own drive is over the line. Map the path back, or clear the setting.

## Recycle bin

Deleted and replaced files go to a hidden `.arrmada-recycle` folder at the top of the
library folder they came from (Movies, TV, and so on), so a delete is a quick move on the
same drive rather than a copy. Each of those folders has a `.plexignore` and an `.ignore`, so
neither Plex nor Jellyfin lists what's in it, and Arrmada's own library scans skip hidden folders. Restore, size and
age limits are in Settings → Downloads → Recycle bin; the size limit counts every bin together.

Older versions kept one shared bin in the app's library volume. It stays listed (marked
"Legacy") until you empty it, and only takes files that sit outside every library folder.

`ARRMADA_RECYCLE_DIR` in `.env` is now just an override: set it to a folder to use that one
bin for everything, or to `off` to delete files straight away.

## Usenet (SABnzbd)

Arrmada can download from Usenet as well as torrents, through SABnzbd. It's optional, and
it needs two things Arrmada can't give you: **your own Usenet provider** (a paid account —
the news servers the files come from) and **Newznab indexers** (the sites that list what's
on Usenet). Without both, Usenet adds nothing.

There are two ways to have SABnzbd: the bundled one, or your own.

**The bundled one.** `install.sh --with-sabnzbd` (on a new install, or later on the one you
have) runs SABnzbd beside Arrmada and Arrmada connects to it by itself, usually within a
minute: its API key is read from SABnzbd's own settings (never written to `.env`, never
shown), Arrmada's categories are there with a folder each, it's in English, every job is
repaired, unpacked and tidied (post-processing 3), and its downloads go under `usenet/` in
your Downloads folder — `usenet/incomplete` while downloading, `usenet/complete` when
done — so an import is a rename on the same drive rather than a copy. That folder follows
your Downloads folder when you change it in Settings → Library, once Arrmada has checked
SABnzbd can see it (the installer mounts your media folder into SABnzbd for that).

What's left is your provider: open `http://<server>:8085` (the port the installer printed;
the SABnzbd card in Settings → Connections has the link, and **Show its login** for signing
in: [SABnzbd's login](#sabnzbds-login)) → **Config → Servers**, and add it there — the
first visit opens SABnzbd's setup wizard, which does the same. Leave the
folders it shows as they are. Getting started and the health list say "Add your Usenet
provider in SABnzbd" until there's a news server. Open SABnzbd by the server's IP address:
it only answers to the names in its `host_whitelist` (Config → Special), which starts with
just `arrmada-sabnzbd`; add your server's name there to use that instead.

Removed in Arrmada, the bundled SABnzbd stays removed (even after a restart) until **Restore
bundled SABnzbd** in Settings → Connections. To stop running it at all, take `sabnzbd` out of
`COMPOSE_PROFILES` in `.env` and run `docker compose rm -sf arrmada-sabnzbd`.

**On Unraid, the template's built-in one.** The Unraid template's container has the same
SABnzbd inside it, off until you set **Built-in SABnzbd** to `true` (Show more settings).
Arrmada connects to it the same way, and its page is on the **SABnzbd web UI port** (8085).
Its settings live in `sabnzbd/` in Arrmada's appdata; its downloads go under `usenet/` in
your Downloads folder, on your media share. Set the switch back to `false` and its client
in Settings → Connections is switched off with it (its settings are kept for next time).
[Arrmada on Unraid](unraid.md#the-built-in-sabnzbd-and-prowlarr) has the details.

**Your own SABnzbd.** In Settings → Connections → **+ Add client**, pick SABnzbd and give
its address (as Arrmada's container reaches it) and its **full API key** (SABnzbd → Config →
General; the NZB key can't read the queue). Arrmada never changes your SABnzbd by itself:
its card shows which of Arrmada's categories are missing, and **Add missing categories**
creates them when you click it. If SABnzbd says "hostname verification failed", add the
name Arrmada uses to its `host_whitelist`. If SABnzbd sees its downloads under a different
path than Arrmada does, add a path mapping on the card. Keep SABnzbd in English: Arrmada
reads its failure reasons, and translated ones can't be told apart.

**Indexers.** Add Newznab indexers in Settings → Connections → **+ Add indexer** (the
Newznab tile appears once a Usenet client exists), or keep them in Prowlarr: its Usenet
indexers come over in the Prowlarr sync once Arrmada has a Usenet client, and adding one
runs the sync straight away.

**Usenet or torrents.** By default the better release wins wherever it is. Settings →
Downloads → **When a release is on both Usenet and torrents** can prefer one network:
the preference only decides between releases that are otherwise as good — it never picks a
worse release, and it never hides the other network's.

**When a download fails.** Arrmada reads SABnzbd's reason:

- Something wrong with the release — articles missing from your provider, damage its
  repair files can't fix, a password, an archive that won't unpack, a duplicate — and the
  release is blocklisted and another one searched for, as with a torrent that can't finish.
- Something wrong where SABnzbd writes — a full disk, a folder it may not write to, a
  missing drive — and nothing is blocklisted (any release would fail the same way). Needs
  you says what to fix; then Retry the download in Activity.
- Nothing downloading for half an hour while jobs wait is almost always the provider or
  your account, so it's never "fixed" by grabbing something else: the health list says to
  check your provider instead.

After an import Arrmada archives the job in SABnzbd's history and deletes what's left of
its folder. Usenet downloads never seed.

## MusicBrainz (Music)

Music (a preview, off by default) gets artists, albums and track listings from
MusicBrainz. Arrmada asks MusicBrainz directly; it never runs or relies on a central
metadata server of its own. MusicBrainz allows one request a second from an address, so
Arrmada sends at most that (every part of the app shares the one limit), says who it is
in its User-Agent, and keeps every answer in its database for good. Pages open from that
cache, a background task rechecks old answers every six hours, and when MusicBrainz is
down or refusing requests Music keeps working from what it already has. A failed or
empty answer never removes, unmonitors or relists anything.

The **MusicBrainz** card in Settings → Connections (admin only) sets:

- **Server** — leave empty for `https://musicbrainz.org`, or enter your own mirror's
  address (a LAN address is fine).
- **Mirror requests a second** — how fast a mirror may be asked (1 to 50). The public
  server always gets one a second, whatever this says.
- **Contact email** — optional; it's added to the User-Agent so MusicBrainz can reach you
  instead of blocking the address. It stays out of the logs and the support bundle.

**Test** looks up a well-known artist on the address in the box.

### Self-hosted MusicBrainz

A mirror ([musicbrainz-docker](https://github.com/metabrainz/musicbrainz-docker)) lifts the
one-a-second limit. It needs about 100 GB of disk and 4 GB of RAM without its search
indexes, or about 350 GB and 16 GB with them, plus a MetaBrainz access token for the daily
updates. Its Solr search service must never be reachable from the internet. Arrmada's
cache is kept whichever server you use, so switching to or from a mirror loses nothing.

## Connecting Jellyfin

Jellyfin is connected in the app, on its card in Settings → Connections (next to Plex's):

1. **Server URL** — the address Arrmada reaches Jellyfin on, for example
   `http://192.168.1.10:8096`, or `http://jellyfin:8096` when both run in the same Docker
   network.
2. **API key** — make one in Jellyfin under **Dashboard → API Keys → +**, and paste it. Test
   checks it before you save; once saved it's never shown again.
3. **Public address for Watch links** — what every requester's Jellyfin button opens. It
   starts as the server URL; if that only works at home (a `192.168.…` address or a
   container name), set the address you reach Jellyfin on from outside, like
   `https://jellyfin.example.com`.

The rest is in Settings → Media servers, Jellyfin's group:

- **Watch monitoring** — off until you switch it on. Arrmada then asks Jellyfin what's playing
  every few seconds and records each play in Insights; Jellyfin's history in Insights starts
  then.
- **Library updates** — on by default once connected. If a line says "whole library", add a
  path mapping from Arrmada's folder to the same folder as Jellyfin sees it.
- **Sign-in** — let people sign in with their Jellyfin username and password, and optionally
  Quick Connect (switch it on in Jellyfin too, under Dashboard → General). Passwords go
  straight to Jellyfin and are never kept.
- **Disconnect** — forgets the address and key; the choices above stay for a reconnect.

If you keep several versions of a movie (say 4K and 1080p), Jellyfin shows them as one title
only when each file name starts with the movie folder's name — `Dune (2021)/Dune (2021) -
2160p.mkv` — which Arrmada's default movie naming does. If you've changed the naming in
Settings → Naming & metadata so file names start differently, Jellyfin lists each version
as its own movie.

The first-run setup's **Media server** step offers the same: Plex, Jellyfin, both, or later.

## Reverse proxies and tunnels

Give Arrmada its own hostname (for example `arrmada.example.com`) and proxy it from the root.
Serving it under a path such as `example.com/arrmada` isn't supported: the old
`ARRMADA_BASE_URL` setting never worked with the web app and has been removed. If your `.env`
still sets it, it's ignored and the app runs at the root.

Behind Cloudflare, uploads over 100 MB are refused, so restore big backups from your home
network.

### Who a visitor is

Arrmada works out each visitor's address for two things: whether they're on your home network
(people with requester accounts get only the requester pages from outside, and first-run setup
only works from home), and the sign-in limits, which count wrong passwords per address.

- **Straight to the port** (on your LAN, or a port forward): the address the connection came
  from.
- **Through a proxy on your network** (nginx, Nginx Proxy Manager, Caddy, Traefik, cloudflared):
  the visitor's address the proxy writes into `X-Forwarded-For` (or `Forwarded`), read from the
  right so a visitor can't put a made-up address in front of it. Forwarding headers are only
  believed from a private or loopback address.
- **Cloudflare Tunnel**: works with no settings. `Cf-Connecting-Ip` is used when no forwarding
  header names an internet address (cloudflared talking to Arrmada, or through a proxy that only
  records cloudflared's address), or when the nearest internet hop is Cloudflare's own edge (an
  orange-cloud hostname in front of your proxy). Behind nginx, Caddy or Traefik, which record the
  visitor's real address, a visitor's own `Cf-Connecting-Ip` is ignored. The header isn't proof
  that Cloudflare sent the request, though: a device or container on your network that reaches
  the port directly, with no proxy in between, can still send one and pick the address it's
  counted under — as it always could with `X-Forwarded-For`.
  `ARRMADA_TRUST_CF_HEADER=1` believes it from any local proxy (only if nothing but Cloudflare can
  reach that proxy); `ARRMADA_TRUST_CF_HEADER=0` never believes it.

> ⚠ A proxy must send `X-Forwarded-For` (or `Forwarded`). Without it every visitor arrives from
> the proxy's private address, so **everyone looks like they're on your home network** and they
> all share one sign-in limit. Caddy, Traefik and Nginx Proxy Manager send it by default; plain
> nginx needs `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` (and keep
> `proxy_set_header Host $host;`, so the browser's address matches the one Arrmada sees).
