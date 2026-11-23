# GPU acceleration

Two things in Arrmada can use a graphics card:

- **Convert** encodes on the GPU instead of the CPU: many times faster, though files come out
  somewhat bigger for the same quality. Intel and AMD graphics work through VAAPI (and Intel
  through Quick Sync too); NVIDIA cards through NVENC. HDR10 and HLG files go to the GPU
  only when it proved at start that it keeps their HDR (below); HDR10+ and Dolby Vision
  always encode on the CPU.
- **AI subtitles** (Whisper) run on Intel graphics through Intel's oneAPI build, and on
  Intel or AMD graphics through Vulkan. They fall back to the CPU by themselves.

All of this is on 64-bit Intel/AMD (x86-64) servers only. The ARM image has no GPU
encoders and no GPU subtitle build: Convert and Whisper use the CPU there, whatever is
passed in ([Install → ARM](install.md#arm)).

## How Arrmada picks an encoder

The image's ffmpeg (jellyfin-ffmpeg) has the VAAPI, Quick Sync and NVENC encoders built in.
At every start, Arrmada looks for each one's device (`/dev/dri` with an Intel or AMD card
behind it; `/dev/nvidia0` or `nvidia-smi` for NVIDIA) and then **encodes one test frame**
with it. Only an encoder whose test frame came out is ever used: one that's compiled in, or
whose device is there, but doesn't actually run is left out before any file is touched.
With **Use my GPU** on, the first working one in this order is used: NVENC, VAAPI, Quick
Sync, for HEVC and for AV1 alike (AV1 only when that encoder exists on the card; otherwise
GPU mode converts to HEVC). With it off, both encode on the CPU (x265, SVT-AV1). If a
hardware encoder fails on real files again and again, Convert carries on on the CPU for the
rest of that run.

### HDR on the GPU

Each hardware encoder that writes 10-bit then encodes a tiny HDR10 clip and a tiny HLG clip,
with the same settings a real conversion uses, and the result is read back: the BT.2020
colour tags, the PQ or HLG curve, and for HDR10 the mastering display and light level
(MaxCLL). Only an encoder whose clip came back with all of it takes files of that kind; the
others leave HDR to the CPU as before. As far as ffmpeg's own code goes, Quick Sync (HEVC and
AV1) and VAAPI (HEVC and AV1) can do it, and so can NVENC HEVC in the image's
jellyfin-ffmpeg; NVENC AV1 can't. The test on your card is what decides. HDR10+ and Dolby
Vision always encode on the CPU. Every converted HDR file is checked again before it
replaces the original, so a GPU that loses the HDR on a real film leaves the original alone,
and that kind of HDR goes to the CPU for the rest of the run.

### Seeing what it found

