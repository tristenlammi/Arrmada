# Arrmada on Unraid

There are two ways to run Arrmada on Unraid:

- **The Community Apps template** (this page). One container, managed from Unraid's Docker
  page like any other app, with **qBittorrent and FlareSolverr built in**: Arrmada starts
  them, connects to them and keeps their settings in step, so there's no download client to
  set up. Already have your own qBittorrent or FlareSolverr? Switch the built-in one off and
  use yours. A **SABnzbd** (for Usenet) and a **Prowlarr** are in the container too, but
  they're off until you switch them on ([below](#the-built-in-sabnzbd-and-prowlarr)); your
  own SABnzbd or Prowlarr works just as well.
- **The Compose install** ([Install](install.md)). The same app with qBittorrent and
  FlareSolverr as containers of their own, through the Docker Compose Manager plugin, plus
  the optional Prowlarr and SABnzbd. Its folders are lines in the stack's `.env`
  (`ARRMADA_DATA_HOST`, and `ARRMADA_MEDIA_HOST` for media in a share rather than in
  `docker.img`), so the compose file itself never needs editing
  ([Configuration](configuration.md#media-in-a-folder-of-your-own-env-only)).

Both keep the same kind of database, but there's no switch from one to the other: choose one.

## Your share layout

Arrmada imports a finished download by hardlinking it into your library: no copy, no extra
space, and the torrent keeps seeding. A hardlink only works inside one mount, so keep your
libraries and your downloads in **one share**, given to Arrmada at `/storage`. For example,
a share called `data`:

```text
/mnt/user/data
├── media
│   ├── movies
│   ├── tv
│   ├── ebooks
│   └── audiobooks
├── downloads         ← the built-in qBittorrent saves here (or torrents/, your choice);
│   └── usenet        ← and the built-in SABnzbd, if you switch it on
└── usenet            ← SABnzbd, if you use your own
```

`/mnt/user` merges your array disks, so a hardlink also needs the download and the library
copy to land on the same disk. When they don't, the import copies instead and the log says
"imported by COPY". Setting the share's allocation method or split level so a folder stays
on one disk helps.

> ⚠ Never point anything at `/data` inside the container except appdata. That's where
> Arrmada keeps its database (and the built-in qBittorrent, SABnzbd and Prowlarr their
> settings).

### The disk guard and your cache pool

The download disk guard (Settings → Downloads) pauses downloads before their drive fills.
It measures the downloads folder's drive, and on Unraid that's the user share: the whole
array, not the cache pool where new torrents actually land before the mover runs. It would
show the array's percentage and never step in while the cache fills.

Give Arrmada a read-only view of the pool and measure that instead:

1. Edit Arrmada in the Docker tab and choose **Add another Path, Port, Variable, Label or
   Device**.
2. Add a **Path**: Container Path `/cache`, Host Path `/mnt/cache` (or your pool's name,
   such as `/mnt/fastpool`), Access Mode **Read Only**. Apply.
3. In Arrmada, Settings → Downloads → Download disk guard → **Measure a different drive**:
   pick `/cache` with **Browse**.

The card then shows `/cache`, how full the pool is and its free space. Keep the downloads
folder in the share (`/storage/downloads`) as above, so imports still hardlink; only the
measurement moves. Arrmada refuses a path that reads as RAM: if the pool's name in the Host
Path is wrong, Docker makes an empty folder in RAM instead, and the guard would never pause.

**A ZFS pool** (the default for new pools since Unraid 6.12) needs one more setting. Each
share on it is its own dataset, and the pool's root counts only its own, so the percentage
reads near 0% however full the pool is. Its free space is right, so set **Also pause when
free space drops below** in the same card (say 100–200 GB); the card and Settings → Status
remind you when the measured drive is ZFS. Arrmada's template doesn't add this path itself: pools have different
names, and Docker would quietly create a missing host path in RAM.
If the path later goes away, the guard warns (in the card and Settings → Status) and stops
pausing or resuming rather than measuring the array again. More in
[Configuration](configuration.md#download-disk-guard).

## Install from Community Apps

1. Open the **Apps** tab, search for **Arrmada**, and click **Install**.
2. Fill in **Media and downloads** with your share (`/mnt/user/data` in the example above).
   Everything else can stay as it is:

   | Field | Default | What it is |
   |---|---|---|
   | Web UI port | 7880 | The web page. Not 7878, because Radarr usually has that |
   | Torrent port (TCP) and (UDP) | 56881 | The built-in qBittorrent's incoming port. Forward it on your router (TCP and UDP) to seed well |
   | Audiobook server port | 13379 | For listening apps (Lissen and others). Off until you switch it on in the app |
   | App data | `/mnt/user/appdata/arrmada` | The database, its backups and settings, and the built-in apps' settings (`/data` inside) |
   | Media and downloads | — | Your share, at `/storage` inside |
   | qBittorrent web UI port *(more settings)* | 8091 | Optional: qBittorrent's own page, if you want to look inside it |
   | SABnzbd web UI port *(more settings)* | 8085 | The built-in SABnzbd's page, once it's switched on: where your Usenet provider goes |
   | Prowlarr web UI port *(more settings)* | 9697 | The built-in Prowlarr's page, once it's switched on: where your indexers go. Not 9696, so it stays clear of a Prowlarr you already run |
   | Transcode folder *(more settings)* | — | Optional scratch space for Convert. A cache pool is better than the array (`/mnt/cache/transcode`) |
   | Built-in qBittorrent / FlareSolverr *(more settings)* | true | `false` to use your own instead ([below](#using-your-own-download-client-or-flaresolverr)) |
   | Built-in SABnzbd / Prowlarr *(more settings)* | false | `true` to run them ([below](#the-built-in-sabnzbd-and-prowlarr)) |
   | Torrent port setting *(more settings)* | 56881 | The port qBittorrent listens on: always the same number as the torrent port entries |
   | qBittorrent web UI host port *(more settings)* | 8091 | The same number as the qBittorrent web UI port, so Arrmada links the right page |
   | SABnzbd / Prowlarr web UI host port *(more settings)* | 8085 / 9697 | The same numbers as their web UI ports, so Arrmada links the right pages |
   | PUID / PGID *(more settings)* | 99 / 100 | Unraid's `nobody:users`, which own your shares. The built-in qBittorrent, SABnzbd and Prowlarr run as them too |
   | Starting library / downloads folder *(more settings)* | `/storage`, `/storage/downloads` | Only used until you pick the folders in the app |
   | FlareSolverr URL *(more settings)* | — | Blank uses the built-in one. Your own FlareSolverr's address if you switched that off |

3. Click **Apply**, then **WebUI** on Arrmada's icon in the Docker tab.

Your timezone comes from Unraid itself (Settings → Date and Time), so schedules such as
Convert's overnight window run on your clock. API keys and passwords never go in the
template: you enter them in the app.

**A port that's taken.** If Unraid says a port "is already allocated", another container
has it: pick another number. For the torrent port, change it in **all three places** (both
sides of the TCP entry, both sides of the UDP entry, and **Torrent port setting**):
qBittorrent tells other peers the port it listens on and can't know about a different
number on the server side. The SABnzbd and Prowlarr ports are taken from the server even
while those two are switched off: if 8085 or 9697 is already in use, change the number on
the server side (and in the matching host port setting).

## Install before it's listed

Until Arrmada shows up in Community Apps, install the same template by hand. In the Unraid
terminal (top right of the web UI):

```sh
curl -fsSL https://raw.githubusercontent.com/tristenlammi/Arrmada/main/templates/arrmada.xml \
  -o /boot/config/plugins/dockerMan/templates-user/my-Arrmada.xml
```

Then **Docker → Add Container**, choose **Arrmada** from the **Template** list, and carry on
from step 2 above.

## First start

Open the web UI from your home network and create your admin account (it can't be created
from outside, so nobody else can claim a new install). Setup then walks you through:

1. **Your TMDB key** (free, [themoviedb.org](https://www.themoviedb.org/settings/api)).
2. **Your folders.** Arrmada looks inside `/storage` and fills in folders named like your
   libraries (`/storage/media/movies`, `/storage/media/tv`, `/storage/downloads` in the
   example). Check them, or pick others with **Browse**. The built-in qBittorrent saves to
   the Downloads folder you pick.
3. **Your media server** (Plex, Jellyfin or both) and **indexers**.

The Dashboard's **Getting started** list ticks itself off as you go. **Connect a download
client** is already done: the built-in qBittorrent registers itself on first start.

## The built-in qBittorrent and FlareSolverr

They run inside Arrmada's container and start and stop with it: qBittorrent as the same
user (PUID/PGID), FlareSolverr as a user of its own that can't read Arrmada's appdata (its
browser opens tracker pages). The container log shows their output with `[qbittorrent]` and `[flaresolverr]` in front
(Docker tab → Arrmada's icon → **Logs**); `[aio]` lines are the part that starts them.

- **qBittorrent** shows up in **Settings → Connections** as *qBittorrent (bundled)*.
  Arrmada sets its incoming port, its save folder and its queue limits, and files each
  download under its own categories. Its settings and torrent list live in appdata
  (`qbittorrent/`), so they survive updates.
- **qBittorrent's own page** is on the qBittorrent web UI port (8091:
  `http://<server>:8091`). Its login was made by Arrmada on first start: open the
  qBittorrent card in Settings → Connections and choose **Show its login** (admins only).
  Nobody gets in without it, Arrmada included: it signs in with the same login. If you
  change the password in qBittorrent's own settings, enter the new one on the qBittorrent
  card too (**Edit**), or Arrmada can't reach it.
- **FlareSolverr** answers only inside the container, and Arrmada's FlareSolverr setting
  points at it unless you set your own. It's used for trackers behind Cloudflare (TorrentLeech,
  1337x) and TheXEM.

Unraid servers are x86-64, which has both. (The all-in-one image's 64-bit ARM build has no
FlareSolverr, because the Chromium it needs is x86-64 only; it says so in the log.)

## The built-in SABnzbd and Prowlarr

The container also has a **SABnzbd** (for Usenet) and a **Prowlarr** (one place to keep
your indexers), the same ones the Compose install adds with `--with-sabnzbd` and
`--with-prowlarr`. They're off unless you switch them on: edit Arrmada, switch on **Show
more settings**, set **Built-in SABnzbd** and/or **Built-in Prowlarr** to `true`, and
**Apply**. (A blank field counts as off.) Like qBittorrent, they run as PUID/PGID, keep their
settings in appdata (`sabnzbd/` and `prowlarr/`), and show up in the container log with
`[sabnzbd]` and `[prowlarr]` in front.

**SABnzbd** needs your own Usenet provider (a paid account) and Newznab indexers;
without both it has nothing to download.

- Arrmada connects to it by itself within a minute: it shows up in **Settings →
  Connections** as *SABnzbd (bundled)*, with Arrmada's categories already in it.
- Add your provider on SABnzbd's own page, `http://<server>:8085` (its card links there)
  → **Config → Servers**. It asks for a login: choose **Show its login** on the SABnzbd
  card (admins only).
- Its downloads go to `usenet/incomplete` and `usenet/complete` in your Downloads folder,
  on your media share, so a finished download is moved into the library on the same drive
  rather than copied. Leave the folders SABnzbd shows as they are; Arrmada moves them with
  your Downloads folder.
- Open its page by the server's IP address. By name (`http://tower:8085`) it may say
  "hostname verification failed" until you add that name in SABnzbd → Config → Special →
  `host_whitelist`. The only name it starts with is the container's own, which Unraid
  changes whenever it recreates the container (an update, or an edit in the Docker tab), so
  use the IP, or add the name you use there.
- Switched back to `false`, it stops, and its client in Settings → Connections is switched
  off with it, so nothing new is sent there. Its settings and queue stay in appdata: set it
  to `true` again and both come back.

**Prowlarr** is set up in its own page, `http://<server>:9697`:

1. On the first visit it asks you to choose a login for it. Do that straight after
   switching it on: until you have, its page is open to anyone on your network (as with
   the Compose install's Prowlarr).
2. Add your indexers there (**Indexers → Add Indexer**).
3. Copy its API key (**Settings → General**), then in Arrmada open **Settings →
   Connections → Prowlarr**: the address is already filled in. Paste the key and choose
   **Sync now**. From then on Arrmada syncs every hour.

Arrmada treats it as its own, so it gives it the built-in FlareSolverr for trackers behind
Cloudflare. Switched back to `false`, it stops; its settings stay in appdata. Arrmada,
though, keeps what it had: the saved address (`http://127.0.0.1:9696`) and key in Settings →
Connections → Prowlarr, and the indexers it synced (named `Prowlarr · …`). The hourly sync
then fails each time, and those indexers fail and back off. So when you switch it off for good, switch off
or delete those indexers in Settings → Connections, and point the Prowlarr card at your own
Prowlarr (its address and API key, then **Sync now**) if you use one. If you'd rather keep
your own Prowlarr from the start, leave the built-in one off and put your Prowlarr's
address in the same place in Arrmada.

Both are in the ARM build too. They make the image about 220 MB larger on disk (about
85 MB more to download), whether or not you switch them on.

## Using your own download client or FlareSolverr

Edit Arrmada, switch on **Show more settings**, and set **Built-in qBittorrent** (or
**Built-in FlareSolverr**) to `false`. After **Apply** it doesn't start, and:

- **qBittorrent off:** its client in Settings → Connections is switched off (not deleted),
  so nothing new is sent to it. Its settings and torrents stay in appdata: set it back to
  `true` and it picks up where it left off, client switched back on. You can remove the two
  torrent port entries if you like. Then add your own client (next section).
- **FlareSolverr off:** put your own FlareSolverr's address in **FlareSolverr URL**, or in
  Settings → Connections → FlareSolverr, for example `http://192.168.1.10:8191`.

Want torrents to go through a VPN? Switch the built-in qBittorrent off and run your own
behind Gluetun: [Downloading through a VPN](vpn.md#the-unraid-all-in-one-container).

The built-in SABnzbd and Prowlarr are off already unless you switched them on; your own
are added the same way as below.

### Connecting your own download client

**Give the client the same share at the same path.** Edit the qBittorrent (or SABnzbd)
container and add a path: container path `/storage`, host path `/mnt/user/data`. Set its
download folder inside it (`/storage/torrents`). Now both containers name a file the same
way, and imports hardlink.

If you'd rather keep the client's own layout (the common `/downloads` → `/mnt/user/data/torrents`),
that works too: add a **path mapping** on the client's card in Arrmada (remote `/downloads`,
local `/storage/torrents`). The card suggests it when it sees a path it can't reach.

Then in Arrmada, **Settings → Connections → + Add client**:

- **qBittorrent:** the address of its web UI as Arrmada's container reaches it. On Unraid's
  default bridge network that's the server's IP and the port, `http://192.168.1.10:8080`
  (container names only work between containers on the same custom network). Then
  qBittorrent's web UI username and password.
- **SABnzbd:** its address and its **full API key** (SABnzbd → Config → General). See
  [Usenet](configuration.md#usenet-sabnzbd) for categories and indexers. SABnzbd works next
  to the built-in qBittorrent too: you don't have to switch that off to add it.

Arrmada sends each download to the Downloads folder you picked, and creates its own
categories (`arrmada`, `arrmada-tv`, `arrmada-books`, `arrmada-music`).

**Prowlarr:** Settings → Connections → Prowlarr, with its address and API key (Prowlarr →
Settings → General), then **Sync now**.

## GPU (Intel or AMD)

Convert and Whisper subtitles use Intel or AMD graphics through `/dev/dri`. Check that the
server has it: `ls /dev/dri` in the Unraid terminal should list `renderD128`. Then edit
Arrmada, switch on **Advanced View** (top right), and add this to **Extra Parameters**,
after what's already there:

```text
--device=/dev/dri
```

The template doesn't add the device by itself: on a server without it, Docker would refuse
to start the container. `docker exec Arrmada /usr/lib/jellyfin-ffmpeg/vainfo` shows what the
container can see. Intel and AMD cards are both recognised; [GPU](gpu.md#intel-and-amd-graphics)
has the details.

An NVIDIA card can do Convert's encoding (NVENC) instead, through the Nvidia-Driver plugin
and `--runtime=nvidia`; AI subtitles stay on the CPU there. [GPU](gpu.md#nvidia-on-unraid)
has the steps, and how to see which encoders Arrmada found.

## Updating

The template runs `ghcr.io/tristenlammi/arrmada:stable-aio`, the latest published release
with qBittorrent and FlareSolverr built in (and the optional SABnzbd and Prowlarr, from the
release whose changelog lists them). When a new one is out, Arrmada's **Settings →
Status → Updates** says so, and Unraid's Docker tab shows **update ready** after **Check for
Updates** (it can take a few minutes longer than the Updates card, while the all-in-one
image is built). Click it (or **Update All**) to pull the new image; the CA Auto Update
plugin can do it on a schedule. Torrents keep their state across an update: qBittorrent
saves it when the container stops.

Before an update changes the database, Arrmada copies it to `backups/` in its appdata, and it
takes a nightly backup too ([Backups](backups.md)). To go back a version, edit the container
and set **Repository** to that release's all-in-one tag, for example
`ghcr.io/tristenlammi/arrmada:0.3.0-aio` (releases before the all-in-one image only have the
plain tag, `ghcr.io/tristenlammi/arrmada:0.2.0`, which has no qBittorrent inside). An older
version won't start on a database a newer one has already upgraded: restore the backup from
before the update first ([Backups](backups.md)).

**Installed from an earlier version of this template?** It ran `:stable`, the app on its own,
and keeps doing so: Unraid never changes an installed container's template. Nothing is
wrong with that. To switch to the built-in qBittorrent and FlareSolverr, edit Arrmada, set
**Repository** to `ghcr.io/tristenlammi/arrmada:stable-aio`, and add the two torrent ports
(`56881`, TCP and UDP); or keep your own client and set **Built-in qBittorrent** to `false`.

**Installed before the built-in SABnzbd and Prowlarr?** Your container keeps the template it
was installed with, so it has no switches for them; the update itself changes nothing, and
both stay off. To switch one on, edit Arrmada and use **Add another Path, Port, Variable,
Label or Device** for each line:

| Type | Name | Container side | Value |
|---|---|---|---|
| Variable | Built-in SABnzbd | Key `ARRMADA_BUNDLE_SABNZBD` | `true` |
| Port | SABnzbd web UI port | Container port `8085` (TCP) | Host port `8085` |
| Variable | SABnzbd web UI host port | Key `ARRMADA_SABNZBD_HOST_PORT` | `8085` |
| Variable | Built-in Prowlarr | Key `ARRMADA_BUNDLE_PROWLARR` | `true` |
| Port | Prowlarr web UI port | Container port `9696` (TCP) | Host port `9697` |
| Variable | Prowlarr web UI host port | Key `ARRMADA_PROWLARR_HOST_PORT` | `9697` |

## Troubleshooting

- **The Open Prowlarr or SABnzbd link goes to the wrong port.** Arrmada can't see the port you
  published, only what the template tells it. Make the **web UI port** and the **web UI host
  port** settings the same number, for example both 9696 for Prowlarr, then apply. For Prowlarr
  you can instead save the right address under Settings, Connections, Prowlarr.
- **The container won't start: "port is already allocated".** Something else has that host
  port. Edit Arrmada and change it ([A port that's taken](#install-from-community-apps)).
  That includes the SABnzbd (8085) and Prowlarr (9697) ports, which are taken even while
  those two are switched off.
- **SABnzbd's page says "hostname verification failed".** Open it by the server's IP
  address, or add the name you use to SABnzbd → Config → Special → `host_whitelist`.
- **SABnzbd or Prowlarr doesn't start.** The container log says why on its `[sabnzbd]` or
  `[prowlarr]` lines; `[aio]` says which built-in apps are switched on. A value other than
  `true` or `false` in their switches leaves them off, and the log says so.
- **Torrents download but hardly upload.** The torrent port isn't reachable from outside:
  forward it (TCP and UDP) on your router to the Unraid server, and check the number is the
  same in all three places.
- **"permission denied" on a library folder.** One disk's copy of a share folder is owned by
  root. Fix it through the share path: `chown -R 99:100` and `chmod -R u+rwX,g+rwX` on the
  library's parent folders.
- **Downloads finish but never import** (your own client). Arrmada can't see where the client
  saved them. The client's card in Settings → Connections names the path; give the client
  `/storage` too, or add the path mapping it suggests.
- **qBittorrent's page doesn't take the login.** If you changed the password in qBittorrent,
  use yours. If it's lost: stop Arrmada, delete `qbittorrent/qBittorrent/config/qBittorrent.conf`
  in its appdata, and start it again. A new config and login are made, and Arrmada uses the
  new login by itself; the torrent list (`qbittorrent/qBittorrent/data`) is kept, but
  qBittorrent's other settings go back to Arrmada's defaults.
- **The qBittorrent card says "invalid username or password".** The password was changed in
  qBittorrent: enter it on the card (**Edit**).
- More in [Troubleshooting](troubleshooting.md).
