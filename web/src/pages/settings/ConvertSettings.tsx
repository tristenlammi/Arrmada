import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { SavedTick } from "../../components/settings/auto";
import { useScrollWhenLoaded } from "../../components/settings/ui";
import { api, type ConvertDevice, type ConvertHoldCheck, type ConvertLibraryFolder, type ConvertSettings as Settings } from "../../lib/api";
import { convertApi } from "../../lib/convertApi";
import { backlogLine } from "../../lib/convertForecast";
import { DevicesPanel, transcodedSentence, transcodingDevices, useConvertDevices } from "../../components/convert/DevicesPanel";
import { fmtBytes } from "../../lib/disposal";
import { LINKS } from "../../lib/links";
import { langName, useLanguages } from "../../lib/languages";
import { SAME_AS_SUBTITLES } from "../../lib/convertKeepSubs";
import { useAutoSave, type AutoSave } from "../../lib/useAutoSave";
import { SwitchOnDialog } from "../convert/ConfirmConvert";
import { setExclusion, useConvertExclusions, type ExclusionTarget } from "../../components/convert/ConvertExclusion";
import { Button, useConfirm, useToast } from "../../ui";

// Settings → Convert: when Convert runs, what it converts to, which
// tracks it keeps, and where it works. It used to be the Convert page's Settings tab; that
// page now links here (Configure →) and reads these through its own GET. Each control saves
// itself — switches and choices at once, text when you leave the field — except the
// transcode folder, which is a path and saves only when you confirm it.
export const CONVERT_SETTINGS_ID = "convert";

const inp = "rounded-lg px-2.5 py-1.5 text-sm";
const inpStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;
const errStyle = { ...inpStyle, border: "1px solid var(--reject)" } as const;
const cardStyle = { border: "1px solid var(--line)", background: "var(--panel)" } as const;
const accentBtn = { background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" } as const;

export function ConvertSettings() {
  const [s, setS] = useState<Settings | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { convertApi.convertSettings().then(setS).catch((e: Error) => setError(e.message)); }, []);
  useScrollWhenLoaded(CONVERT_SETTINGS_ID, !!s);
  // Each save sends just its own keys and takes back just those, so two saves answering out
  // of order can't put an older value back.
  const commit = useCallback(async (p: Partial<Settings>) => {
    const next = await convertApi.updateConvertSettings(p);
    setS((cur) => {
      if (!cur) return next;
      const merged = { ...cur } as Record<string, unknown>;
      for (const k of Object.keys(p)) merged[k] = (next as unknown as Record<string, unknown>)[k];
      return merged as unknown as Settings;
    });
  }, []);
  return (
    <div id={CONVERT_SETTINGS_ID} className="flex scroll-mt-20 flex-col gap-4">
      <h2 className="m-0 text-lg font-bold">Convert</h2>
      {s ? <ConvertForm s={s} commit={commit} /> : <p className="m-0 text-base text-ink-dim">{error ?? "Loading…"}</p>}
    </div>
  );
}

function minutesApart(a: string | undefined, b: string): number {
  const mins = (t?: string) => { const m = /^(\d{1,2}):(\d{2})$/.exec(t ?? ""); return m ? +m[1] * 60 + +m[2] : null; };
  const x = mins(a), y = mins(b);
  if (x === null || y === null) return 0;
  const d = Math.abs(x - y);
  return Math.min(d, 1440 - d);
}

type Commit = (p: Partial<Settings>) => Promise<void>;
type Hours = { hours_start: string; hours_end: string };

