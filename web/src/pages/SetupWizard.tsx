import { useCallback, useEffect, useState } from "react";
import { api, type APIKeyStatus, type LibraryPaths, type SetupState } from "../lib/api";
import { FolderChips, FolderPicker } from "./Library";
import { restartAndWait } from "../lib/restart";
import { FleetMark } from "../components/FleetMark";

// SetupWizard is the first thing an admin sees on a fresh install: the metadata key,
// then each library folder (pre-filled from what's on the mount), then a restart so
// the importer and qBittorrent start using those folders. Everything is optional and
// can be changed later in Settings; "Skip setup" never nags again.

type Step = "keys" | "folders" | "finish";

const FOLDERS: { key: keyof LibraryPaths; label: string; hint: string; optional?: boolean }[] = [
  { key: "movies", label: "Movies", hint: "Where movie folders live" },
  { key: "tv", label: "TV shows", hint: "Where show folders live" },
  { key: "downloads", label: "Downloads", hint: "Where qBittorrent saves downloads" },
  { key: "ebooks", label: "Ebooks", hint: "Optional — only if you use Books", optional: true },
  { key: "audiobooks", label: "Audiobooks", hint: "Optional — can be the same as Ebooks", optional: true },
  { key: "music", label: "Music", hint: "Optional — only if you use Music", optional: true },
];

const OPTIONAL_KEYS = ["hardcover", "omdb", "tvdb"];

