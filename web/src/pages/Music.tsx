import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type Artist, type ArtistLookup } from "../lib/api";

const FILTERS = [
  { key: "all", label: "All" },
  { key: "monitored", label: "Monitored" },
  { key: "incomplete", label: "Incomplete" },
  { key: "complete", label: "Complete" },
] as const;
type FilterKey = (typeof FILTERS)[number]["key"];

function have(a: Artist): number {
  return a.stats?.have_tracks ?? 0;
}
function total(a: Artist): number {
  return a.stats?.tracks ?? 0;
}
function isComplete(a: Artist): boolean {
  return total(a) > 0 && have(a) >= total(a);
}

function matches(a: Artist, f: FilterKey): boolean {
  switch (f) {
    case "monitored":
      return a.monitored;
    case "incomplete":
      return !isComplete(a);
    case "complete":
      return isComplete(a);
    default:
      return true;
  }
}

export function Music() {
  const [artists, setArtists] = useState<Artist[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<FilterKey>("all");
  const [query, setQuery] = useState("");
  const [adding, setAdding] = useState(false);
  const [toast, setToast] = useState<string | null>(null);

  const flash = (m: string) => {
    setToast(m);
    window.setTimeout(() => setToast(null), 3500);
  };
  const load = () =>
    api
      .artists()
      .then(setArtists)
      .catch((e: Error) => {
        setError(e.message);
        setArtists([]);
      });
  useEffect(() => {
    load();
  }, []);

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (artists ?? [])
      .filter((a) => matches(a, filter))
      .filter((a) => !q || a.name.toLowerCase().includes(q));
  }, [artists, filter, query]);

  return (
    <>
      <PageHeader title="Music" crumb="Library / Music" />
      <div className="mx-auto w-full max-w-[1360px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex flex-wrap items-center gap-2">
          {FILTERS.map((f) => {
            const on = filter === f.key;
            return (
              <button
                key={f.key}
                onClick={() => setFilter(f.key)}
                className="rounded-full px-3 py-1 text-[12px] font-semibold"
                style={{
                  border: `1px solid ${on ? "var(--accent)" : "var(--line)"}`,
                  background: on ? "var(--accent-soft)" : "var(--panel)",
                  color: on ? "var(--accent)" : "var(--ink-faint)",
                }}
              >
                {f.label}
              </button>
            );
          })}
          <div className="ml-auto flex items-center gap-2">
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Filter artists…"
              className="w-[200px] rounded-lg px-3 py-1.5 text-[12px]"
              style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
            />
            <button
              onClick={() => setAdding(true)}
              className="rounded-lg px-4 py-2 text-[12.5px] font-semibold"
              style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
            >
              Add artist
            </button>
          </div>
        </div>

        {error && (
          <div className="mb-4 rounded-lg p-3 text-[12px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>
            {error}
          </div>
        )}

        {artists === null ? (
          <p className="text-[12.5px] text-ink-dim">Loading…</p>
        ) : shown.length === 0 ? (
          <div className="rounded-xl p-10 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            {artists.length === 0 ? "No artists yet. Add one to get started." : "No artists match this filter."}
          </div>
        ) : (
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(240px, 1fr))" }}>
            {shown.map((a) => (
              <ArtistCard key={a.id} a={a} />
            ))}
          </div>
        )}
      </div>

      {adding && (
        <AddArtistModal
          onClose={() => setAdding(false)}
          onAdded={(name) => {
            load();
            flash(`Added ${name}.`);
          }}
        />
      )}
      {toast && (
        <div
          className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium"
          style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}
        >
          {toast}
        </div>
      )}
    </>
  );
}

