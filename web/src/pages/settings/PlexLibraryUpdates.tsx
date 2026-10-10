import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Note, Section, Toggle, input, inputStyle } from "../../components/settings/ui";
import { LINKS } from "../../lib/links";
import { plexApi, type PlexPathMap, type PlexScanRoot, type PlexScanView } from "../../lib/plexApi";
import { ago } from "../../lib/taskTime";
import { Button } from "../../ui";

// The "Plex library updates" card: after Arrmada imports, upgrades, renames, deletes or
// converts a file it asks Plex to scan just that folder. Each library folder's line shows
// where its scans go on Plex's side and how that was worked out, so a wrong guess is
// visible. Self-contained (its own load and save), so the Plex settings page can host it
// as is; for now it sits in Settings → Library.

export const PLEX_LIBRARY_UPDATES_ID = "plex-library-updates";

const KIND_LABEL: Record<string, string> = { movie: "Movies", show: "TV" };

function howLabel(r: PlexScanRoot): string {
  switch (r.how) {
    case "direct": return "same path";
    case "mapped": return "your mapping";
    case "guessed": return "found automatically";
    case "section": return "whole library";
    case "none": return "no Plex library";
    default: return "";
  }
}

function RootLine({ r, busy, onTest }: { r: PlexScanRoot; busy: string; onTest: (kind: "movie" | "show", run: boolean) => void }) {
  const label = KIND_LABEL[r.kind] ?? r.kind;
  if (!r.arrmada_root) {
    return <div className="text-[12px] text-ink-faint">{label}: no folder set</div>;
  }
  const target = r.how === "section" || !r.plex_path ? null : r.plex_path;
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px]">
        <span className="font-semibold">{label}:</span>
        <span className="font-mono text-[11.5px]">{r.arrmada_root}</span>
        {r.how && r.how !== "none" && (
          <>
            <span className="text-ink-faint">→</span>
            <span className="font-mono text-[11.5px]" style={{ color: "var(--accent)" }}>{target ?? "the whole library"}</span>
            <span className="text-ink-faint">
              · {r.sections.length === 1 ? "section" : "sections"} {r.sections.join(", ")} ({howLabel(r)})
            </span>
          </>
        )}
        <span className="ml-auto flex gap-1.5">
          <Button size="sm" busy={busy === `${r.kind}:test`} onClick={() => onTest(r.kind, false)}>Test</Button>
          <Button size="sm" busy={busy === `${r.kind}:run`} busyLabel="Scanning…" onClick={() => onTest(r.kind, true)}>Scan now</Button>
        </span>
      </div>
      {r.how === "section" && (
        <Note tone="warn">
          Arrmada couldn't match {r.arrmada_root} to a folder Plex knows, so each change scans the whole {r.sections.join(", ")} library.
          That works, but it's slow on a big library: add a path mapping below.{r.note ? ` (${r.note})` : ""}
        </Note>
      )}
      {r.how === "none" && <Note tone="warn">{r.note || `Plex has no ${label} library.`}</Note>}
    </div>
  );
}