export function SetupWizard({ onDone }: { onDone: () => void }) {
  const [state, setState] = useState<SetupState | null>(null);
  const [step, setStep] = useState<Step>("keys");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [keys, setKeys] = useState<Record<string, string>>({});
  const [folders, setFolders] = useState<LibraryPaths | null>(null);
  const [picking, setPicking] = useState<keyof LibraryPaths | null>(null);
  const [restarting, setRestarting] = useState(false);
  const [blocking, setBlocking] = useState<Partial<Record<keyof LibraryPaths, boolean>>>({});
  const onBlocking = useCallback((k: keyof LibraryPaths, b: boolean) => setBlocking((cur) => (cur[k] === b ? cur : { ...cur, [k]: b })), []);
  const folderBlocked = Object.values(blocking).some(Boolean);

  const load = () => api.setupState().then((s) => {
    setState(s);
    // Pre-fill each folder: what's already chosen, else what the mount looks like it holds.
    setFolders((cur) => cur ?? Object.fromEntries(FOLDERS.map((f) => [
      f.key, s.libraries_chosen ? s.library[f.key] : (s.suggestions[f.key] ?? s.library[f.key] ?? ""),
    ])) as unknown as LibraryPaths);
  }).catch((e) => setError((e as Error).message));
  useEffect(() => { load(); }, []);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true); setError(null);
    try { await fn(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  };

  const finish = () => run(async () => { await api.completeSetup(); onDone(); });

  const saveKeys = () => run(async () => {
    for (const [id, v] of Object.entries(keys)) {
      if (v.trim()) await api.setAPIKey(id, v.trim());
    }
    await load();
    setStep("folders");
  });

  const saveFolders = () => run(async () => {
    if (folders) await api.setLibraryPaths(folders);
    await load();
    setStep("finish");
  });

  // Restart and wait for the NEW process (not the old one's last answers), then finish.
  const restart = () => run(async () => {
    setRestarting(true);
    try {
      await restartAndWait({ then: async () => { await api.completeSetup(); window.location.reload(); } });
    } catch (e) {
      setRestarting(false);
      throw e;
    }
  });

  if (!state || !folders) {
    return <Frame><p className="text-[13px] text-ink-dim">{error ?? "Loading…"}</p></Frame>;
  }

  const keyStatus = (id: string): APIKeyStatus | undefined => state.keys.find((k) => k.id === id);
  const tmdb = keyStatus("tmdb");
  const managedOnly = state.mounts.length > 0 && !state.mounts.includes("/storage");

  return (
    <Frame>
      <div className="mb-5 flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wide text-ink-faint">
        {(["keys", "folders", "finish"] as Step[]).map((s, i) => (
          <span key={s} style={{ color: s === step ? "var(--accent)" : undefined }}>
            {i > 0 && <span className="mx-1.5 text-ink-faint">›</span>}
            {i + 1}. {s === "keys" ? "Metadata" : s === "folders" ? "Folders" : "Finish"}
          </span>
        ))}
      </div>

      {error && <div className="mb-3 rounded-lg p-2.5 text-[12px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>}

      {step === "keys" && (
        <>
          <h1 className="m-0 text-[20px] font-bold">Welcome to Arrmada</h1>
          <p className="mb-4 mt-1 text-[13px] text-ink-dim">Two quick steps and you're ready. Everything here can be changed later in Settings.</p>

          <KeyField
            status={tmdb}
            label="TMDB API key"
            note="Needed for Movies and TV — titles, artwork and Discover. It's free."
            value={keys.tmdb ?? ""}
            onChange={(v) => setKeys({ ...keys, tmdb: v })}
          />
          <details className="mt-3">
            <summary className="cursor-pointer text-[12px] font-semibold text-ink-dim">More keys (all optional)</summary>
            <div className="mt-2 flex flex-col gap-3">
              {OPTIONAL_KEYS.map((id) => {
                const s = keyStatus(id);
                return s ? <KeyField key={id} status={s} label={s.label} note={s.purpose} value={keys[id] ?? ""} onChange={(v) => setKeys({ ...keys, [id]: v })} /> : null;
              })}
            </div>
          </details>
          <Nav busy={busy} onSkip={finish} onNext={saveKeys} nextLabel={Object.values(keys).some((v) => v.trim()) ? "Save and continue" : "Continue"} />
        </>
      )}

      {step === "folders" && (
        <>
          <h1 className="m-0 text-[20px] font-bold">Where's your media?</h1>
          <p className="mb-1 mt-1 text-[13px] text-ink-dim">
            Pick a folder for each library. {state.libraries_chosen ? "These are your current folders." : "We've filled in anything that looked right — check it."}
          </p>
          <p className="mb-4 text-[11.5px] text-ink-faint">
            Keep Downloads on the same drive or share as your libraries, so finished downloads are hardlinked instantly instead of copied.
          </p>
          {managedOnly && (
            <div className="mb-3 rounded-lg p-2.5 text-[12px]" style={{ border: "1px solid var(--avoid)", color: "var(--avoid)" }}>
              Arrmada is using its own managed storage — no media folder was given at install. To use your existing library, re-run the installer with your media folder.
            </div>
          )}
          <div className="flex flex-col gap-2.5">
            {FOLDERS.map((f) => (
              <div key={f.key}>
                <div className="mb-1 flex items-baseline justify-between gap-2">
                  <span className="text-[12.5px] font-semibold">{f.label}</span>
                  <span className="text-[11px] text-ink-faint">{f.hint}</span>
                </div>
                <div className="flex gap-2">
                  <input
                    value={folders[f.key]}
                    onChange={(e) => setFolders({ ...folders, [f.key]: e.target.value })}
                    placeholder={f.optional ? "Leave as is if you don't use it" : "/storage/…"}
                    className="min-w-0 flex-1 rounded-lg px-3 py-2 font-mono text-[12px]"
                    style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
                  />
                  <button onClick={() => setPicking(f.key)} className="flex-none rounded-lg px-3 py-2 text-[12px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Browse</button>
                </div>
                <FolderChips
                  kind={f.key}
                  path={folders[f.key]}
                  current={state.library[f.key] ?? ""}
                  downloads={folders.downloads}
                  onBlocking={onBlocking}
                  onCreated={(saved) => setFolders((cur) => (cur ? { ...cur, [f.key]: saved[f.key] } : cur))}
                />
              </div>
            ))}
          </div>
          <Nav busy={busy} blocked={folderBlocked} onBack={() => setStep("keys")} onSkip={finish} onNext={saveFolders} nextLabel="Save and continue" />
          {picking && (
            <FolderPicker
              initial={folders[picking] || state.mounts[0]}
              onClose={() => setPicking(null)}
              onSelect={(p) => { setFolders({ ...folders, [picking]: p }); setPicking(null); }}
            />
          )}
        </>
      )}

      {step === "finish" && (
        <>
          <h1 className="m-0 text-[20px] font-bold">{state.restart_needed ? "One restart to go" : "You're all set"}</h1>
          {state.restart_needed ? (
            <p className="mt-1 text-[13px] text-ink-dim">
              Arrmada needs to restart to start using the new folders for downloads and imports.
              {state.can_restart ? " It takes a few seconds." : " Restart the Arrmada container the way you started it (for example docker compose restart arrmada-app)."}
            </p>
          ) : (
            <p className="mt-1 text-[13px] text-ink-dim">Your folders are in use.</p>
          )}
          {!state.tmdb_configured && (
            <p className="mt-2 text-[12px]" style={{ color: "var(--avoid)" }}>No TMDB key yet — Movies and TV won't find anything until you add one in Settings → API keys.</p>
          )}
          <div className="mt-4 rounded-xl p-3.5 text-[12.5px] text-ink-dim" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
            <div className="mb-1 font-semibold text-[var(--ink)]">Next:</div>
            <ul className="m-0 list-disc pl-5">
              <li>Add your trackers on the <b>Indexers</b> page (or sync them from Prowlarr).</li>
              <li>Check the default <b>Quality</b> profile.</li>
              <li>Import what's already on disk from <b>Settings → Library</b>.</li>
            </ul>
          </div>
          <div className="mt-5 flex justify-end gap-2">
            {state.restart_needed && state.can_restart ? (
              <button onClick={restart} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-60" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>
                {restarting ? "Restarting…" : "Restart and finish"}
              </button>
            ) : (
              <button onClick={finish} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-60" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>Open Arrmada</button>
            )}
          </div>
        </>
      )}
    </Frame>
  );
}

function Frame({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-full items-start justify-center overflow-y-auto px-4 py-10 font-sans">
      <div className="w-full max-w-[600px]">
        <div className="mb-6 flex items-center gap-2.5">
          <span className="grid h-[30px] w-[30px] place-items-center rounded-lg" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>
            <FleetMark className="h-[17px] w-[17px]" />
          </span>
          <span className="text-[15px] font-extrabold tracking-[0.12em]">ARRMADA</span>
        </div>
        <div className="rounded-2xl p-6" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}>{children}</div>
      </div>
    </div>
  );
}

