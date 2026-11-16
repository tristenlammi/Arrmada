import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Button, IconButton, Sheet, useConfirm, useToast } from "../ui";
import { usePoll } from "../lib/usePoll";
import { formatAgo, formatBytes } from "../lib/format";
import { langName, useLanguages } from "../lib/languages";
import { jobFailed, jobToast, useJob } from "../lib/useJob";
import type { SubLangStatus, SubTrack } from "../lib/api";
import { subMediaKey, subtitleFileApi, type SubFileView, type SubMediaRef, type SubRedo, type SubSidecar } from "../lib/subtitleFileApi";
import { orphanApi } from "../pages/subtitles/orphanApi";
import { LINKS } from "../lib/links";
import { REASON_LABEL, SOURCE_LABEL, WantChip, sidecarForLang, sidecarForWant } from "./subtitles/WantChip";
import { SearchPanel } from "./subtitles/SearchPanel";

// SubtitleDrawer is one video's subtitles in full (SUB-27/28): each kept language's state
// with Search OpenSubtitles, Generate with AI and Replace; every subtitle file beside the
// video with where it came from, its Health and its first lines, timing nudges, a sync
// check and Delete (to the recycle bin); orphans; the embedded tracks with Extract; and
// what the last attempts said. A right-hand panel on a desktop, a full-height sheet on a
// phone. Staff-only; LazySubtitleDrawer loads it on first open.

export interface SubtitleDrawerProps {
  media: SubMediaRef;
  /** Shown until the file view loads. */
  title?: string;
  onClose: () => void;
  /** Something on disk changed (a shift, a delete, an adopt): the opener can refresh. */
  onChanged?: () => void;
}

const lbl = "font-mono text-2xs font-bold uppercase tracking-[0.11em] text-ink-faint";
const RUNG_LABEL: Record<string, string> = { extract: "Embedded", opensubtitles: "OpenSubtitles", ai: "AI", upgrade: "Upgrade", repair: "Repair" };
const NUDGES = [-1000, -500, -100, 100, 500, 1000];
const HEALTH_COLOR: Record<string, string> = { ok: "var(--good-text)", uncertain: "var(--accent-text)", bad: "var(--avoid-text)", unknown: "var(--ink-dim)", unchecked: "var(--ink-faint)" };

// fmtCue is a cue time as h:mm:ss.mmm (the hour only when there is one).
export function fmtCue(ms: number): string {
  const neg = ms < 0;
  let t = Math.abs(Math.round(ms));
  const h = Math.floor(t / 3_600_000); t -= h * 3_600_000;
  const m = Math.floor(t / 60_000); t -= m * 60_000;
  const s = Math.floor(t / 1000);
  const frac = String(t - s * 1000).padStart(3, "0");
  const mmss = `${String(m).padStart(h ? 2 : 1, "0")}:${String(s).padStart(2, "0")}.${frac}`;
  return `${neg ? "−" : ""}${h ? `${h}:` : ""}${mmss}`;
}

// fmtShift is a signed shift in seconds: "+1.5 s", "−0.5 s".
export function fmtShift(ms: number): string {
  const s = (Math.abs(ms) / 1000).toFixed(1).replace(/\.0$/, "");
  return `${ms < 0 ? "−" : "+"}${s} s`;
}

// extractLang is the language an embedded track is saved as: its own tag, or the first
// kept language for an untagged one (as an untagged sidecar would count).
export function extractLang(t: SubTrack, kept: string[]): string {
  return t.lang && t.lang !== "und" ? t.lang : kept[0] ?? "en";
}