function ArtistCard({ a }: { a: Artist }) {
  const done = have(a);
  const all = total(a);
  const pct = all > 0 ? Math.round((done / all) * 100) : 0;
  const tone = all === 0 ? "var(--ink-faint)" : done >= all ? "var(--good)" : a.monitored ? "var(--avoid)" : "var(--ink-faint)";
  return (
    <Link
      to={`/music/${a.id}`}
      className="flex flex-col gap-2 rounded-xl p-3.5 transition-colors hover:bg-[var(--panel-2)]"
      style={{ background: "var(--panel)", border: "1px solid var(--line)" }}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="truncate text-[13.5px] font-semibold">{a.name}</div>
          <div className="mt-0.5 truncate text-[11px] text-ink-faint">
            {a.stats?.albums ?? 0} album{(a.stats?.albums ?? 0) === 1 ? "" : "s"}
            {a.genres && a.genres.length > 0 && <> · {a.genres[0]}</>}
          </div>
        </div>
        <span className="flex-none rounded-full px-2 py-0.5 font-mono text-[10px] font-semibold uppercase" style={{ background: "var(--panel-2)", color: tone }}>
          {a.monitored ? "monitored" : "paused"}
        </span>
      </div>
      <div className="flex items-center gap-2">
        <div className="h-1.5 flex-1 overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
          <div className="h-full rounded-full" style={{ width: `${pct}%`, background: tone }} />
        </div>
        <span className="flex-none font-mono text-[10.5px] text-ink-faint">
          {all > 0 ? `${done}/${all}` : "—"}
        </span>
      </div>
    </Link>
  );
}

// AddArtistModal searches MusicBrainz. The disambiguation is shown prominently because it's
// very often the only way to tell two same-named artists apart.
function AddArtistModal({ onClose, onAdded }: { onClose: () => void; onAdded: (name: string) => void }) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<ArtistLookup[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState<string | null>(null);

  const search = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!query.trim()) return;
    setSearching(true);
    setError(null);
    try {
      setResults(await api.lookupArtists(query.trim()));
    } catch (err) {
      setError((err as Error).message);
      setResults([]);
    } finally {
      setSearching(false);
    }
  };

  const add = async (r: ArtistLookup) => {
    setSaving(r.mbid);
    setError(null);
    try {
      await api.addArtist({ mbid: r.mbid, monitored: true });
      onAdded(r.name);
      onClose();
    } catch (err) {
      setError((err as Error).message);
      setSaving(null);
    }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div
        className="mt-10 w-full max-w-[680px] rounded-2xl p-5"
        style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="mb-1 flex items-center justify-between">
          <h2 className="m-0 text-[15px] font-bold">Add an artist</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">
            ✕
          </button>
        </div>
        <p className="mb-3 text-[12px] text-ink-dim">
          Searches MusicBrainz. Adding an artist brings in their albums; track listings fill in when you open an album.
        </p>

        <form className="mb-3 flex gap-2" onSubmit={search}>
          <input
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Artist name…"
            className="flex-1 rounded-lg px-3 py-2 text-[13px]"
            style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
          />
          <button
            type="submit"
            disabled={searching}
            className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50"
            style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
          >
            {searching ? "Searching…" : "Search"}
          </button>
        </form>

        {error && (
          <div className="mb-3 rounded-lg p-2.5 text-[12px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>
            {error}
          </div>
        )}

        {results !== null && (
          results.length === 0 ? (
            <div className="rounded-xl p-6 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
              No artists found.
            </div>
          ) : (
            <div className="flex max-h-[420px] flex-col overflow-y-auto">
              {results.map((r) => (
                <div key={r.mbid} className="flex items-center gap-3 border-b py-2.5" style={{ borderColor: "var(--line)" }}>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[13px] font-medium">
                      {r.name}
                      {r.disambiguation && <span className="ml-2 text-[11.5px] text-ink-faint">({r.disambiguation})</span>}
                    </div>
                    <div className="truncate font-mono text-[10.5px] text-ink-faint">
                      {[r.type, r.country].filter(Boolean).join(" · ") || r.mbid}
                    </div>
                  </div>
                  <button
                    disabled={saving !== null}
                    onClick={() => add(r)}
                    className="flex-none rounded-lg px-3 py-1.5 text-[12px] font-semibold disabled:opacity-50"
                    style={{ border: "1px solid var(--accent)", color: "var(--accent)" }}
                  >
                    {saving === r.mbid ? "Adding…" : "Add"}
                  </button>
                </div>
              ))}
            </div>
          )
        )}
      </div>
    </div>
  );
}
