import { useCallback, useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import {
  api,
  type Evaluation,
  type FormatInfo,
  type QualityProfileInfo,
  type StoredProfile,
} from "../lib/api";

const MEDIA_TABS = [
  { key: "movie", label: "Movies" },
  { key: "series", label: "Series" },
  { key: "book", label: "Books" },
  { key: "music", label: "Music" },
];

const RESOLUTIONS = [
  { v: "2160p", l: "4K" },
  { v: "1080p", l: "1080p" },
  { v: "720p", l: "720p" },
  { v: "576p", l: "576p" },
  { v: "480p", l: "480p" },
];

const SOURCES = [
  { v: "", l: "Any source" },
  { v: "HDTV", l: "HDTV+" },
  { v: "DVD", l: "DVD+" },
  { v: "WEBRip", l: "WEBRip+" },
  { v: "WEB-DL", l: "WEB-DL+" },
  { v: "BluRay", l: "BluRay+" },
  { v: "Remux", l: "Remux only" },
];

const MAX_SOURCES = [
  { v: "", l: "No upper limit" },
  { v: "Remux", l: "up to Remux" },
  { v: "BluRay", l: "up to BluRay (no Remux)" },
  { v: "WEB-DL", l: "up to WEB-DL" },
  { v: "WEBRip", l: "up to WEBRip" },
  { v: "DVD", l: "up to DVD" },
];

const CONDITION_TYPES = [
  { v: "dynamic_range", l: "Dynamic range (DV/HDR10…)" },
  { v: "audio", l: "Audio (Atmos/TrueHD…)" },
  { v: "codec", l: "Codec (x265/x264…)" },
  { v: "source", l: "Source (BluRay/WEB-DL…)" },
  { v: "resolution", l: "Resolution" },
  { v: "edition", l: "Edition (Director's Cut…)" },
  { v: "release_group", l: "Release group" },
];

function emptyProfile(media: string): StoredProfile {
  return {
    id: 0,
    media_type: media,
    name: "",
    base: "",
    allowed_resolutions: [],
    min_source: "",
    max_source: "",
    bitrate_cap_mbps: 0,
    small_bias: 0,
    min_format_score: 0,
    format_scores: {},
    custom_formats: [],
    keywords: [],
    rejected: [...EXECUTABLE_TYPES], // reject executables by default (malware safety)
    min_seeders: 0,
    stall_minutes: 0,
    upgrades_enabled: true,
    upgrade_bitrate_mbps: 0,
  };
}

// Common junk file-types / sources worth one-click rejecting.
const REJECT_TYPES = ["CAM", "TS", "XviD", "AVI", "WMV", "3D", "HDCAM", "R5"];
// Executable/script extensions — pre-rejected on new profiles for safety.
const EXECUTABLE_TYPES = ["exe", "bat", "cmd", "scr", "msi", "com", "vbs", "ps1"];

export function Quality() {
  const [media, setMedia] = useState("movie");
  const [profiles, setProfiles] = useState<QualityProfileInfo[]>([]);
  const [formats, setFormats] = useState<FormatInfo[]>([]);
  const [editing, setEditing] = useState<StoredProfile | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    api
      .qualityProfiles(media)
      .then((r) => {
        setProfiles(r.profiles);
        setFormats(r.formats);
        setError(null);
      })
      .catch((e: Error) => setError(e.message));
  }, [media]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // Books use fixed presets (Ebook / Audiobook / Ebook + Audiobook) — no resolution/HDR
  // editor, no adding/deleting. You just pick which one is the default.
  const preset = media === "book";

  const openNew = () => setEditing(emptyProfile(media));
  const editRef = async (info: QualityProfileInfo) => {
    const sp = await api.qualityProfile(info.key);
    setEditing(sp);
  };

  if (editing) {
    return (
      <Builder
        formats={formats}
        initial={editing}
        onCancel={() => setEditing(null)}
        onSaved={() => {
          setEditing(null);
          refresh();
        }}
      />
    );
  }

  return (
    <>
      <PageHeader title="Quality profiles" crumb="System / Quality" />
      <div className="mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6">
        <div className="mb-5 flex items-center justify-between">
          <Tabs value={media} onChange={setMedia} />
          {!preset && (
            <button onClick={openNew} className="rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
              + New profile
            </button>
          )}
        </div>

        <p className="mb-4 text-[12.5px] text-ink-dim">
          {preset
            ? "Books come as three fixed presets — pick which edition(s) you want when adding a book, or set a default here. There's nothing to tune: Arrmada always prefers EPUB for ebooks and M4B for audiobooks."
            : "A profile tells Arrmada what a good release looks like. Two come pre-loaded to get you going — edit them, delete them, or add your own. See exactly what you'd get, no scores to decode."}
        </p>

        {error && <div className="mb-3 rounded-lg p-3 text-[12px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>}

        {profiles.length === 0 ? (
          <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No quality profiles. Add one to tell Arrmada what to grab.
          </div>
        ) : (
          <div className="flex flex-col gap-2.5">
            {profiles.map((p) => (
              <ProfileCard key={p.key} info={p} media={media} preset={preset} onEdit={() => editRef(p)} onChange={refresh} />
            ))}
          </div>
        )}
      </div>
    </>
  );
}

function Tabs({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <div className="inline-flex rounded-lg p-1" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
      {MEDIA_TABS.map((t) => {
        const active = t.key === value;
        return (
          <button key={t.key} onClick={() => onChange(t.key)} className="rounded-md px-3 py-1.5 text-[12px] font-semibold transition-colors" style={{ background: active ? "var(--accent)" : "transparent", color: active ? "var(--accent-ink)" : "var(--ink-dim)" }}>
            {t.label}
          </button>
        );
      })}
    </div>
  );
}

// Plain-English summary for the fixed book presets (no scores to show).
const BOOK_PRESET_SUMMARY: Record<string, string> = {
  "Ebook": "Grabs the ebook only — prefers EPUB, then AZW3, MOBI, PDF.",
  "Audiobook": "Grabs the audiobook only — prefers M4B, then MP3.",
  "Ebook + Audiobook": "Grabs both the ebook and the audiobook, each in its best format.",
};

function ProfileCard({ info, media, preset, onEdit, onChange }: { info: QualityProfileInfo; media: string; preset: boolean; onEdit: () => void; onChange: () => void }) {
  const [confirming, setConfirming] = useState(false);
  const del = async () => {
    const id = Number(info.key.replace("custom:", ""));
    await api.deleteQualityProfile(id);
    onChange();
  };
  const makeDefault = async () => {
    await api.setDefaultProfile(media, info.key);
    onChange();
  };
  const summary = preset ? (BOOK_PRESET_SUMMARY[info.name] ?? info.summary) : (info.summary || "Any quality");
  return (
    <div className="flex items-center gap-4 rounded-xl p-3.5" style={{ background: "var(--panel)", border: `1px solid ${info.is_default ? "var(--accent)" : "var(--line)"}` }}>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="text-[13.5px] font-semibold">{info.name}</span>
          {info.is_default && <span className="rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>Default</span>}
        </div>
        <div className="mt-1 truncate text-[11.5px] text-ink-dim" title={summary}>{summary}</div>
      </div>
      <div className="flex flex-none items-center gap-2">
        {!info.is_default && (
          <button onClick={makeDefault} title="Use this profile by default when adding" className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Make default</button>
        )}
        {!preset && (
          <>
            <button onClick={onEdit} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>
              Edit
            </button>
            {confirming ? (
              <>
                <button onClick={del} className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--reject)", color: "#fff" }}>Delete</button>
                <button onClick={() => setConfirming(false)} className="rounded-lg px-2.5 py-1.5 text-[11.5px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>✕</button>
              </>
            ) : (
              <button onClick={() => setConfirming(true)} className="rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Delete</button>
            )}
          </>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Builder
// ---------------------------------------------------------------------------

function Builder({ formats, initial, onCancel, onSaved }: { formats: FormatInfo[]; initial: StoredProfile; onCancel: () => void; onSaved: () => void }) {
  const [sp, setSp] = useState<StoredProfile>(initial);
  const [preview, setPreview] = useState<Evaluation[] | null>(null);
  const [decision, setDecision] = useState<{ winner: Evaluation | null; why?: string[]; chosen_over?: string; eligible: Evaluation[]; rejected: Evaluation[] } | null>(null);
  const [advanced, setAdvanced] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState(false);

  // Live preview, debounced.
  useEffect(() => {
    let alive = true;
    const t = window.setTimeout(() => {
      api
        .qualityPreviewSpec(sp)
        .then((p) => {
          if (!alive) return;
          // Go marshals empty slices as null — coalesce so .length/.map are safe.
          const d = { ...p.decision, eligible: p.decision.eligible ?? [], rejected: p.decision.rejected ?? [] };
          setDecision(d);
          setPreview(d.eligible);
        })
        .catch(() => {});
    }, 250);
    return () => {
      alive = false;
      window.clearTimeout(t);
    };
  }, [sp]);

  const patch = (p: Partial<StoredProfile>) => setSp((s) => ({ ...s, ...p }));

  const toggleRes = (v: string) => {
    const has = sp.allowed_resolutions.includes(v);
    patch({ allowed_resolutions: has ? sp.allowed_resolutions.filter((r) => r !== v) : [...sp.allowed_resolutions, v] });
  };

  const setFormatScore = (name: string, score: number) => {
    const next = { ...sp.format_scores };
    if (score === 0) {
      delete next[name];
    } else {
      next[name] = score;
      // You can't *prefer* two formats in the same conflict group (e.g. prefer
      // both Dolby Vision and HDR10). But Avoiding one while Preferring the other
      // is valid — a TV that can't play DV still wants HDR10 — so only a positive
      // score clears sibling *preferences*; Avoid/Neutral are left untouched.
      if (score > 0) {
        const group = formats.find((f) => f.name === name)?.group;
        if (group) {
          for (const f of formats) {
            if (f.name !== name && f.group === group && (next[f.name] ?? 0) > 0) delete next[f.name];
          }
        }
      }
    }
    patch({ format_scores: next });
  };

  const videoFormats = formats.filter((f) => f.group === "hdr" || f.group === "codec");
  const audioFormats = formats.filter((f) => f.group === "audio");

  const save = async () => {
    if (!sp.name.trim()) {
      setError("Give your profile a name.");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      if (sp.id > 0) await api.updateQualityProfile(sp.id, sp);
      else await api.createQualityProfile(sp);
      onSaved();
    } catch (e) {
      setError((e as Error).message);
      setSaving(false);
    }
  };

  const winner = decision?.winner ?? null;
  const total = (decision?.eligible.length ?? 0) + (decision?.rejected.length ?? 0);

  return (
    <>
      <PageHeader title={sp.id > 0 ? "Edit profile" : "New profile"} crumb="System / Quality" />
      <div className="mx-auto grid w-full max-w-[1240px] grid-cols-1 gap-7 px-4 py-6 sm:px-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.05fr)]">
        {/* Builder form */}
        <section>
          <div className="mb-4 flex items-center gap-2">
            <button onClick={onCancel} className="text-[12px] text-ink-dim hover:text-[var(--ink)]">← Back</button>
          </div>

          <label className="mb-1.5 block font-mono text-[10px] font-bold uppercase tracking-wide text-accent">Name</label>
          <input value={sp.name} onChange={(e) => patch({ name: e.target.value })} placeholder="e.g. My 4K collection" className="mb-5 w-full rounded-lg px-3 py-2 text-[13px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />

          <SectionLabel>Resolution goal</SectionLabel>
          <div className="flex flex-wrap gap-2">
            {RESOLUTIONS.map((r) => {
              const active = sp.allowed_resolutions.includes(r.v);
              return (
                <button key={r.v} onClick={() => toggleRes(r.v)} className="rounded-lg px-3 py-1.5 text-[12px] font-semibold" style={{ border: `1px solid ${active ? "var(--accent)" : "var(--line)"}`, background: active ? "var(--accent-soft)" : "var(--panel)", color: active ? "var(--accent)" : "var(--ink-dim)" }}>
                  {r.l}
                </button>
              );
            })}
          </div>
          <p className="mt-1.5 text-[11px] text-ink-faint">{sp.allowed_resolutions.length === 0 ? "Any resolution allowed." : "Only these resolutions will be grabbed."}</p>

          <SectionLabel>Source range</SectionLabel>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <span className="mb-1 block text-[10.5px] text-ink-faint">Minimum</span>
              <select value={sp.min_source} onChange={(e) => patch({ min_source: e.target.value })} className="w-full rounded-lg px-3 py-2 text-[12.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
                {SOURCES.map((s) => <option key={s.v} value={s.v}>{s.l}</option>)}
              </select>
            </div>
            <div>
              <span className="mb-1 block text-[10.5px] text-ink-faint">Maximum</span>
              <select value={sp.max_source} onChange={(e) => patch({ max_source: e.target.value })} className="w-full rounded-lg px-3 py-2 text-[12.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
                {MAX_SOURCES.map((s) => <option key={s.v} value={s.v}>{s.l}</option>)}
              </select>
            </div>
          </div>
          <p className="mt-1.5 text-[10.5px] text-ink-faint">Within this range and your bitrate cap, Arrmada picks the highest-bitrate release.</p>

          <SectionLabel>Bitrate ceiling</SectionLabel>
          <div className="rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
            <div className="mb-1 flex items-baseline justify-between">
              <span className="text-[12.5px] text-ink-dim">Reject releases above</span>
              <span className="flex items-center gap-1.5">
                <input type="number" min={0} step={1} value={sp.bitrate_cap_mbps}
                  onChange={(e) => patch({ bitrate_cap_mbps: Math.max(0, Number(e.target.value)) })}
                  className="w-[68px] rounded-lg px-2 py-1 text-right font-mono text-[13px] font-semibold"
                  style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: sp.bitrate_cap_mbps === 0 ? "var(--ink-dim)" : "var(--accent)" }} />
                <span className="text-[11px] text-ink-faint">{sp.bitrate_cap_mbps === 0 ? "· no limit" : "Mbps"}</span>
              </span>
            </div>
            <input type="range" min={0} max={100} step={1} value={Math.min(100, sp.bitrate_cap_mbps)} onChange={(e) => patch({ bitrate_cap_mbps: Number(e.target.value) })} className="w-full" style={{ accentColor: "var(--accent)" }} />
            <div className="mt-2 flex justify-between font-mono text-[10px] text-ink-faint"><span>No limit</span><span>50 Mbps</span><span>100+ Mbps</span></div>
            <p className="mt-2.5 text-[10.5px] text-ink-faint">Length-independent — caps quality per second, so it treats a 90-minute film and a 3-hour epic (or a full season) the same. Drag to 100, or type a higher number for remux-grade limits. ~15–25 Mbps is a great 1080p target, ~40–60 for 4K.</p>
          </div>

          <SectionLabel>Video</SectionLabel>
          <p className="-mt-1 mb-2 text-[10.5px] text-ink-faint">Prefer one HDR format. You can Avoid the one your TV can't play (e.g. Dolby Vision) while Preferring HDR10.</p>
          <div className="flex flex-col gap-2">
            {videoFormats.map((f) => (
              <FormatToggle key={f.name} format={f} score={sp.format_scores[f.name] ?? 0} advanced={advanced} onChange={(s) => setFormatScore(f.name, s)} />
            ))}
          </div>

          <SectionLabel>Audio</SectionLabel>
          <p className="-mt-1 mb-2 text-[10.5px] text-ink-faint">Pick one preferred audio format.</p>
          <div className="flex flex-col gap-2">
            {audioFormats.map((f) => (
              <FormatToggle key={f.name} format={f} score={sp.format_scores[f.name] ?? 0} advanced={advanced} onChange={(s) => setFormatScore(f.name, s)} />
            ))}
          </div>

          <SectionLabel>Preferred keywords</SectionLabel>
          <KeywordEditor keywords={sp.keywords ?? []} onChange={(kw) => patch({ keywords: kw })} />

          <SectionLabel>Reject</SectionLabel>
          <RejectEditor rejected={sp.rejected ?? []} onChange={(r) => patch({ rejected: r })} />

          <SectionLabel>Grab rules</SectionLabel>
          <div className="grid grid-cols-2 gap-3">
            <NumberField label="Minimum seeders" hint="Skip releases with fewer" value={sp.min_seeders} onChange={(v) => patch({ min_seeders: v })} />
            <NumberField label="Stall timeout (min)" hint="0 = off. Try another release if it stalls" value={sp.stall_minutes} onChange={(v) => patch({ stall_minutes: v })} />
          </div>

          <SectionLabel>Upgrades</SectionLabel>
          <div className="rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
            <div className="flex items-center justify-between gap-3">
              <div className="min-w-0">
                <div className="text-[12.5px] font-semibold">Automatically upgrade</div>
                <div className="text-[10.5px] text-ink-faint">After a file is imported, keep watching for a better release and replace it. Stops once you're at the best resolution with all your preferred formats — never churns on a few extra Mb/s.</div>
              </div>
              <button
                role="switch"
                aria-checked={sp.upgrades_enabled}
                onClick={() => patch({ upgrades_enabled: !sp.upgrades_enabled })}
                className="relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors"
                style={{ background: sp.upgrades_enabled ? "var(--accent)" : "var(--panel-2)", border: "1px solid var(--line)" }}
              >
                <span className="inline-block h-4 w-4 transform rounded-full bg-white transition-transform" style={{ transform: sp.upgrades_enabled ? "translateX(22px)" : "translateX(3px)" }} />
              </button>
            </div>
            {sp.upgrades_enabled && (
              <div className="mt-3.5 border-t pt-3" style={{ borderColor: "var(--line)" }}>
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <div className="text-[12px] font-semibold">Also upgrade on higher bitrate</div>
                    <div className="text-[10.5px] text-ink-faint">Grab a same-or-better release whose average bitrate is at least this many Mbps higher, up to your bitrate ceiling. 0 = only upgrade on a real quality gain.</div>
                  </div>
                  <div className="flex flex-none items-center gap-1.5">
                    <input type="number" min={0} step={1} value={sp.upgrade_bitrate_mbps} onChange={(e) => patch({ upgrade_bitrate_mbps: Math.max(0, Number(e.target.value)) })} className="w-[64px] rounded-lg px-2 py-1.5 text-right text-[13px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
                    <span className="text-[11px] text-ink-faint">Mbps</span>
                  </div>
                </div>
              </div>
            )}
          </div>

          <div className="mt-4 flex items-center justify-between">
            <button onClick={() => setAdvanced((a) => !a)} className="text-[11.5px] font-semibold" style={{ color: "var(--accent)" }}>{advanced ? "Hide advanced" : "Advanced…"}</button>
          </div>

          {advanced && (
            <AdvancedPanel sp={sp} patch={patch} setFormatScore={setFormatScore} />
          )}

          {error && <div className="mt-4 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
          <div className="mt-5 flex gap-2.5">
            <button onClick={save} disabled={saving} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{saving ? "Saving…" : "Save profile"}</button>
            <button onClick={onCancel} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
          </div>
        </section>

        {/* Live result */}
        <section>
          <div className="mb-1 flex items-center gap-2 text-[15px] font-bold">
            <span className="inline-block h-2 w-2 rounded-full" style={{ background: "var(--accent)" }} />
            What you'll get
          </div>
          <p className="mb-4 mt-1 text-[11.5px] text-ink-faint">Live from the scoring engine on a sample release set — change anything and it re-decides.</p>
          {!winner ? (
            <div className="rounded-xl p-7 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>Nothing matches this profile yet. Loosen a preference or raise the size ceiling.</div>
          ) : (
            <Hero winner={winner} why={decision?.why ?? []} chosenOver={decision?.chosen_over} open={open} onToggle={() => setOpen((o) => !o)} eligible={preview?.slice(1) ?? []} rejected={decision?.rejected ?? []} total={total} advanced={advanced} />
          )}
        </section>
      </div>
    </>
  );
}

function NumberField({ label, hint, value, onChange }: { label: string; hint: string; value: number; onChange: (v: number) => void }) {
  return (
    <div className="rounded-xl p-3" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <div className="text-[12px] font-semibold">{label}</div>
      <div className="mb-2 text-[10.5px] text-ink-faint">{hint}</div>
      <input type="number" min={0} value={value} onChange={(e) => onChange(Math.max(0, Number(e.target.value)))} className="w-full rounded-lg px-2.5 py-1.5 text-[13px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
    </div>
  );
}

function KeywordEditor({ keywords, onChange }: { keywords: { term: string; score: number }[]; onChange: (kw: { term: string; score: number }[]) => void }) {
  const [term, setTerm] = useState("");
  const [score, setScore] = useState(25);
  const add = () => {
    if (!term.trim()) return;
    onChange([...keywords, { term: term.trim(), score }]);
    setTerm("");
  };
  return (
    <div>
      {keywords.length > 0 && (
        <div className="mb-2 flex flex-col gap-1.5">
          {keywords.map((k, i) => (
            <div key={i} className="flex items-center gap-2 rounded-lg px-2.5 py-1.5 text-[12px]" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
              <span className="font-semibold">{k.term}</span>
              <span className="ml-auto font-mono" style={{ color: k.score >= 0 ? "var(--good)" : "var(--reject)" }}>{k.score > 0 ? `+${k.score}` : k.score}</span>
              <button onClick={() => onChange(keywords.filter((_, j) => j !== i))} className="text-ink-faint hover:text-[var(--reject)]">✕</button>
            </div>
          ))}
        </div>
      )}
      <div className="flex items-center gap-2">
        <input value={term} onChange={(e) => setTerm(e.target.value)} onKeyDown={(e) => e.key === "Enter" && add()} placeholder="e.g. IMAX, Criterion, PROPER" className="flex-1 rounded-lg px-2.5 py-1.5 text-[12.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <input type="number" value={score} onChange={(e) => setScore(Number(e.target.value))} className="w-[70px] rounded-lg px-2 py-1.5 text-right font-mono text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <button onClick={add} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>Add</button>
      </div>
      <p className="mt-1.5 text-[10.5px] text-ink-faint">Words in the release name that add (or subtract, if negative) score.</p>
    </div>
  );
}

function RejectEditor({ rejected, onChange }: { rejected: string[]; onChange: (r: string[]) => void }) {
  const [term, setTerm] = useState("");
  const has = (t: string) => rejected.some((r) => r.toLowerCase() === t.toLowerCase());
  const toggle = (t: string) => (has(t) ? onChange(rejected.filter((r) => r.toLowerCase() !== t.toLowerCase())) : onChange([...rejected, t]));
  const addCustom = () => {
    if (!term.trim() || has(term.trim())) { setTerm(""); return; }
    onChange([...rejected, term.trim()]);
    setTerm("");
  };
  const hidden = [...REJECT_TYPES, ...EXECUTABLE_TYPES].map((t) => t.toLowerCase());
  const custom = rejected.filter((r) => !hidden.includes(r.toLowerCase()));
  const execOn = EXECUTABLE_TYPES.every((t) => has(t));
  const toggleExec = () =>
    execOn
      ? onChange(rejected.filter((r) => !EXECUTABLE_TYPES.some((t) => t.toLowerCase() === r.toLowerCase())))
      : onChange([...rejected.filter((r) => !EXECUTABLE_TYPES.some((t) => t.toLowerCase() === r.toLowerCase())), ...EXECUTABLE_TYPES]);
  return (
    <div>
      <button onClick={toggleExec} className="mb-2 flex w-full items-center gap-2.5 rounded-lg p-2.5 text-left" style={{ border: `1px solid ${execOn ? "var(--reject)" : "var(--line)"}`, background: execOn ? "var(--reject-soft)" : "var(--panel)" }}>
        <span className="grid h-4 w-4 flex-none place-items-center rounded" style={{ background: execOn ? "var(--reject)" : "transparent", border: `1px solid ${execOn ? "var(--reject)" : "var(--line)"}` }}>
          {execOn && <svg width="10" height="10" viewBox="0 0 24 24" fill="none"><path d="M4 12l5 5L20 6" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" /></svg>}
        </span>
        <span className="min-w-0 flex-1">
          <span className="text-[12.5px] font-semibold" style={{ color: execOn ? "var(--reject)" : "var(--ink)" }}>Reject executables & scripts</span>
          <span className="block text-[10.5px] text-ink-faint">.exe .bat .cmd .scr .msi … — malware safety, on by default</span>
        </span>
      </button>
      <div className="mb-2 flex flex-wrap gap-1.5">
        {REJECT_TYPES.map((t) => (
          <button key={t} onClick={() => toggle(t)} className="rounded-lg px-2.5 py-1 text-[11.5px] font-semibold" style={{ border: `1px solid ${has(t) ? "var(--reject)" : "var(--line)"}`, background: has(t) ? "var(--reject-soft)" : "var(--panel)", color: has(t) ? "var(--reject)" : "var(--ink-dim)" }}>{t}</button>
        ))}
      </div>
      {custom.length > 0 && (
        <div className="mb-2 flex flex-wrap gap-1.5">
          {custom.map((r) => (
            <span key={r} className="flex items-center gap-1.5 rounded-lg px-2 py-1 text-[11.5px]" style={{ background: "var(--reject-soft)", color: "var(--reject)" }}>{r}<button onClick={() => toggle(r)}>✕</button></span>
          ))}
        </div>
      )}
      <div className="flex items-center gap-2">
        <input value={term} onChange={(e) => setTerm(e.target.value)} onKeyDown={(e) => e.key === "Enter" && addCustom()} placeholder="Reject any release containing… (e.g. HDCAM, Telesync)" className="flex-1 rounded-lg px-2.5 py-1.5 text-[12.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <button onClick={addCustom} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Reject</button>
      </div>
      <p className="mt-1.5 text-[10.5px] text-ink-faint">Toggle file-types/sources to reject, or add your own terms. Any match is skipped entirely.</p>
    </div>
  );
}

function FormatToggle({ format, score, advanced, onChange }: { format: FormatInfo; score: number; advanced: boolean; onChange: (s: number) => void }) {
  const state = score > 0 ? "prefer" : score < 0 ? "avoid" : "ignore";
  const opts: { key: string; label: string; val: number; tone: string }[] = [
    { key: "avoid", label: "Avoid", val: -50, tone: "var(--reject)" },
    { key: "ignore", label: "Neutral", val: 0, tone: "var(--ink-faint)" },
    { key: "prefer", label: "Prefer", val: 50, tone: "var(--good)" },
  ];
  return (
    <div className="flex items-center gap-3 rounded-lg p-2.5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <div className="min-w-0 flex-1">
        <div className="text-[12.5px] font-semibold">{format.name}</div>
        <div className="truncate text-[11px] text-ink-faint" title={format.description}>{format.description}</div>
      </div>
      {advanced ? (
        <input type="number" value={score} onChange={(e) => onChange(Number(e.target.value))} className="w-[72px] rounded-lg px-2 py-1 text-right font-mono text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
      ) : (
        <div className="inline-flex rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
          {opts.map((o) => (
            <button key={o.key} onClick={() => onChange(o.val)} className="rounded-md px-2.5 py-1 text-[11px] font-semibold" style={{ background: state === o.key ? o.tone : "transparent", color: state === o.key ? "#fff" : "var(--ink-faint)" }}>{o.label}</button>
          ))}
        </div>
      )}
    </div>
  );
}

function AdvancedPanel({ sp, patch, setFormatScore }: { sp: StoredProfile; patch: (p: Partial<StoredProfile>) => void; setFormatScore: (name: string, score: number) => void }) {
  const [cfName, setCfName] = useState("");
  const [cfType, setCfType] = useState("release_group");
  const [cfValue, setCfValue] = useState("");
  const [cfScore, setCfScore] = useState(50);

  const addCustom = () => {
    if (!cfName.trim() || !cfValue.trim()) return;
    const cf = { name: cfName.trim(), conditions: [{ type: cfType, value: cfValue.trim() }] };
    patch({ custom_formats: [...(sp.custom_formats ?? []), cf] });
    setFormatScore(cf.name, cfScore);
    setCfName("");
    setCfValue("");
  };

  const removeCustom = (name: string) => {
    patch({ custom_formats: (sp.custom_formats ?? []).filter((c) => c.name !== name) });
    setFormatScore(name, 0);
  };

  return (
    <div className="mt-3 rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <div className="mb-3 flex items-center justify-between">
        <span className="font-mono text-[10px] font-bold uppercase tracking-wide text-accent">Custom formats</span>
      </div>
      <p className="mb-3 text-[11px] text-ink-dim">Match anything the parser reads — a favourite release group, a specific edition, a codec — and score it.</p>

      {(sp.custom_formats ?? []).map((c) => (
        <div key={c.name} className="mb-2 flex items-center gap-2 rounded-lg p-2 text-[11.5px]" style={{ background: "var(--panel-2)" }}>
          <span className="font-semibold">{c.name}</span>
          <span className="font-mono text-[10.5px] text-ink-faint">{c.conditions[0]?.type} = {c.conditions[0]?.value}</span>
          <span className="ml-auto font-mono" style={{ color: (sp.format_scores[c.name] ?? 0) >= 0 ? "var(--good)" : "var(--reject)" }}>{sp.format_scores[c.name] ?? 0}</span>
          <button onClick={() => removeCustom(c.name)} className="text-ink-faint hover:text-[var(--reject)]">✕</button>
        </div>
      ))}

      <div className="mt-2 grid grid-cols-2 gap-2">
        <input value={cfName} onChange={(e) => setCfName(e.target.value)} placeholder="Format name" className="rounded-lg px-2.5 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <select value={cfType} onChange={(e) => setCfType(e.target.value)} className="rounded-lg px-2 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
          {CONDITION_TYPES.map((t) => <option key={t.v} value={t.v}>{t.l}</option>)}
        </select>
        <input value={cfValue} onChange={(e) => setCfValue(e.target.value)} placeholder="Value (e.g. FraMeSToR)" className="rounded-lg px-2.5 py-1.5 text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
        <div className="flex items-center gap-2">
          <input type="number" value={cfScore} onChange={(e) => setCfScore(Number(e.target.value))} className="w-full rounded-lg px-2 py-1.5 text-right font-mono text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
          <button onClick={addCustom} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>Add</button>
        </div>
      </div>

      <div className="mt-4 flex items-center justify-between">
        <span className="text-[12px] text-ink-dim">Require at least this preferred-score to grab</span>
        <input type="number" value={sp.min_format_score} onChange={(e) => patch({ min_format_score: Number(e.target.value) })} className="w-[80px] rounded-lg px-2 py-1 text-right font-mono text-[12px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Preview (reused from the original page)
// ---------------------------------------------------------------------------

function Hero({ winner, why, chosenOver, open, onToggle, eligible, rejected, total, advanced }: { winner: Evaluation; why: string[]; chosenOver?: string; open: boolean; onToggle: () => void; eligible: Evaluation[]; rejected: Evaluation[]; total: number; advanced: boolean }) {
  const r = winner.candidate.release;
  return (
    <>
      <div className="rounded-2xl p-5" style={{ background: "linear-gradient(180deg, var(--accent-soft), var(--panel) 62%)", border: "1px solid var(--accent)", boxShadow: "var(--shadow)" }}>
        <div className="mb-2.5 font-mono text-[10px] font-bold uppercase tracking-[0.12em] text-accent">★ Arrmada would grab this</div>
        <div className="mb-3 text-[24px] font-bold tracking-tight">{r.resolution} {r.source}</div>
        <div className="mb-4 flex flex-wrap gap-1.5">
          {(r.hdr ?? []).map((h) => <Chip key={h} accent>{h}</Chip>)}
          {(r.audio ?? []).map((a) => <Chip key={a} accent>{a}</Chip>)}
          {r.codec && <Chip>{r.codec}</Chip>}
          <Chip>{winner.candidate.size_gb.toFixed(1)} GB</Chip>
          <Chip>▲ {winner.candidate.seeders}</Chip>
        </div>
        <div className="rounded-xl p-3.5" style={{ background: "color-mix(in srgb, var(--bg) 34%, transparent)", border: "1px solid var(--line-soft)" }}>
          <div className="mb-2.5 font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Why this one</div>
          <div className="flex flex-col gap-2">
            {why.map((w) => (
              <div key={w} className="flex items-start gap-2 text-[13px]">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" style={{ color: "var(--accent)", flex: "none", marginTop: 2 }}><path d="M4 12l5 5L20 6" stroke="currentColor" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" /></svg>
                <span>{w}</span>
              </div>
            ))}
          </div>
        </div>
        {chosenOver && <div className="mt-3.5 border-t border-dashed pt-3.5 text-[12.5px] text-ink-dim" style={{ borderColor: "var(--line)" }}>{chosenOver}</div>}
        <div className="mt-3 truncate font-mono text-[10.5px] text-ink-faint" title={winner.candidate.name}>{winner.candidate.name}</div>
      </div>
      <button onClick={onToggle} aria-expanded={open} className="mt-3.5 flex w-full items-center justify-center gap-2 rounded-xl p-2.5 text-[12.5px] font-semibold text-ink-dim transition-colors hover:text-ink" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
        {open ? "Hide" : `Compare all ${total}`} releases
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" style={{ transform: open ? "rotate(180deg)" : "none", transition: "transform .2s" }}><path d="M6 9l6 6 6-6" stroke="currentColor" strokeWidth="2" strokeLinecap="round" fill="none" /></svg>
      </button>
      {open && (
        <div className="mt-2.5 overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", background: "var(--line-soft)" }}>
          {eligible.length > 0 && <CmpLabel>Also eligible</CmpLabel>}
          {eligible.map((e, i) => <CmpRow key={`e${i}`} ev={e} advanced={advanced} />)}
          {rejected.length > 0 && <CmpLabel>Skipped</CmpLabel>}
          {rejected.map((e, i) => <CmpRow key={`r${i}`} ev={e} skip advanced={advanced} />)}
        </div>
      )}
    </>
  );
}

function Chip({ children, accent }: { children: React.ReactNode; accent?: boolean }) {
  return <span className="rounded-md px-1.5 py-1 font-mono text-[10.5px]" style={{ background: accent ? "var(--accent-soft)" : "var(--panel-2)", border: `1px solid ${accent ? "var(--accent-line)" : "var(--line-soft)"}`, color: accent ? "var(--accent)" : "var(--ink-dim)" }}>{children}</span>;
}

function CmpLabel({ children }: { children: React.ReactNode }) {
  return <div className="px-3.5 pb-1 pt-2.5 font-mono text-[9px] font-bold uppercase tracking-[0.09em] text-ink-faint" style={{ background: "var(--panel)" }}>{children}</div>;
}

function CmpRow({ ev, skip, advanced }: { ev: Evaluation; skip?: boolean; advanced?: boolean }) {
  const r = ev.candidate.release;
  return (
    <div className="flex items-center gap-3 px-3.5 py-2.5" style={{ background: "var(--panel)", opacity: skip ? 0.6 : 1 }}>
      <span className="min-w-[108px] text-[12.5px] font-semibold">{r.resolution} {r.source}</span>
      <span className="min-w-[50px] font-mono text-[11.5px] text-ink-dim">{ev.candidate.size_gb.toFixed(1)} GB</span>
      <span className="ml-auto text-right text-[12px]" style={{ color: skip ? "var(--reject)" : "var(--ink-faint)" }}>{skip ? ev.reject_reason : advanced ? `score ${ev.total}` : (ev.matched && ev.matched.length ? ev.matched.join(", ") : "eligible")}</span>
    </div>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="mb-3 mt-6 flex items-center gap-2.5">
      <span className="font-mono text-[10px] font-bold uppercase tracking-[0.12em] text-accent">{children}</span>
      <span className="h-px flex-1" style={{ background: "var(--line-soft)" }} />
    </div>
  );
}