export function PlexLibraryUpdates() {
  const [view, setView] = useState<PlexScanView | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [maps, setMaps] = useState<PlexPathMap[]>([]);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState("");
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);

  const apply = (v: PlexScanView) => {
    setView(v);
    setEnabled(v.enabled);
    setMaps(v.path_map);
    setDirty(false);
  };
  useEffect(() => {
    plexApi.scanView().then(apply).catch((e: Error) => setLoadErr(e.message));
  }, []);

  if (loadErr) return <Section id={PLEX_LIBRARY_UPDATES_ID} title="Plex library updates" subtitle="Tell Plex what changed."><Note tone="warn">{loadErr}</Note></Section>;
  if (!view) return <Section id={PLEX_LIBRARY_UPDATES_ID} title="Plex library updates" subtitle="Tell Plex what changed."><div className="text-[12px] text-ink-faint">Loading…</div></Section>;

  const edit = (next: PlexPathMap[]) => { setMaps(next); setDirty(true); };
  const save = async () => {
    setBusy("save");
    setMsg(null);
    try {
      apply(await plexApi.saveScan({ enabled, path_map: maps.filter((m) => m.from.trim() || m.to.trim()) }));
      setMsg({ tone: "ok", text: "Saved." });
    } catch (e) {
      setMsg({ tone: "err", text: (e as Error).message });
    } finally {
      setBusy("");
    }
  };
  const test = async (kind: "movie" | "show", run: boolean) => {
    setBusy(`${kind}:${run ? "run" : "test"}`);
    setMsg(null);
    try {
      const res = await plexApi.testScan(kind, run);
      setView((v) => v && { ...v, roots: v.roots.map((r) => (r.kind === kind ? res.root : r)), last_scan: run ? res.last_scan : v.last_scan });
      setMsg({ tone: "ok", text: run ? `Asked Plex to scan ${res.root.plex_path || "the whole library"}.` : `${KIND_LABEL[kind]} resolves to ${res.root.plex_path || "the whole library"} (${howLabel(res.root)}).` });
    } catch (e) {
      setMsg({ tone: "err", text: (e as Error).message });
    } finally {
      setBusy("");
    }
  };

  const last = view.last_scan;
  return (
    <Section
      id={PLEX_LIBRARY_UPDATES_ID}
      title="Plex library updates"
      subtitle="After Arrmada imports, upgrades, renames, deletes or converts a file, it asks Plex to scan just that folder, so the change shows up in Plex within a minute. Arrmada's folders usually have different names inside Plex's container; each line shows where its scans go."
    >
      {!view.configured && (
        <Note tone="info">
          Connect your Plex server first, in <Link to={LINKS.plexConnection} style={{ color: "var(--accent)" }}>Insights → Settings</Link>. Until then nothing is sent.
        </Note>
      )}
      <Toggle
        label="Tell Plex to scan after changes"
        hint="One quick scan of the changed folder, a few seconds after things settle. Off leaves it to Plex's own watcher and scheduled scans."
        checked={enabled}
        onChange={(v) => { setEnabled(v); setDirty(true); }}
      />
      {view.configured && view.error && <Note tone="warn">Couldn't read Plex's libraries: {view.error}</Note>}
      {view.configured && !view.error && (
        <div className="flex flex-col gap-3">
          {view.roots.map((r) => <RootLine key={r.kind} r={r} busy={busy} onTest={test} />)}
        </div>
      )}

      <div className="flex flex-col gap-2">
        <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Path mappings</span>
        <span className="text-[10.5px] text-ink-faint">Only needed when a line above says "whole library" or points somewhere wrong. The Arrmada folder is as Arrmada sees it; the Plex folder is the same place as Plex sees it (for example /data/media/movies, or D:\Media\Movies on Windows).</span>
        {maps.map((m, i) => (
          <div key={i} className="flex flex-wrap items-center gap-2">
            <input aria-label="Arrmada folder" placeholder="/movies" value={m.from} onChange={(e) => edit(maps.map((x, j) => (j === i ? { ...x, from: e.target.value } : x)))} className={`${input} min-w-0 flex-1`} style={inputStyle} />
            <span className="text-ink-faint">→</span>
            <input aria-label="Plex folder" placeholder="/data/media/movies" value={m.to} onChange={(e) => edit(maps.map((x, j) => (j === i ? { ...x, to: e.target.value } : x)))} className={`${input} min-w-0 flex-1`} style={inputStyle} />
            <Button size="sm" variant="ghost" onClick={() => edit(maps.filter((_, j) => j !== i))}>Remove</Button>
          </div>
        ))}
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" onClick={() => edit([...maps, { from: "", to: "" }])}>Add mapping</Button>
          <Button size="sm" variant="primary" disabled={!dirty} busy={busy === "save"} busyLabel="Saving…" onClick={save}>Save</Button>
        </div>
      </div>

      <div className="text-[11.5px] text-ink-dim">
        {last ? (
          last.error
            ? <span style={{ color: "var(--reject-text)" }}>Last scan {ago(last.at)} failed: {last.error}</span>
            : <>Last scan: {ago(last.at)} · <span className="font-mono">{last.path || "whole library"}</span>{last.section ? ` · ${last.section}` : ""}</>
        ) : "No scans asked for since Arrmada started."}
        {view.pending > 0 && ` · ${view.pending} waiting`}
      </div>
      {msg && <div className="text-[11.5px]" style={{ color: msg.tone === "err" ? "var(--reject-text)" : "var(--good-text)" }}>{msg.text}</div>}
    </Section>
  );
}
