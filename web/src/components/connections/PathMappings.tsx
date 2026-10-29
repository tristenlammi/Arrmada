import { useState } from "react";
import { type DownloadClient, type NewDownloadClient, type PathMap } from "../../lib/api";
import { downloadClientApi, type PathCheck, type PathReport } from "../../lib/downloadClientApi";
import { clientKindName } from "../../lib/network";
import { Button } from "../../ui";

// PathMappings sits in a download client's editor: the folders it sees under another path
// than Arrmada does (a seedbox, or qBittorrent or SABnzbd on another machine), and a check
// of which of its paths Arrmada can actually see. Adding or removing a mapping saves at
// once; there is no Save button. The bundled qBittorrent shares Arrmada's folders, so it
// only gets the check.

const field = "w-full min-w-0 rounded-lg px-3 py-2 font-mono text-sm";
const fieldStyle = { background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

const WHAT: Record<string, string> = { save_folder: "Save folder", newest_download: "Newest finished download", complete_folder: "Completed folder", category_folder: "Category folder", incomplete_folder: "Incomplete folder" };

/** A path check's name: "Completed folder", or "Category folder arrmada-tv". */
export function pathCheckName(c: PathCheck): string {
  const what = WHAT[c.what] ?? c.what;
  return c.category ? `${what} ${c.category}` : what;
}

// PathCheckLine is one path a client reported and whether Arrmada sees it: ✓ where it is,
// – for a download that's gone, ✕ for a folder Arrmada can't see. An advisory path
// (qBittorrent's incomplete folder) is only information — imports wait for the move out
// of it — so it never reads as a failure: ⓘ in the caution colour, whether or not
// Arrmada can see it, and the report's summary says what to change. The usenet card's
// completed folder uses it too (seesAt: "Arrmada sees it at …"), so both say it alike.
export function PathCheckLine({ c, seesAt }: { c: PathCheck; seesAt?: boolean }) {
  if (c.advisory) {
    return (
      <div className="break-all text-sm" style={{ color: "var(--avoid-text)" }}>
        ⓘ <span className="text-ink-dim">{pathCheckName(c)}:</span>{" "}
        <span className="font-mono">{c.remote}</span>
        {c.local !== c.remote && <> <span className="text-ink-faint">→</span> <span className="font-mono">{c.local}</span></>}
        <span className="text-ink-dim"> (Arrmada doesn't need to see it — imports wait until a torrent leaves it)</span>
      </div>
    );
  }
  const tone = c.visible ? "var(--good-text)" : c.gone ? "var(--avoid-text)" : "var(--reject-text)";
  const mark = c.visible ? "✓" : c.gone ? "–" : "✕";
  if (seesAt) {
    return (
      <div className="break-all text-sm" style={{ color: tone }}>
        {mark} <span className="text-ink-dim">{pathCheckName(c)}</span> <span className="font-mono">{c.remote}</span>
        <span className="text-ink-dim">{c.visible ? " · Arrmada sees it at " : " · Arrmada can't see it at "}</span>
        <span className="font-mono">{c.local}</span>
      </div>
    );
  }
  return (
    <div className="break-all text-sm" style={{ color: tone }}>
      {mark} <span className="text-ink-dim">{pathCheckName(c)}:</span>{" "}
      <span className="font-mono">{c.remote}</span>
      {c.local !== c.remote && <> <span className="text-ink-faint">→</span> <span className="font-mono">{c.local}</span></>}
      {!c.visible && <span className="text-ink-dim">{c.gone ? " (not there any more: moved or deleted)" : " (Arrmada can't see it)"}</span>}
    </div>
  );
}

export function PathMappings({ client, body, onChanged }: {
  client: DownloadClient;
  /** The client's saved settings as a PUT body with these mappings (password blank = kept). */
  body: (maps: PathMap[]) => NewDownloadClient;
  onChanged?: () => void;
}) {
  const who = clientKindName(client.kind);
  const [maps, setMaps] = useState<PathMap[]>(client.path_maps ?? []);
  const [remote, setRemote] = useState("");
  const [local, setLocal] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const [report, setReport] = useState<PathReport | null>(null);
  const [checkError, setCheckError] = useState<string | null>(null);

  const save = async (next: PathMap[]) => {
    setSaving(true);
    setError(null);
    try {
      const c = await downloadClientApi.updateDownloadClient(client.id, body(next));
      setMaps(c.path_maps ?? []);
      setReport(null); // it answered for the old mappings
      onChanged?.();
      return true;
    } catch (e) {
      setError((e as Error).message);
      return false;
    } finally {
      setSaving(false);
    }
  };

  const add = async (pm: PathMap) => {
    // A mapping for the same remote folder is replaced, not doubled.
    const next = [...maps.filter((m) => m.remote.replace(/[\\/]+$/, "") !== pm.remote.replace(/[\\/]+$/, "")), pm];
    if (await save(next)) {
      setRemote("");
      setLocal("");
    }
  };

  const check = async () => {
    setChecking(true);
    setCheckError(null);
    setReport(null);
    try {
      const r = await downloadClientApi.testDownloadClient(client.id);
      if (!r.ok) setCheckError(r.error ?? "It didn't answer.");
      else if (r.paths) setReport(r.paths);
      else setCheckError(r.paths_error ? `Connected, but it couldn't say where it saves: ${r.paths_error}` : "Connected; this client can't say where it saves.");
    } catch (e) {
      setCheckError((e as Error).message);
    } finally {
      setChecking(false);
    }
  };

  return (
    <section aria-label="Path mappings" className="mt-2 rounded-lg p-3.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-base font-semibold">{client.bundled ? "Paths" : "Path mappings"}</div>
        <Button size="sm" onClick={check} busy={checking} busyLabel="Checking…">Check paths</Button>
      </div>
      {!client.bundled && (
        <>
          <p className="mb-0 mt-1 text-xs text-ink-faint">
            Only for a {who} that sees its downloads under another path than Arrmada, such as a seedbox or another machine. Left, the folder as {who} names it; right, the same folder inside the Arrmada container.
          </p>
          {maps.length > 0 && (
            <ul className="m-0 mt-2.5 flex list-none flex-col gap-1.5 p-0">
              {maps.map((m) => (
                <li key={m.remote} className="flex min-w-0 items-center gap-2 text-sm">
                  <span className="min-w-0 flex-1 break-all font-mono">
                    {m.remote} <span className="text-ink-faint">→</span> {m.local}
                  </span>
                  <Button size="sm" variant="ghost" disabled={saving} aria-label={`Remove the mapping for ${m.remote}`} onClick={() => void save(maps.filter((x) => x !== m))}>
                    Remove
                  </Button>
                </li>
              ))}
            </ul>
          )}
          <form
            aria-label="Add a path mapping"
            className="mt-2.5 grid grid-cols-1 items-end gap-2 sm:grid-cols-[1fr_1fr_auto]"
            onSubmit={(e) => { e.preventDefault(); if (remote.trim() && local.trim()) void add({ remote: remote.trim(), local: local.trim() }); }}
          >
            <label className="flex min-w-0 flex-col gap-1">
              <span className="font-mono text-2xs font-bold uppercase tracking-[0.1em] text-ink-faint">{who}'s path</span>
              <input className={field} style={fieldStyle} value={remote} onChange={(e) => setRemote(e.target.value)} placeholder="/downloads" autoComplete="off" />
            </label>
            <label className="flex min-w-0 flex-col gap-1">
              <span className="font-mono text-2xs font-bold uppercase tracking-[0.1em] text-ink-faint">Arrmada's path</span>
              <input className={field} style={fieldStyle} value={local} onChange={(e) => setLocal(e.target.value)} placeholder="/media/downloads" autoComplete="off" />
            </label>
            <Button type="submit" size="md" disabled={saving || !remote.trim() || !local.trim()} busy={saving} busyLabel="Saving…">Add mapping</Button>
          </form>
          {error && <p role="alert" className="mb-0 mt-2 text-sm" style={{ color: "var(--reject-text)" }}>{error}</p>}
        </>
      )}

      {checkError && <p role="alert" className="mb-0 mt-2.5 break-words text-sm" style={{ color: "var(--reject-text)" }}>{checkError}</p>}
      {report && (
        <div className="mt-2.5 flex flex-col gap-1.5" aria-live="polite">
          {report.checks.map((c) => <PathCheckLine key={`${c.what}:${c.category ?? ""}`} c={c} />)}
          <p className="m-0 break-words text-sm text-ink-dim">{report.summary}</p>
          {report.suggestion && !client.bundled && (
            <div className="flex flex-wrap items-center gap-2">
              <span className="break-all font-mono text-sm">{report.suggestion.remote} → {report.suggestion.local}</span>
              <Button size="sm" variant="primary" disabled={saving} onClick={() => void add(report.suggestion as PathMap)}>Use this mapping</Button>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