function ConvertForm({ s, commit }: { s: Settings; commit: Commit }) {
  // One hook per control: each shows its own Saving… / Saved ✓ / error.
  const put = <K extends keyof Settings>(k: K) => (v: Settings[K]) => commit({ [k]: v } as Partial<Settings>);
  const auto = useAutoSave(s.auto, put("auto"));
  // Switching on asks first, with what it will do across the library; off saves at once.
  const [askSwitchOn, setAskSwitchOn] = useState(false);
  const autoSwitch: AutoSave<boolean> = {
    ...auto,
    commit: async (on) => {
      if (on === true && !auto.value) { setAskSwitchOn(true); return false; }
      return auto.commit(on);
    },
  };
  const hours = useAutoSave<Hours>({ hours_start: s.hours_start, hours_end: s.hours_end }, (v) => commit(v), {
    equal: (a, b) => a.hours_start === b.hours_start && a.hours_end === b.hours_end,
    validate: (v) => ((v.hours_start === "") !== (v.hours_end === "") ? "Set both a start and an end time." : null),
  });
  const pause = useAutoSave(s.pause_watching, put("pause_watching"));
  const av1Save = useAutoSave(s.allow_av1, put("allow_av1"));
  const devices = useConvertDevices();
  const confirm = useConfirm();
  // Turning AV1 on asks first when a device seen lately had AV1 transcoded: the plays are
  // the evidence, not the owner's memory of every TV in the house.
  const av1: AutoSave<boolean> = {
    ...av1Save,
    commit: async (v) => {
      const on = v ?? av1Save.value;
      const risky = transcodingDevices(devices, "av1");
      if (on && !s.allow_av1 && risky.length > 0) {
        const ok = await confirm({
          title: "Allow AV1?",
          body: transcodedSentence(risky, "av1"),
          confirmLabel: "Allow AV1 anyway",
        });
        if (!ok) return false;
      }
      return av1Save.commit(v);
    },
  };
  const gpu = useAutoSave(s.use_gpu, put("use_gpu"));
  const crop = useAutoSave(s.crop, put("crop"));
  const tenBit = useAutoSave(s.ten_bit ?? "always", put("ten_bit"), { keepOnError: false });
  const dolbyVision = useAutoSave(s.dolby_vision, put("dolby_vision"), { keepOnError: false });
  // How many files the Dolby Vision choice applies to, from the library stats.
  const [dvFiles, setDvFiles] = useState<number | null>(null);
  useEffect(() => { convertApi.convertStats().then((st) => setDvFiles(st.total.dolby_vision_files ?? 0)).catch(() => {}); }, []);
  const audioLangs = useAutoSave(s.keep_audio_langs, put("keep_audio_langs"));
  const origLang = useAutoSave(s.keep_original_lang, put("keep_original_lang"));
  const commentary = useAutoSave(s.drop_commentary, put("drop_commentary"));
  const subLangs = useAutoSave(s.keep_sub_langs, put("keep_sub_langs"));
  const imageSubs = useAutoSave(s.image_subs, put("image_subs"), { keepOnError: false });
  const tidy = useAutoSave(s.tidy_tracks, put("tidy_tracks"));
  const scratch = useAutoSave(s.scratch_dir, put("scratch_dir"), { equal: (a, b) => a.trim() === b.trim() });
  const vaapi = useAutoSave(s.vaapi_device || "", put("vaapi_device"), { keepOnError: false });
  const cores = useAutoSave(String(s.cpu_cores), (v) => commit({ cpu_cores: Number(v) }), {
    validate: (v) => (/^\d{1,3}$/.test(v.trim()) && Number(v) <= 256 ? null : "CPU cores needs a number from 0 to 256."),
  });
  const workers = useAutoSave(String(s.workers), (v) => commit({ workers: Number(v) }), {
    validate: (v) => (/^[1-4]$/.test(v.trim()) ? null : "Conversions at once must be 1–4."),
  });
  const resumable = useAutoSave(s.resumable ?? false, put("resumable"));
  const scanAt = useAutoSave(s.scan_at || "03:00", put("scan_at"), {
    validate: (v) => (/^\d{1,2}:\d{2}$/.test(v.trim()) ? null : "Pick a time like 03:00."),
  });
  const holdDays = useAutoSave(String(s.hold_days), (v) => commit({ hold_days: Number(v) }), {
    validate: (v) => (/^\d{1,3}$/.test(v.trim()) && Number(v) <= 365 ? null : "Keep originals for 0 to 365 days."),
  });
  const sameList = (a: string[], b: string[]) => a.length === b.length && a.every((x) => b.includes(x));
  const skipRes = useAutoSave<string[]>(s.skip_resolutions ?? [], put("skip_resolutions"), { equal: sameList, keepOnError: false });
  const skipFolders = useAutoSave<string[]>(s.skip_folders ?? [], put("skip_folders"), { equal: sameList, keepOnError: false });
  const holdMax = useAutoSave(String(s.hold_max_gb), (v) => commit({ hold_max_gb: Number(v) }), {
    validate: (v) => (/^\d{1,7}$/.test(v.trim()) ? null : "The hold's size is a number of GB (0 = no limit)."),
  });

  const [hw, setHw] = useState<{ dir: string; free: number; need: number; needTitle: string; devices: { path: string; pci: string; vendor: string }[]; holdCheck: ConvertHoldCheck[] } | null>(null);
  const loadHw = useCallback(() => {
    convertApi.convertHardware().then((h) => setHw({ dir: h.scratch_dir, free: h.scratch_free_bytes, need: h.scratch_need_bytes ?? 0, needTitle: h.scratch_need_title ?? "", devices: h.render_devices ?? [], holdCheck: h.hold_check ?? [] })).catch(() => {});
  }, []);
  useEffect(loadHw, [loadHw]);
  // The backlog's encoding time, so the hours show what they mean before they're saved.
  const [backlogSec, setBacklogSec] = useState(0);
  useEffect(() => { convertApi.convertStatus().then((st) => setBacklogSec(st.backlog_sec ?? 0)).catch(() => {}); }, []);
  // A new transcode folder changes the free space shown under it.
  useEffect(() => { if (scratch.status === "saved") loadHw(); }, [scratch.status, loadHw]);

  const scheduled = !!(hours.value.hours_start || hours.value.hours_end);
  // The switch is the hours pair seen as on/off: on starts with 01:00–07:00, off clears both.
  const scheduleSwitch: AutoSave<boolean> = {
    ...hours,
    value: scheduled,
    set: () => {},
    commit: (on) => hours.commit(on ? { hours_start: "01:00", hours_end: "07:00" } : { hours_start: "", hours_end: "" }),
  };
  const browserTime = new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  const clockSkewed = minutesApart(s.server_time, browserTime) > 5;
  const textKeys = (save: AutoSave<string>) => (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") { e.preventDefault(); void save.commit(); }
    if (e.key === "Escape" && save.dirty) { e.preventDefault(); save.revert(); }
  };
  const textProps = (save: AutoSave<string>) => ({
    value: save.value,
    onChange: (e: React.ChangeEvent<HTMLInputElement>) => save.set(e.target.value),
    onBlur: () => void save.commit(),
    onKeyDown: textKeys(save),
    "aria-invalid": save.status === "error" || undefined,
    style: save.status === "error" ? errStyle : inpStyle,
  });

  // What switching on would write, against what the players did lately (CONV-25): HEVC
  // always, 10-bit when every re-encode is, AV1 when it's allowed.
  const switchOnWarnings = [
    transcodedSentence(transcodingDevices(devices, "hevc"), "hevc"),
    tenBit.value === "always" ? transcodedSentence(transcodingDevices(devices, "hevc10"), "hevc10") : "",
    s.allow_av1 ? transcodedSentence(transcodingDevices(devices, "av1"), "av1") : "",
  ].filter(Boolean);

  return (
    <div className="flex flex-col gap-4">
      <Section title="Automatic conversion">
        {askSwitchOn && (
          <SwitchOnDialog onCancel={() => setAskSwitchOn(false)} deviceWarnings={switchOnWarnings} switchOn={async () => {
            if (!(await auto.commit(true))) throw new Error("Couldn't switch it on — try again in a moment.");
            setAskSwitchOn(false);
          }} />
        )}
        <Toggle save={autoSwitch} label="Convert my library automatically"
          hint="Works through everything that's worth converting, biggest savings first, one file at a time. Files you convert from the Library start within seconds either way, after any other file you asked for — an automatic conversion in progress waits for them." />
        <Toggle
          save={scheduleSwitch}
          label="Only during set hours" hint={`A conversion still running when the hours end is paused and picks up again next time${s.can_pause ? "" : " (pausing needs the Linux container — elsewhere it finishes first)"}.`} />
        {scheduled && (
          <div className="ml-6 flex flex-col gap-1.5">
            <span className="flex flex-wrap items-center gap-1.5 text-sm">
              <input type="time" aria-label="Start" value={hours.value.hours_start} onChange={(e) => hours.set({ ...hours.value, hours_start: e.target.value })} onBlur={() => void hours.commit()} className={inp} style={hours.status === "error" ? errStyle : inpStyle} />
              <span className="text-ink-faint">to</span>
              <input type="time" aria-label="End" value={hours.value.hours_end} onChange={(e) => hours.set({ ...hours.value, hours_end: e.target.value })} onBlur={() => void hours.commit()} className={inp} style={hours.status === "error" ? errStyle : inpStyle} />
            </span>
            <span className={`text-xs ${clockSkewed ? "" : "text-ink-faint"}`} style={clockSkewed ? { color: "var(--avoid-text)" } : undefined}>
              {clockSkewed ? "⚠ " : ""}Read on the server's clock: {s.server_time} {s.server_tz}{clockSkewed ? ` — yours says ${browserTime}. Set TZ in your .env and recreate the container (docker compose up -d).` : "."}
            </span>
            {backlogSec > 0 && hours.value.hours_start && hours.value.hours_end && (
              <span className="text-xs text-ink-dim">
                At these hours: {backlogLine(backlogSec, hours.value.hours_start, hours.value.hours_end)} — at this machine's pace so far.
              </span>
            )}
          </div>
        )}
        <Toggle save={pause} label="Pause while someone is watching"
          hint={s.plex_watching_known ? "Someone playing something on Plex or Jellyfin: nothing new starts, and a running conversion is frozen until the stream stops." : <>Needs watch monitoring turned on for your media server (<Link to={LINKS.mediaServers} className="font-semibold" style={{ color: "var(--accent-text)" }}>Settings → Media servers</Link>) — until then this does nothing.</>} />
      </Section>

      <Section title="Format" desc="HEVC by default: the best quality for the size. Whether your own players take each format as it is shows under Your devices below. HDR10, HDR10+ and HLG are kept.">
        <div role="radiogroup" aria-label="Dolby Vision files">
          <div className="flex flex-wrap items-center gap-2 text-sm font-semibold">
            Dolby Vision files{dvFiles !== null ? ` (${dvFiles.toLocaleString()} in your library)` : ""} <SavedTick save={dolbyVision} />
          </div>
          <div className="mb-1.5 text-xs text-ink-faint">A conversion keeps the HDR10 (or HLG) picture under the Dolby Vision layer; the layer itself goes, unless you keep it below. Profile 5 files are never converted — they have no standard picture under the layer.</div>
          {([["skip", "Leave them alone — for a Dolby Vision TV (their tracks are still tidied)"], ["drop", "Convert and drop the Dolby Vision layer (keeps HDR10)"]] as const).map(([v, label]) => (
            <label key={v} className="flex cursor-pointer items-center gap-2 py-0.5 text-sm">
              <input type="radio" name="dolby_vision" checked={dolbyVision.value === v} onChange={() => void dolbyVision.commit(v)} />{label}
            </label>
          ))}
          {(s.dovi_tool || dolbyVision.value === "keep") && (
            <label className="flex cursor-pointer items-start gap-2 py-0.5 text-sm">
              <input type="radio" name="dolby_vision" className="mt-0.5" checked={dolbyVision.value === "keep"} onChange={() => void dolbyVision.commit("keep")} />
              <span>
                Convert and keep Dolby Vision (profile 8.1) — preview
                <span className="block text-xs leading-snug text-ink-faint">
                  The Dolby Vision metadata is read from the original and put back on the new picture, frame for frame, then checked before the swap. Profile 7 discs become 8.1 and lose their enhancement layer; profile 8.4 (HLG) files are left alone. Always HEVC, never cropped, encoded in one go, and about 15% bigger than without. Not yet checked on a Dolby Vision TV — convert one film and watch it before letting it loose on the library.
                </span>
              </span>
            </label>
          )}
        </div>
        <Toggle save={av1} label="My devices can play AV1"
          hint={<>Turn on only if every TV, phone and streaming box you watch on decodes AV1 — older Apple TVs, Shields and Fire Sticks don't, and Plex would transcode on the fly. When on, each file gets a quick side-by-side test and goes to AV1 only if it's clearly smaller at the same quality; otherwise HEVC. Files with HDR10+ always stay HEVC.</>} />
        <TenBitChoice save={tenBit} transcoded={transcodingDevices(devices, "hevc10")} />
        {s.has_gpu && (
          <Toggle save={gpu} label="Use my GPU"
            hint={`Many times faster, but files come out somewhat bigger for the same quality. HDR always encodes on the CPU (the GPU can't carry its metadata).${s.gpu_does_av1 ? "" : " Your GPU has no AV1 encoder, so GPU mode converts to HEVC."}`} />
        )}
        <Toggle save={crop} label="Remove black bars"
          hint="Widescreen films are encoded without the black bars above and below (or beside) the picture. The picture and its shape stay exactly the same and your TV letterboxes it as before. Files come out slightly smaller, and the quality check judges the picture alone instead of counting the bars as a perfect match. The film is checked about every 25 seconds (40 to 400 places): only what's black in every one of those frames is removed, and if any of them shows a different shape (IMAX or open-matte scenes) the film keeps its full frame. A wider shot shorter than the gap between checks could still be trimmed, which is why this is off unless you turn it on. Files that keep image subtitles keep their bars too — those subtitles are placed on the full frame." />
        {!s.hdr10plus_tool && <p className="text-xs" style={{ color: "var(--avoid-text)" }}>hdr10plus_tool isn't installed, so HDR10 files in HEVC are left alone (their HDR10+ couldn't be checked).</p>}
        <DevicesPanel data={devices} />
      </Section>

      <Section title="Audio & subtitles" desc="Tracks you don't keep are removed while converting — or on their own, which only copies the file (no re-encode). Audio is never re-encoded: Atmos, TrueHD and DTS-HD pass through untouched.">
        <Field label="Keep audio in" hint="comma-separated, e.g. en, ja · blank = keep every language · the first one becomes the default track" save={audioLangs}>
          <input aria-label="Keep audio in" placeholder="all" className={`${inp} w-[190px]`} {...textProps(audioLangs)} />
        </Field>
        <Toggle save={origLang} label="Also keep the original language"
          hint="A Japanese film keeps its Japanese track even if you only listed English." />
        <Toggle save={commentary} label="Remove commentary tracks" hint="Tracks flagged or titled as commentary." />
        <KeepSubsChoice save={subLangs} input={<input aria-label="Keep subtitles in" placeholder="all" className={`${inp} w-[190px]`} {...textProps(subLangs)} />} />
        <div role="radiogroup" aria-label="Image subtitles">
          <div className="flex flex-wrap items-center gap-2 text-sm font-semibold">Image subtitles (PGS, VobSub) <SavedTick save={imageSubs} /></div>
          <div className="mb-1.5 text-xs text-ink-faint">Plex has to burn these into the picture, which transcodes the video every time they're on.</div>
          {([["keep", "Keep them"], ["when_text", "Remove them when a text version exists (embedded, or an .srt from Subtitles)"], ["remove", "Always remove them — Subtitles can fetch text ones"]] as const).map(([v, label]) => (
            <label key={v} className="flex cursor-pointer items-center gap-2 py-0.5 text-sm">
              <input type="radio" name="image_subs" checked={imageSubs.value === v} onChange={() => void imageSubs.commit(v)} />{label}
            </label>
          ))}
        </div>
        <Toggle save={tidy} label="Tidy tracks even when that's all a file needs"
          hint="Off: a file whose picture is already fine is only rewritten when dropping tracks frees at least 10% (a big foreign-language audio track does). Its other track changes happen when it's re-encoded, or with Fix tracks. On: every file is tidied — which rewrites it end to end, often to shed a few small subtitle tracks." />
        <p className="m-0 text-xs text-ink-faint">Untagged tracks are kept. A language filter that would remove every audio track or every subtitle removes none — the tags are wrong.</p>
      </Section>

      <LeaveAloneSection resolutions={skipRes} folders={skipFolders} libraryFolders={s.library_folders ?? []} />

      <Section title="Undo" desc="A converted file's original isn't deleted straight away: it waits in a hidden folder on the same disk (.arrmada-hold in each library folder, which Plex ignores) so the conversion can be undone. It's never put in the recycle bin, and nothing in the hold is deleted until its time is up or you release it (Convert, History).">
        <Field label="Keep originals for" hint="days · 0 = delete each original once its conversion is verified · applies to conversions from now on" save={holdDays}>
          <span className="flex items-center gap-1.5 text-sm">
            <input aria-label="Keep originals for (days)" inputMode="numeric" className={`${inp} w-[70px]`} {...textProps(holdDays)} onChange={(e) => holdDays.set(e.target.value.replace(/[^0-9]/g, "").slice(0, 3))} />
            <span className="text-ink-faint">days</span>
          </span>
        </Field>
        {holdDays.value.trim() === "0" && <p className="m-0 text-sm" style={{ color: "var(--avoid-text)" }}>Originals are deleted once the conversion is verified — there is no undo.</p>}
        <Field label="Hold up to" hint="GB · when the next original wouldn't fit, conversions pause until one is released — nothing is deleted to make room · 0 = no limit" save={holdMax}>
          <span className="flex items-center gap-1.5 text-sm">
            <input aria-label="Hold up to (GB)" inputMode="numeric" className={`${inp} w-[90px]`} {...textProps(holdMax)} onChange={(e) => holdMax.set(e.target.value.replace(/[^0-9]/g, "").slice(0, 7))} />
            <span className="text-ink-faint">GB</span>
          </span>
        </Field>
        {hw && hw.holdCheck.length > 0 && (
          <div className="flex flex-col gap-1 text-sm">
            {hw.holdCheck.map((c) => (
              <div key={c.root} className="flex flex-wrap items-baseline gap-x-2">
                <span className="font-semibold">{c.label}</span>
                <span className="font-mono text-xs text-ink-faint">{c.root}</span>
                {c.err ? <span style={{ color: "var(--reject-text)" }}>✗ {c.err}</span>
                  : c.instant ? <span style={{ color: "var(--good-text)" }}>instant move ✓</span>
                  : <span style={{ color: "var(--avoid-text)" }}>would copy ⚠ — this folder spans more than one disk, so keeping an original is a full copy</span>}
              </div>
            ))}
          </div>
        )}
      </Section>

      <details className="rounded-xl p-4" style={cardStyle}>
        <summary className="cursor-pointer text-md font-bold">Advanced</summary>
        <div className="mt-3 flex flex-col gap-3.5">
          <Field label="Transcode folder" hint="Fast storage (an SSD/NVMe pool), never the array. Blank = the default. Saved when you press Enter or Use this folder." save={scratch}>
            <span className="flex flex-wrap items-center gap-2">
              <input
                type="text"
                aria-label="Transcode folder"
                value={scratch.value}
                onChange={(e) => scratch.set(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && scratch.dirty) { e.preventDefault(); void scratch.commit(scratch.value.trim()); }
                  if (e.key === "Escape" && scratch.dirty) { e.preventDefault(); scratch.revert(); }
                }}
                placeholder="/transcode"
                className={`${inp} w-[240px]`}
                style={scratch.status === "error" ? errStyle : inpStyle}
              />
              {scratch.dirty && scratch.status !== "saving" && (
                <button onClick={() => void scratch.commit(scratch.value.trim())} className="rounded-lg px-3 py-1.5 text-sm font-semibold" style={accentBtn}>Use this folder</button>
              )}
            </span>
          </Field>
          {/* Green only when the biggest of the next files fits: one 4K remux can need ~90 GB,
              so a fixed threshold said "fine" right before a file failed for space. */}
          {hw && <div className="text-xs text-ink-faint">Using <span className="font-mono text-ink-dim">{hw.dir}</span> · <b style={{ color: hw.free >= hw.need ? "var(--good-text)" : "var(--avoid-text)" }}>{fmtBytes(hw.free)}</b> free{hw.need > 0 && <> · largest of the next 20 files{hw.needTitle ? <> (“{hw.needTitle}”)</> : null} needs ~{fmtBytes(hw.need)}</>}</div>}
          {hw && hw.devices.length > 1 && (
            <Field label="GPU device" hint="The discrete card is usually the higher renderD number" save={vaapi}>
              <select aria-label="GPU device" value={vaapi.value} onChange={(e) => void vaapi.commit(e.target.value)} className={`${inp} w-[280px]`} style={inpStyle}>
                <option value="">Default (renderD128)</option>
                {hw.devices.map((dev) => <option key={dev.path} value={dev.path}>{dev.path.replace("/dev/dri/", "")} · {dev.vendor}{dev.pci ? ` · ${dev.pci}` : ""}</option>)}
              </select>
            </Field>
          )}
          <Field label="CPU cores" hint="How many cores a CPU conversion may use · 0 = half the machine" save={cores}>
            <input aria-label="CPU cores" inputMode="numeric" className={`${inp} w-[80px]`} {...textProps(cores)} onChange={(e) => cores.set(e.target.value.replace(/[^0-9]/g, "").slice(0, 3))} />
          </Field>
          <Field label="Conversions at once" hint="1 is right for CPU; 2 can help with a GPU" save={workers}>
            <input aria-label="Conversions at once" inputMode="numeric" className={`${inp} w-[70px]`} {...textProps(workers)} onChange={(e) => workers.set(e.target.value.replace(/[^0-9]/g, "").slice(0, 1))} />
          </Field>
          <Field label="Daily library scan at" hint={`Once a day Convert re-reads the library for changes made outside Arrmada (imports are picked up straight away). Read on the server's clock (${s.server_tz}).`} save={scanAt}>
            <input type="time" aria-label="Daily library scan at" className={inp} {...textProps(scanAt)} />
          </Field>
          <Toggle save={resumable} label="Encode long films in pieces"
            hint="Encodes long films in ~5-minute pieces, so a restart or update during the encode loses at most one piece. Only CPU encodes of constant-frame-rate films are pieced; anything else encodes in one go. Off by default while it's being proven." />
        </div>
      </details>
    </div>
  );
}

