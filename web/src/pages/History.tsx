import { useEffect, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type ImportRecord } from "../lib/api";

function bytes(n: number): string {
  if (n <= 0) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1);
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : 1)} ${u[i]}`;
}

export function History() {
  const [items, setItems] = useState<ImportRecord[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .history()
        .then((h) => alive && (setItems(h), setError(null), setLoaded(true)))
        .catch((e: Error) => alive && (setError(e.message), setLoaded(true)));
    load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  return (
    <>
      <PageHeader title="History" crumb="Imported to library" />
      <div className="mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6">
        {error && (
          <div className="mb-3 rounded-lg p-3 text-[12.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>
            {error}
          </div>
        )}

        {loaded && items.length === 0 && !error ? (
          <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            Nothing imported yet. Grab a release; when it finishes downloading, Arrmada organizes it into your library and it appears here.
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            {items.map((it) => (
              <div key={it.hash} className="rounded-xl p-4" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
                <div className="flex items-center gap-3">
                  <span className="grid h-8 w-8 place-items-center rounded-lg" style={{ background: "var(--good-soft)", color: "var(--good)" }}>
                    <svg width="16" height="16" viewBox="0 0 24 24" fill="none"><path d="M4 12l5 5L20 6" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" /></svg>
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[13px] font-semibold">{it.title || "Imported"}</div>
                    <div className="truncate font-mono text-[10.5px] text-ink-faint" title={it.target_path}>{it.target_path}</div>
                  </div>
                  <span className="font-mono text-[11px] text-ink-dim">{bytes(it.size_bytes)}</span>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}