- **Settings → Convert**: **Use my GPU** only appears when at least one hardware encoder passed
  its test. Turn it on to use it (it's off by default). Its note says when the GPU has no AV1
  encoder. With more than one render device (an iGPU and a card), **Advanced → GPU device**
  picks the one VAAPI uses; the discrete card is usually the higher `renderD` number.
- **Convert → Overview** says what conversions run on, for example
  `Converts on VAAPI (HEVC) · HDR10+ and Dolby Vision on the CPU`,
  `Converts on VAAPI (HEVC) · HDR on the CPU`, or `CPU (x265)`. The **Use my GPU** note says
  which kinds of HDR the GPU takes.
- **Logs** (admins): at each start, `convert: encoders verified` lists the encoders that work
  and, under `gpu_hdr`, which HDR each GPU encoder keeps.
- Every encoder, with the reason one was turned down, is in
  `http://<server>:7878/api/v1/convert/hardware` (open it while signed in as an admin or
  manager): `available: false` with a `note` is the error its test frame gave; `hdr10` and
  `hlg` say whether it passed the HDR test, and `hdr_note` why not.
- **Settings → Subtitles → Local AI** says where the last AI subtitle ran: on the GPU (Intel
  oneAPI), on the GPU (Vulkan), or on the CPU. When the oneAPI build was set aside it says
  why. **Subtitles → Logs** names the device each AI subtitle ran on, and how many times
  faster than real time it went.

## Intel and AMD graphics

The card is passed in as `/dev/dri`:

- **The installer** does it for you on x86-64 when the server has `/dev/dri` (it writes
  `devices: - /dev/dri:/dev/dri` into `docker-compose.override.yml`).
- **The plain Compose file** ([docker-compose.example.yml](../docker-compose.example.yml)):
  uncomment `devices: [/dev/dri:/dev/dri]` under `arrmada-app`.
- **Unraid's template**: [Unraid → GPU](unraid.md#gpu-intel-or-amd).

To check, on the host `ls -l /dev/dri` should list a `renderD128` (one per card), and inside
the container:

```sh
docker exec Arrmada-app ls -l /dev/dri
docker exec Arrmada-app /usr/lib/jellyfin-ffmpeg/vainfo
```

`vainfo` lists the encode profiles the card offers (`VAEntrypointEncSlice` lines). Use the
one under `/usr/lib/jellyfin-ffmpeg/`: it's the one that goes with the image's ffmpeg and its
drivers. For a second card add `--display drm --device /dev/dri/renderD129`.

**The render group.** The device on the host usually belongs to a `render` or `video` group,
and you don't need to add Arrmada to it: the container starts as root and makes its own
copies of the render nodes (the ones under the container's `/dev/dri`) readable and
writable by everyone, then drops to the user it runs as (`ARRMADA_PUID`). The nodes on the
host aren't changed. (Adding a group with Compose's `group_add` wouldn't help either: that
drop sets the user's groups afresh.) Where the container can't change them, rootless Docker
for example, `vainfo` says "permission denied"; then let everyone use the node on the host
(`sudo chmod 666 /dev/dri/renderD128`, until the next reboot; a udev rule keeps it).

**Intel.** Gen 9 (Skylake) and later encode HEVC; Arc and Core Ultra graphics also encode
AV1. Both VAAPI and Quick Sync are tried, and VAAPI is preferred where both work (Quick Sync
fails to start on some Arc setups where VAAPI is fine).

**AMD.** Nothing extra to set: Arrmada reads each card's vendor and gives ffmpeg the
matching VA driver, AMD's (`radeonsi`) for an AMD card and Intel's (`iHD`) for an Intel one,
both in the image. With an Intel iGPU and an AMD card in one server, the card picked under
**Advanced → GPU device** decides. (Quick Sync is Intel-only; AV1 encoding needs an RX
7000-series card or newer.) This hasn't been tested on AMD hardware by the project: the test
frame and the Logs line tell you whether it works.

**Choosing the driver yourself.** `LIBVA_DRIVER_NAME` set on the container overrides that
choice for every run, for a card the automatic choice gets wrong:

```yaml
services:
  arrmada-app:
    environment:
      LIBVA_DRIVER_NAME: radeonsi   # or iHD
```

Images before this change set `LIBVA_DRIVER_NAME=iHD` themselves, so on those an AMD card
needs exactly this line.

## NVIDIA

Convert can use NVENC, which the image's ffmpeg has built in. The libraries that drive the
card come from the host's NVIDIA driver, and the **NVIDIA Container Toolkit** puts them into
the container. NVENC hasn't been tested on NVIDIA hardware by the project; the test frame at
start-up is what decides, and the places above show the result.

1. Install the NVIDIA driver and the
   [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)
   on the host, then `sudo nvidia-ctk runtime configure --runtime=docker` and restart Docker.
   `nvidia-smi` on the host should list the card.
2. Give the card to `arrmada-app`, with the driver's video (NVENC), compute and utility
   parts. Put this in `docker-compose.override.yml` in Arrmada's folder (Compose merges it
   into `docker-compose.yml`; with the plain Compose file, add the same lines to it):

   ```yaml
   services:
     arrmada-app:
       deploy:
         resources:
           reservations:
             devices:
               - driver: nvidia
                 count: 1          # or: device_ids: ["0"]
                 capabilities: [gpu]
       environment:
         NVIDIA_DRIVER_CAPABILITIES: compute,video,utility
   ```

   The older form works too: `runtime: nvidia` under `arrmada-app`, with
   `NVIDIA_VISIBLE_DEVICES: all` beside `NVIDIA_DRIVER_CAPABILITIES` in its environment.
   Keep `utility`: it brings `nvidia-smi`, which is one of the ways Arrmada spots the card.
3. Run `sh update.sh` (or `docker compose up -d`), then check:

   ```sh
   docker exec Arrmada-app nvidia-smi
   ```

   After the next start, **Use my GPU** appears in Settings → Convert and the Logs line
   lists `hevc_nvenc` (and `av1_nvenc` on an RTX 40-series card or newer). If the API above
   shows NVENC with a note like "Driver does not support the required nvenc API version",
   the host's driver is older than the image's ffmpeg needs: update the driver.

If the installer wrote `docker-compose.override.yml` (its first line says
`# Auto-generated by install.sh`), add your lines and delete that first line. The installer
then leaves the file to you; while the line is there, a later `install.sh --media` or
`--transcode` writes the file afresh without your lines.

Only one NVIDIA card is used, the first one the container sees: pick it with `device_ids` (or
`NVIDIA_VISIBLE_DEVICES`). **GPU device** in Settings → Convert is for VAAPI only. NVENC
encodes on the card, but the original is decoded on the CPU.

**AI subtitles on NVIDIA** are not supported as such. The Vulkan build of Whisper could in
principle use an NVIDIA card, but that needs the driver's Vulkan part in the container too
(`graphics` added to `NVIDIA_DRIVER_CAPABILITIES`), and it hasn't been tested. If you try it,
`docker exec Arrmada-app vulkaninfo --summary` should list the card, and Settings → Subtitles
then says "Running on the GPU (Vulkan)" after the next AI subtitle. Otherwise Whisper runs on
the CPU, which works, just slowly.

### NVIDIA on Unraid

1. Install **Nvidia-Driver** from Community Apps (Apps → search "Nvidia Driver") and reboot.
   Its page under Settings shows your card and its GPU UUID (`GPU-…`).
2. Edit Arrmada, switch on **Advanced View** (top right), and add to **Extra Parameters**:

   ```text
   --runtime=nvidia
   ```

3. Add two Variables (**Add another Path, Port, Variable, Label or Device** → Variable):
   `NVIDIA_VISIBLE_DEVICES` = the GPU UUID (or `all`), and
   `NVIDIA_DRIVER_CAPABILITIES` = `compute,video,utility`.
4. Apply, then check as above (`docker exec Arrmada nvidia-smi`; the template's container is
   called `Arrmada`).

The Compose install on Unraid takes the `runtime: nvidia` form above, in its
`docker-compose.override.yml`.

## AI subtitles

The image carries three Whisper builds, and the app picks by itself:

1. **Intel oneAPI (SYCL)**, tried first. It runs the whole model on an Intel GPU through Level
   Zero and is the one that uses Arc's matrix units. On a server without an Intel GPU it
   finds no device, and the app sets it aside for good (until the next start) and says why.
2. **Vulkan**, the fallback: any GPU with a Vulkan driver in the container. The image has
   Mesa's drivers for Intel and AMD, used through the same `/dev/dri`.
   `docker exec Arrmada-app vulkaninfo --summary` shows what it can see.
3. **CPU**: the Vulkan build runs on the CPU by itself when it finds no device.

So for Intel and AMD, passing `/dev/dri` (above) is all it takes. On ARM, only the CPU build
is there, and Settings says so; on a small board like a Raspberry Pi an AI subtitle can take
longer than the film, so subtitles from OpenSubtitles or the file itself are the better
source there.