// The resolution classes a file can be left out by, as the server names them.
const RESOLUTIONS = [["SD", "SD"], ["720p", "720p"], ["1080p", "1080p"], ["2160p", "4K"]] as const;

// LeaveAloneSection: what Convert never touches, by your choice — files of a resolution,
// whole library folders, and the titles you switched off on their own pages. Nothing is
// left out until you choose it.
function LeaveAloneSection({ resolutions, folders, libraryFolders }: {
  resolutions: AutoSave<string[]>; folders: AutoSave<string[]>; libraryFolders: ConvertLibraryFolder[];
}) {
  const flip = (save: AutoSave<string[]>, key: string, on: boolean) => {
    const next = on ? [...save.value.filter((k) => k !== key), key] : save.value.filter((k) => k !== key);
    void save.commit(next);
  };
  const groups = [["movie", "Movies"], ["series", "TV"]] as const;
  return (
    <Section title="Leave alone" desc="Files Convert never re-encodes or tidies. They're listed as left out, with the reason, and the Overview counts them.">
      <div role="group" aria-label="Resolutions to leave alone">
        <div className="flex flex-wrap items-center gap-2 text-sm font-semibold">Don't convert files that are <SavedTick save={resolutions as AutoSave<unknown>} /></div>
        <div className="mb-1 text-xs text-ink-faint">By the picture's real size, as Convert read it from the file — a widescreen 1080p film counts as 1080p.</div>
        <div className="flex flex-wrap gap-x-4 gap-y-1">
          {RESOLUTIONS.map(([v, label]) => (
            <label key={v} className="flex cursor-pointer items-center gap-2 py-0.5 text-sm">
              <input type="checkbox" checked={resolutions.value.includes(v)} onChange={(e) => flip(resolutions, v, e.target.checked)} />{label}
            </label>
          ))}
        </div>
      </div>
      {libraryFolders.length > 0 && (
        <div role="group" aria-label="Library folders to leave alone">
          <div className="flex flex-wrap items-center gap-2 text-sm font-semibold">Leave these library folders alone <SavedTick save={folders as AutoSave<unknown>} /></div>
          <div className="mb-1 text-xs text-ink-faint">Every file in a ticked folder is left as it is — for a folder of things you keep as they came, or one on a disk you'd rather not churn.</div>
          {groups.map(([media, heading]) => {
            const list = libraryFolders.filter((f) => f.media === media);
            if (list.length === 0) return null;
            return (
              <div key={media} className="mt-1">
                <div className="font-mono text-2xs uppercase text-ink-faint">{heading}</div>
                {list.map((f) => (
                  <label key={f.key} className="flex cursor-pointer items-baseline gap-2 py-0.5 text-sm">
                    <input type="checkbox" aria-label={`Leave ${heading} folder ${f.label} alone`} checked={folders.value.includes(f.key)} onChange={(e) => flip(folders, f.key, e.target.checked)} />
                    <span className="font-semibold">{f.label}</span>
                    <span className="min-w-0 truncate font-mono text-xs text-ink-faint" title={f.path}>{f.path}</span>
                  </label>
                ))}
              </div>
            );
          })}
        </div>
      )}
      <LeftOutTitles />
    </Section>
  );
}