export default function SubtitleDrawer({ media, title, onClose, onChanged }: SubtitleDrawerProps) {
  const [view, setView] = useState<SubFileView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [searching, setSearching] = useState<string | null>(null); // the language whose search panel is open
  const [aiStream, setAiStream] = useState<number | null>(null); // the audio track Generate uses; null = Arrmada picks
  const languages = useLanguages();
  const toast = useToast();
  const confirm = useConfirm();

  // Keyed on the file, not the object: a caller passing a fresh literal each render
  // mustn't refetch.
  const key = subMediaKey(media);
  const load = useCallback(() => subtitleFileApi.view(media).then((v) => { setView(v); setError(null); }).catch((e: Error) => setError(e.message)),
    [key]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { void load(); }, [load]);
  // While a job works on the file, follow it: its sidecars appear as they're written.
  const working = (view?.jobs.length ?? 0) > 0;
  const [wasWorking, setWasWorking] = useState(false);
  usePoll(load, working ? 4000 : null, { immediate: false });
  // A job finishing changed the folder: the opener's chips are stale too.
  useEffect(() => {
    if (working) setWasWorking(true);
    else if (wasWorking) { setWasWorking(false); onChanged?.(); }
  }, [working, wasWorking, onChanged]);

  const changed = useCallback(() => { void load(); onChanged?.(); }, [load, onChanged]);
  const name = (code: string) => (code ? `${langName(code, languages)}` : "untagged");

  // queue asks first (it replaces a file, or spends a download), then queues the job.
  const queue = async (r: SubRedo, what: { title: string; body: ReactNode; done: string }) => {
    if (!(await confirm({ title: what.title, body: what.body, confirmLabel: "Queue it" }))) return;
    try {
      await subtitleFileApi.redo(media, r);
      toast(what.done);
      void load();
    } catch (e) { toast((e as Error).message, { tone: "error" }); }
  };
  const replaces = (lang: string) => {
    const s = view && sidecarForLang(lang, view.sidecars, view.kept[0] === lang);
    return s ? <> Once it's written, {s.name} moves to the recycle bin; if nothing comes of it, that file stays.</> : null;
  };
  const generate = (l: SubLangStatus) => queue(
    { lang: l.lang, mode: "ai", ...(aiStream != null ? { audio_stream: aiStream } : {}) },
    {
      title: `Generate ${name(l.lang)} with AI?`,
      body: <>Whisper makes a {name(l.lang)} subtitle from {aiStream != null ? `audio track #${aiStream}` : "the audio"} (large-v3 when it's installed). It waits in the AI lane, behind whatever is already there.{replaces(l.lang)}</>,
      done: `Queued: ${l.lang} with AI`,
    },
  );
  const next = (l: SubLangStatus) => queue(
    { lang: l.lang, mode: "next" },
    {
      title: `Replace the ${name(l.lang)} subtitle?`,
      body: <>The next-best source replaces it: another OpenSubtitles upload (never the one there now), an embedded track, or AI. A download spends one of today's OpenSubtitles downloads.{replaces(l.lang)}</>,
      done: `Queued: ${l.lang} from the next-best source`,
    },
  );
  const extract = (t: SubTrack) => {
    const lang = extractLang(t, view?.kept ?? []);
    return queue(
      { lang, variant: t.sdh ? "sdh" : "", mode: "extract", track_index: t.index },
      {
        title: `Extract track #${t.index} as ${name(lang)}?`,
        body: <>The embedded {t.codec} track{t.title ? ` “${t.title}”` : ""} is copied out beside the video as the {name(lang)} subtitle{t.sdh ? " (SDH)" : ""}. The video isn't touched.{replaces(lang)}</>,
        done: `Queued: extract track #${t.index}`,
      },
    );
  };

  const heading = view?.title || title || "Subtitles";
  const file = view?.video_path.split(/[\\/]/).pop();

  return (
    <Sheet
      onClose={onClose}
      ariaLabel={`Subtitles — ${heading}`}
      handle={false}
      panelClassName="thin-scroll w-full overflow-y-auto rounded-t-2xl pb-[env(safe-area-inset-bottom)] h-[calc(100dvh-0.75rem)] sm:ml-auto sm:h-auto sm:max-w-[640px] sm:self-stretch sm:rounded-2xl sm:pb-0"
    >
      <div className="sticky top-0 z-[1] flex items-start gap-3 px-4 pb-3 pt-4" style={{ background: "var(--panel)", borderBottom: "1px solid var(--line-soft)" }}>
        <div className="min-w-0 flex-1">
          <div className={lbl}>Subtitles</div>
          <h2 className="m-0 mt-0.5 truncate text-lg font-bold" title={heading}>{heading}</h2>
          {file && <div className="mt-0.5 truncate font-mono text-xs text-ink-faint" title={view?.video_path}>{file}</div>}
        </div>
        <IconButton label="Close" onClick={onClose} className="h-8 w-8 flex-none rounded-lg text-lg text-ink-dim hover:bg-[var(--panel-2)]">✕</IconButton>
      </div>

      <div className="flex flex-col gap-5 px-4 py-4">
        {error && !view && <div className="rounded-lg p-3 text-base" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>{error}</div>}
        {!view && !error && <div className="py-10 text-center text-base text-ink-dim">Reading the folder…</div>}
        {view && (
          <>
            {view.jobs.length > 0 && (
              <div className="rounded-lg px-3 py-2 text-sm" style={{ border: "1px solid var(--accent-line)", background: "var(--accent-soft)", color: "var(--accent-text)" }}>
                {view.jobs.map((j) => (
                  <div key={j.id}>{j.state === "running" ? `Working: ${j.stage || "starting"}${j.progress ? ` · ${j.progress}%` : ""}` : `Queued${j.lane === "gpu" ? " for AI" : ""}${j.langs?.length ? ` (${j.langs.join(", ")})` : ""}`}</div>
                ))}
              </div>
            )}

            <Section label="Languages you keep">
              {view.profile && (
                <p className="m-0 mb-2 text-xs text-ink-faint">
                  {view.profile.override ? <>This title uses the <b>{view.profile.name}</b> language profile (pinned in the Subtitles Library).</> : <>Language profile: <b>{view.profile.name}</b>.</>}{" "}
                  <Link to={LINKS.subtitlesSettings} style={{ color: "var(--accent-text)" }}>Edit profiles</Link>
                </p>
              )}
              <div className="flex flex-col gap-2">
                {view.languages.map((l) => (
                  <div key={l.lang + (l.variant ?? "")}>
                    <div className="flex flex-wrap items-center gap-1.5">
                      <WantChip l={l} sidecar={sidecarForWant(l, view.sidecars, view.kept[0] === l.lang)} />
                      {/* A forced want comes from the file's forced track or a foreign-parts
                          upload; search, AI and replace are for the full subtitle. */}
                      {l.variant !== "forced" && <span className="ml-auto flex flex-wrap gap-1.5">
                        <Button size="sm" onClick={() => setSearching((s) => (s === l.lang ? null : l.lang))} aria-expanded={searching === l.lang}>
                          {searching === l.lang ? "Close search" : "Search OpenSubtitles"}
                        </Button>
                        <Button size="sm" onClick={() => generate(l)}>Generate with AI</Button>
                        {l.have && <Button size="sm" variant="ghost" onClick={() => next(l)} title="Another download, an embedded track or AI — never the one there now">Replace…</Button>}
                      </span>}
                    </div>
                    {searching === l.lang && <SearchPanel media={media} lang={l.lang} langLabel={name(l.lang)} onQueued={() => { setSearching(null); void load(); }} />}
                  </div>
                ))}
              </div>
            </Section>

            <Section label={`Subtitle files · ${view.sidecars.length}`}>
              {view.sidecars.length === 0 ? (
                <p className="m-0 text-sm text-ink-dim">No subtitle files beside this video yet.</p>
              ) : (
                <div className="flex flex-col gap-2.5">
                  {view.sidecars.map((s) => <SidecarCard key={s.name} media={media} s={s} kept={view.kept} langLabel={name(s.lang)} onChanged={changed} toast={toast} />)}
                </div>
              )}
            </Section>

            {view.orphans.length > 0 && (
              <Section label="Not linked to this file" hint="Named for no video in this folder, so Plex won't show them with this file.">
                <div className="flex flex-col gap-1.5">
                  {view.orphans.map((o) => <OrphanRow key={o.name} media={media} name={o.name} lang={o.lang} onChanged={changed} toast={toast} />)}
                </div>
              </Section>
            )}

            <Section label={`Embedded tracks · ${view.embedded.length}`} hint="Inside the video. A text track can be copied out as the subtitle file; image tracks need OCR.">
              {view.embedded.length === 0 ? (
                <p className="m-0 text-sm text-ink-dim">None.</p>
              ) : (
                <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
                  {view.embedded.map((t) => (
                    <li key={t.index} className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
                      <span className="font-mono text-xs text-ink-faint">#{t.index}</span>
                      <span className="font-semibold">{t.lang && t.lang !== "und" ? name(t.lang) : "No language"}</span>
                      {t.title && <span className="truncate text-ink-dim">“{t.title}”</span>}
                      <Tag>{t.text ? t.codec : `${t.codec} · image`}</Tag>
                      {t.forced && <Tag>forced</Tag>}
                      {t.sdh && <Tag>SDH</Tag>}
                      {t.default && <Tag>default</Tag>}
                      {t.text && !t.forced && (
                        <Button size="sm" className="ml-auto" onClick={() => extract(t)} title={`Copy it out as the ${name(extractLang(t, view.kept))} subtitle`}>Extract</Button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </Section>

            {view.audio.length > 0 && (
              <Section label={`Audio · ${view.audio.length}`} hint={view.audio.length > 1 ? "Generate with AI listens to the track Arrmada picks for the language, unless you choose one." : undefined}>
                <div className="flex flex-wrap gap-1.5 text-sm text-ink-dim">
                  {view.audio.length > 1 && (
                    <button type="button" onClick={() => setAiStream(null)} aria-pressed={aiStream == null} className="rounded px-1.5 py-0.5 font-mono text-xs"
                      style={{ background: aiStream == null ? "var(--accent-soft)" : "var(--panel-2)", color: aiStream == null ? "var(--accent-text)" : "var(--ink-dim)" }}>
                      AI picks
                    </button>
                  )}
                  {view.audio.map((a) => {
                    const label = `#${a.index} ${a.lang ? name(a.lang) : "untagged"}${a.title ? ` “${a.title}”` : ""}${a.default ? " · default" : ""}`;
                    if (view.audio.length < 2) return <Tag key={a.index}>{label}</Tag>;
                    const on = aiStream === a.index;
                    return (
                      <button key={a.index} type="button" onClick={() => setAiStream(a.index)} aria-pressed={on} title="Generate with AI listens to this track"
                        className="rounded px-1.5 py-0.5 font-mono text-xs" style={{ background: on ? "var(--accent-soft)" : "var(--panel-2)", color: on ? "var(--accent-text)" : "var(--ink-dim)" }}>
                        {label}
                      </button>
                    );
                  })}
                </div>
              </Section>
            )}

            {view.attempts.length > 0 && (
              <Section label="Last attempts">
                <ul className="m-0 flex list-none flex-col gap-1 p-0">
                  {view.attempts.map((a) => (
                    <li key={a.id} className="flex flex-wrap items-baseline gap-x-2 text-sm">
                      <span className="font-mono text-2xs font-bold uppercase">{a.lang}{a.variant ? `.${a.variant}` : ""}</span>
                      <span className="text-ink-faint">{RUNG_LABEL[a.rung] ?? a.rung}</span>
                      <span style={{ color: a.outcome === "ok" ? "var(--good-text)" : "var(--ink-dim)" }}>{a.detail || REASON_LABEL[a.outcome] || a.outcome}</span>
                      <span className="ml-auto font-mono text-xs text-ink-faint">{formatAgo(a.at * 1000)}</span>
                    </li>
                  ))}
                </ul>
              </Section>
            )}
          </>
        )}
      </div>
    </Sheet>
  );
}

function Section({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <section>
      <div className={lbl}>{label}</div>
      {hint && <p className="m-0 mt-0.5 text-xs text-ink-faint">{hint}</p>}
      <div className="mt-2">{children}</div>
    </section>
  );
}

export function Tag({ children }: { children: ReactNode }) {
  return <span className="rounded px-1.5 py-0.5 font-mono text-xs" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{children}</span>;
}

// SidecarCard is one subtitle file: language and source, Health (with a sync check),
// where it came from, its first three lines, the timing nudges, and Delete.
function SidecarCard({ media, s, kept, langLabel, onChanged, toast }: {
  media: SubMediaRef; s: SubSidecar; kept: string[]; langLabel: string; onChanged: () => void; toast: ReturnType<typeof useToast>;
}) {
  const confirm = useConfirm();
  const [pending, setPending] = useState(0);
  const [busy, setBusy] = useState<null | "shift" | "delete" | "check" | "fix">(null);
  const [checkJob, setCheckJob] = useState<number | null>(null);
  useJob(checkJob, {
    giveUpMs: 30 * 60 * 1000, // a remux's soundtrack takes a minute or more to read
    onDone: (job) => { toast(jobToast(job, busy === "fix" ? "Sync fixed." : "Sync checked."), jobFailed(job) ? { tone: "error" } : undefined); setCheckJob(null); setBusy(null); onChanged(); },
  });
  const h = s.health ?? { state: "unchecked", note: "not checked" };
  // The language a check or redo names: the sidecar's own, or the first kept one when untagged.
  const lang = s.lang || kept[0] || "";
  const checkable = s.ext === ".srt" && s.variant !== "forced" && !!lang;

  const apply = async () => {
    setBusy("shift");
    try {
      await subtitleFileApi.shift(media, s.name, pending);
      toast(`Shifted ${s.name} by ${fmtShift(pending)}`);
      setPending(0);
      onChanged();
    } catch (e) { toast((e as Error).message, { tone: "error" }); } finally { setBusy(null); }
  };
  const check = async () => {
    setBusy("check");
    try {
      const r = await subtitleFileApi.syncCheck(media, lang, s.variant === "sdh" ? "sdh" : "");
      toast(`Checking the sync of ${r.file} — reading the soundtrack takes a minute`);
      if (r.job_id) setCheckJob(r.job_id); else setBusy(null);
    } catch (e) { toast((e as Error).message, { tone: "error" }); setBusy(null); }
  };
  const fix = async () => {
    setBusy("fix");
    try {
      const r = await subtitleFileApi.syncFix(media, lang, s.variant === "sdh" ? "sdh" : "", s.name);
      toast(`Measuring ${r.file} against the audio, then moving it into sync`);
      if (r.job_id) setCheckJob(r.job_id); else setBusy(null);
    } catch (e) { toast((e as Error).message, { tone: "error" }); setBusy(null); }
  };
  const remove = async () => {
    const ok = await confirm({
      title: `Delete ${s.name}?`,
      body: <>It goes to the recycle bin, so you can restore it from there.{s.provider_file_id ? " This download won't be fetched again for this file." : ""} The next run may make this language again from another source.</>,
      confirmLabel: "Move to recycle bin",
      tone: "danger",
    });
    if (!ok) return;
    setBusy("delete");
    try {
      await subtitleFileApi.remove(media, s.name);
      toast(`${s.name} is in the recycle bin`);
      onChanged();
    } catch (e) { toast((e as Error).message, { tone: "error" }); setBusy(null); }
  };

  const provenance = [
    s.release,
    s.hash_match ? "hash match" : s.score != null ? `score ${s.score}` : "",
    s.model,
    s.embedded_index != null ? `track #${s.embedded_index}` : "",
  ].filter(Boolean);
  const healthLabel = h.state === "unchecked" ? "Health not checked" : h.state === "unknown" ? "Health unknown" : `Health ${h.score ?? ""}`;

  return (
    <div className="rounded-lg p-3" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-base font-semibold">{langLabel}</span>
        {s.variant === "sdh" && <Tag>SDH</Tag>}
        {s.variant === "forced" && <Tag>forced</Tag>}
        <span className="rounded px-1.5 py-0.5 text-xs font-semibold" style={{ background: s.owned ? "var(--accent-soft)" : "var(--panel)", color: s.owned ? "var(--accent-text)" : "var(--ink-dim)", border: "1px solid var(--line-soft)" }}>
          {SOURCE_LABEL[s.source] ?? s.source}
        </span>
        <span className="rounded px-1.5 py-0.5 font-mono text-xs" style={{ color: HEALTH_COLOR[h.state] ?? "var(--ink-faint)", border: "1px solid var(--line-soft)" }} title={`How well its timing fits the speech (0-100): ${h.note}`}>
          {healthLabel}
        </span>
        {s.locked && <Tag>chosen · kept</Tag>}
        <span className="ml-auto font-mono text-xs text-ink-faint">{formatBytes(s.size)}</span>
      </div>
      <div className="mt-1 break-all font-mono text-xs text-ink-faint">{s.name}</div>
      {provenance.length > 0 && <div className="mt-0.5 text-xs text-ink-dim">{provenance.join(" · ")}</div>}
      {h.state !== "unchecked" && <div className="mt-0.5 text-xs text-ink-faint">{h.note}</div>}

      {s.first_cues.length > 0 ? (
        <ol className="m-0 mt-2 flex list-none flex-col gap-1 p-0">
          {s.first_cues.map((c, i) => (
            <li key={i} className="flex gap-2 text-sm leading-snug">
              <span className="w-[86px] flex-none font-mono text-xs" style={{ color: pending ? "var(--accent-text)" : "var(--ink-faint)" }}>{fmtCue(Math.max(0, c.start_ms + pending))}</span>
              <span className="min-w-0 flex-1">{c.text}</span>
            </li>
          ))}
          {s.cue_count > s.first_cues.length && <li className="font-mono text-xs text-ink-faint">… {s.cue_count} lines in all</li>}
        </ol>
      ) : (
        <p className="m-0 mt-2 text-sm text-ink-faint">{s.ext === ".srt" ? "No lines could be read." : "No preview for this format."}</p>
      )}

      <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
        {s.shiftable && (
          <>
            <span className="mr-0.5 text-xs text-ink-faint">Timing</span>
            {NUDGES.map((n) => (
              <Button key={n} size="sm" onClick={() => setPending((p) => p + n)} disabled={busy !== null} aria-label={`Move ${fmtShift(n)}`}>{fmtShift(n)}</Button>
            ))}
            {pending !== 0 && (
              <>
                <Button size="sm" variant="primary" onClick={apply} busy={busy === "shift"} busyLabel="Applying…">Apply {fmtShift(pending)}</Button>
                <Button size="sm" variant="ghost" onClick={() => setPending(0)} disabled={busy !== null}>Reset</Button>
              </>
            )}
          </>
        )}
        <span className="ml-auto flex gap-1.5">
          {checkable && pending === 0 && (
            <Button size="sm" variant="ghost" onClick={check} busy={busy === "check"} busyLabel="Checking…" disabled={busy !== null} title="Measure how well it lines up with the speech in the audio">Check sync</Button>
          )}
          {checkable && s.shiftable && pending === 0 && (
            <Button size="sm" variant="ghost" onClick={fix} busy={busy === "fix"} busyLabel="Fixing…" disabled={busy !== null} title="Measure it against the speech, then move it into sync. It's only moved when the measurement is sure.">Fix sync</Button>
          )}
          <Button size="sm" variant="ghost" onClick={remove} busy={busy === "delete"} busyLabel="Deleting…" disabled={busy !== null || pending !== 0} style={{ color: "var(--reject-text)" }}>Delete…</Button>
        </span>
      </div>
    </div>
  );
}

// OrphanRow is a subtitle named for no video: Adopt (movies) renames it to pair with
// this file; Delete sends it to the recycle bin.
function OrphanRow({ media, name, lang, onChanged, toast }: { media: SubMediaRef; name: string; lang?: string; onChanged: () => void; toast: ReturnType<typeof useToast> }) {
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const run = async (fn: () => Promise<string>) => {
    setBusy(true);
    try { toast(await fn()); onChanged(); } catch (e) { toast((e as Error).message, { tone: "error" }); } finally { setBusy(false); }
  };
  const adopt = () => run(async () => {
    if (media.kind !== "movie") return "";
    const r = await orphanApi.adopt(media.movie_id, name);
    return `Adopted — now ${r.name}`;
  });
  const remove = async () => {
    if (!(await confirm({ title: `Delete ${name}?`, body: "It goes to the recycle bin, so you can restore it from there.", confirmLabel: "Move to recycle bin", tone: "danger" }))) return;
    await run(async () => { await subtitleFileApi.remove(media, name); return `${name} is in the recycle bin`; });
  };
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-1.5 text-sm">
      <span className="min-w-0 flex-1 truncate font-mono text-ink-dim" title={name}>{name}</span>
      {lang && <Tag>{lang}</Tag>}
      {media.kind === "movie" && <Button size="sm" onClick={adopt} disabled={busy} title="Rename it to pair with this file, so Plex shows it" style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)", background: "transparent" }}>Adopt</Button>}
      <Button size="sm" variant="ghost" onClick={remove} disabled={busy} style={{ color: "var(--reject-text)" }}>Delete…</Button>
    </div>
  );
}