function KeyField({ status, label, note, value, onChange }: { status?: APIKeyStatus; label: string; note: string; value: string; onChange: (v: string) => void }) {
  return (
    <label className="block">
      <div className="mb-1 flex items-baseline justify-between gap-2">
        <span className="text-[12.5px] font-semibold">{label}</span>
        {status?.configured && <span className="text-[11px]" style={{ color: "var(--good)" }}>✓ saved{status.hint ? ` (…${status.hint})` : ""}</span>}
      </div>
      <input
        type={status?.secret === false ? "text" : "password"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={status?.configured ? "Leave blank to keep the saved key" : "Paste the key"}
        autoComplete="off"
        className="block w-full rounded-lg px-3 py-2 font-mono text-[12px]"
        style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
      />
      <span className="mt-1 block text-[11px] text-ink-faint">
        {note}{status?.help_url && <> · <a href={status.help_url} target="_blank" rel="noreferrer" className="underline" style={{ color: "var(--accent)" }}>get one</a></>}
      </span>
    </label>
  );
}

function Nav({ busy, blocked, onBack, onSkip, onNext, nextLabel }: { busy: boolean; blocked?: boolean; onBack?: () => void; onSkip: () => void; onNext: () => void; nextLabel: string }) {
  return (
    <div className="mt-6 flex items-center justify-between gap-2">
      <button onClick={onSkip} disabled={busy} className="text-[12px] text-ink-faint hover:text-[var(--ink)]" title="Finish now; everything can be set later in Settings">Skip setup</button>
      <div className="flex gap-2">
        {onBack && <button onClick={onBack} disabled={busy} className="rounded-lg px-3.5 py-2 text-[12.5px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Back</button>}
        <button onClick={onNext} disabled={busy || blocked} title={blocked ? "Fix the folders marked in red first" : undefined} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-60" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>{busy ? "Saving…" : nextLabel}</button>
      </div>
    </div>
  );
}