// LeftOutTitles lists the films, shows and seasons switched off on their own pages ("Don't
// convert this"), each with the way back.
function LeftOutTitles() {
  const list = useConvertExclusions();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const items = list.data ?? [];
  return (
    <div>
      <div className="text-sm font-semibold">Titles left alone</div>
      {items.length === 0 ? (
        <div className="text-xs text-ink-faint">None. To leave a film or show alone, choose “Don't convert this” in its page's ⋯ menu, or on its plan card under Convert → Library.</div>
      ) : (
        <ul className="m-0 mt-1 flex list-none flex-col gap-1 p-0">
          {items.map((x) => {
            const key = `${x.kind}:${x.movie_id ?? x.series_id}:${x.season}`;
            const target: ExclusionTarget = x.kind === "movie"
              ? { kind: "movie", id: x.movie_id ?? 0, name: x.title }
              : { kind: "series", id: x.series_id ?? 0, name: x.title, ...(x.season >= 0 ? { season: x.season } : {}) };
            const href = x.kind === "movie" ? `/movies/${x.movie_id}` : `/series/${x.series_id}`;
            return (
              <li key={key} className="flex flex-wrap items-center gap-2 text-sm">
                <Link to={href} className="font-semibold hover:underline">{x.title}</Link>
                <span className="text-xs text-ink-faint">{x.kind === "movie" ? "film" : x.season >= 0 ? "season" : "show"}</span>
                <Button size="sm" variant="ghost" className="ml-auto" busy={busy === key} busyLabel="Saving…"
                  onClick={async () => {
                    setBusy(key);
                    try { toast(await setExclusion(target, false), { ms: 3500 }); } catch (e) { toast((e as Error).message, { tone: "error" }); } finally { setBusy(null); }
                  }}>
                  Let Convert work on it
                </Button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

// TenBitChoice: whether an 8-bit SDR source is written 10-bit (less banding) or kept 8-bit
// (plays as it is on more players). The devices panel below is the evidence either way.
function TenBitChoice({ save, transcoded }: { save: AutoSave<"always" | "match_source">; transcoded: ConvertDevice[] }) {
  const options = [
    ["always", "Always 10-bit", "Smoother skies, smoke and dark gradients — 8-bit sources show banding sooner after a re-encode. Players that can't decode 10-bit have the video transcoded."],
    ["match_source", "Match the source", "An 8-bit file stays 8-bit, so a player that plays the original as it is plays the converted file too. Gradients band a little more."],
  ] as const;
  return (
    <div role="radiogroup" aria-label="8-bit sources">
      <div className="flex flex-wrap items-center gap-2 text-sm font-semibold">8-bit sources (most H.264 films) <SavedTick save={save} /></div>
      <div className="mb-1.5 text-xs text-ink-faint">
        {save.value === "always"
          ? "Now: every re-encode is 10-bit (Main10)."
          : "Now: 8-bit sources stay 8-bit; 10-bit and HDR files are always 10-bit."}
        {transcoded.length > 0 && ` ${transcodedSentence(transcoded, "hevc10")}`}
      </div>
      {options.map(([v, label, hint]) => (
        <label key={v} className="flex cursor-pointer items-start gap-2 py-0.5 text-sm">
          <input type="radio" name="ten_bit" className="mt-0.5" checked={save.value === v} onChange={() => void save.commit(v)} />
          <span><b className="font-semibold">{label}</b><span className="block text-xs leading-snug text-ink-faint">{hint}</span></span>
        </label>
      ))}
    </div>
  );
}

function Toggle({ save, label, hint, disabled }: { save: AutoSave<boolean>; label: string; hint: ReactNode; disabled?: boolean }) {
  return (
    <div className="flex flex-col gap-0.5">
      <label className={`flex items-start gap-2.5 text-base ${disabled ? "opacity-50" : "cursor-pointer"}`}>
        <input type="checkbox" checked={save.value} disabled={disabled} onChange={(e) => void save.commit(e.target.checked)} className="mt-0.5" />
        <span><b className="font-semibold">{label}</b><span className="mt-0.5 block text-xs leading-snug text-ink-faint">{hint}</span></span>
      </label>
      {save.status !== "idle" && <span className="ml-6"><SavedTick save={save} /></span>}
    </div>
  );
}

function Section({ title, desc, children }: { title: string; desc?: string; children: ReactNode }) {
  return (
    <div className="rounded-xl p-4" style={cardStyle}>
      <div className="text-md font-bold">{title}</div>
      {desc && <div className="mt-0.5 text-sm text-ink-faint">{desc}</div>}
      <div className="mt-3 flex flex-col gap-3.5">{children}</div>
    </div>
  );
}

// KeepSubsChoice: follow the Subtitles module's kept languages ("@subtitles" on the server,
// so the two lists can't drift apart), or name languages here. Following is only ever
// chosen, never switched on for you: an existing list stays as it was.
function KeepSubsChoice({ save, input }: { save: AutoSave<string>; input: ReactNode }) {
  const follows = save.value.split(",").some((t) => t.trim().toLowerCase() === SAME_AS_SUBTITLES);
  const [subsLangs, setSubsLangs] = useState<string[] | null>(null);
  const languages = useLanguages();
  useEffect(() => { if (follows) api.subtitleSettings().then((st) => setSubsLangs(st.keep_languages ?? st.languages)).catch(() => {}); }, [follows]);
  return (
    <div role="radiogroup" aria-label="Keep subtitles in" className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-2 text-sm font-semibold">Keep subtitles in <SavedTick save={save as AutoSave<unknown>} /></div>
      <label className="flex cursor-pointer items-center gap-2 py-0.5 text-sm">
        <input type="radio" name="keep_sub_langs" checked={follows} onChange={() => void save.commit(SAME_AS_SUBTITLES)} />
        <span>Same as Subtitles{follows && subsLangs ? <span className="text-ink-faint"> — {subsLangs.map((c) => langName(c, languages)).join(", ")}</span> : null}</span>
      </label>
      <label className="flex cursor-pointer items-center gap-2 py-0.5 text-sm">
        <input type="radio" name="keep_sub_langs" checked={!follows} onChange={() => { if (follows) void save.commit(""); }} />
        These languages
      </label>
      {follows
        ? <span className="ml-6 text-xs text-ink-faint">Each title follows its own <Link to={LINKS.subtitlesSettings} style={{ color: "var(--accent-text)" }}>Subtitles profile</Link>: an unwanted full or SDH track goes too, and the full one when the audio is already that language and the profile says so. Forced tracks always stay.</span>
        : <span className="ml-6 flex flex-wrap items-center gap-2">{input}<span className="text-xs text-ink-faint">comma-separated, e.g. en · blank = keep every language</span></span>}
    </div>
  );
}

function Field({ label, hint, save, children }: { label: string; hint: string; save: AutoSave<unknown> | AutoSave<string>; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span><span className="block text-sm font-semibold">{label}</span><span className="block text-xs text-ink-faint">{hint}</span></span>
        {children}
      </div>
      {save.status !== "idle" && <div className="flex justify-end"><SavedTick save={save as AutoSave<unknown>} /></div>}
    </div>
  );
}
