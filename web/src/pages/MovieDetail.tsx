import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { ReleaseSearchModal } from "../components/ReleaseSearchModal";
import { PasteLinkModal } from "../components/PasteLinkModal";
import {
  api,
  type BlockEntry,
  type CollectionMember,
  type ImportCandidate,
  type Movie,
  type MovieEvent,
  type MovieFile,
  type MovieVersion,
} from "../lib/api";
import { useLive } from "../lib/useLive";

const AVAILABILITY_LABELS: Record<string, string> = {
  announced: "Announced",
  inCinemas: "In cinemas",
  released: "Released",
};

export function MovieDetail() {
  const { id } = useParams<{ id: string }>();
  const movieId = Number(id);
  const [movie, setMovie] = useState<Movie | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [toast, setToast] = useState<string | null>(null);
  const { last } = useLive();

  const flash = (msg: string) => {
    setToast(msg);
    window.setTimeout(() => setToast(null), 3500);
  };

  const load = useCallback(() => {
    api
      .movie(movieId)
      .then((m) => {
        setMovie(m);
        setError(null);
      })
      .catch((e: Error) => {
        if (e.message.toLowerCase().includes("not found")) setNotFound(true);
        else setError(e.message);
      });
  }, [movieId]);

  useEffect(() => {
    load();
  }, [load]);

  // Poll while a download is in progress so the bar advances live.
  const downloading = !!movie?.download;
  useEffect(() => {
    if (!downloading) return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [downloading, load]);

  useEffect(() => {
    if (!last) return;
    const topics = [
      "movie.downloaded",
      "download.imported",
      "release.grabbed",
      "movie.file_deleted",
      "movie.refreshed",
      "movie.renamed",
    ];
    if (topics.includes(last.topic)) load();
  }, [last, load]);

  if (notFound) {
    return (
      <>
        <PageHeader title="Movie" crumb="Library / Movies" />
        <div className="mx-auto w-full max-w-[900px] px-6 py-10 text-center text-[13px] text-ink-dim">
          That movie isn't in your library.{" "}
          <Link to="/movies" className="underline" style={{ color: "var(--accent)" }}>Back to Movies</Link>
        </div>
      </>
    );
  }

  if (!movie) {
    return (
      <>
        <PageHeader title="Movie" crumb="Library / Movies" />
        <div className="mx-auto w-full max-w-[900px] px-6 py-10 text-[13px] text-ink-dim">
          {error ? <span style={{ color: "var(--reject)" }}>{error}</span> : "Loading…"}
        </div>
      </>
    );
  }

  const st = statusOf(movie);
  const ex = movie.extra;

  return (
    <>
      <PageHeader title={movie.title} crumb="Library / Movies" />

      {/* Hero band with backdrop */}
      <div className="relative">
        {ex?.backdrop_url && (
          <>
            <div
              className="pointer-events-none absolute inset-0 bg-cover bg-center opacity-[0.18]"
              style={{ backgroundImage: `url(${ex.backdrop_url})` }}
            />
            <div className="pointer-events-none absolute inset-0" style={{ background: "linear-gradient(180deg, transparent, var(--bg))" }} />
          </>
        )}
        <div className="relative mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6">
          <Link to="/movies" className="mb-4 inline-flex items-center gap-1 text-[12px] text-ink-dim hover:text-[var(--ink)]">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d="M15 19l-7-7 7-7" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
            All movies
          </Link>

          <div className="flex flex-col gap-6 sm:flex-row">
            <div className="w-[180px] flex-none overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", aspectRatio: "2/3" }}>
              {movie.poster_url ? (
                <img src={movie.poster_url} alt={movie.title} className="h-full w-full object-cover" />
              ) : (
                <div className="flex h-full w-full items-end p-3" style={{ background: "linear-gradient(150deg, hsl(24 40% 30%), hsl(20 35% 16%))" }}>
                  <span className="text-[15px] font-bold text-white">{movie.title}</span>
                </div>
              )}
            </div>

            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2.5">
                <span className="rounded-full px-2.5 py-1 font-mono text-[10.5px] font-semibold uppercase" style={{ background: st.soft, color: st.tone }}>{st.label}</span>
                {ex?.certification && (
                  <span className="rounded px-1.5 py-0.5 font-mono text-[10.5px] font-bold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{ex.certification}</span>
                )}
                <span className="font-mono text-[11px] text-ink-faint">{movie.year || "—"}</span>
                {movie.runtime ? <span className="font-mono text-[11px] text-ink-faint">{fmtRuntime(movie.runtime)}</span> : null}
                {typeof ex?.vote_average === "number" && ex.vote_average > 0 && (
                  <span className="font-mono text-[11px]" style={{ color: "var(--accent)" }}>★ {ex.vote_average.toFixed(1)}</span>
                )}
                <ExternalLinks movie={movie} />
              </div>

              <p className="mt-3 text-[13px] leading-relaxed text-ink-dim">{movie.overview || "No overview available."}</p>

              <FactRow movie={movie} />

              <div className="mt-4 flex flex-wrap items-center gap-4">
                <ProfileSelector movie={movie} onChange={load} />
                <AvailabilitySelector movie={movie} onChange={load} />
              </div>

              <WhyPanel movie={movie} />
              <Toolbar movie={movie} onChange={load} flash={flash} />
            </div>
          </div>
        </div>
      </div>

      <div className="mx-auto w-full max-w-[1200px] px-4 pb-10 sm:px-6">
        {movie.download && <DownloadBar dl={movie.download} />}
        <VersionsArea movie={movie} onChange={load} flash={flash} />
        <CastRow cast={movie.extra?.cast} />
        <BlocklistPanel movieId={movie.id} refreshKey={movie.has_file} />
        <HistoryPanel movieId={movie.id} refreshKey={movie.has_file} />
      </div>

      {toast && (
        <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>
          {toast}
        </div>
      )}
    </>
  );
}

function statusOf(m: Movie): { label: string; tone: string; soft: string } {
  if (m.has_file) return { label: "Downloaded", tone: "var(--good)", soft: "var(--good-soft, rgba(90,140,90,.12))" };
  if (m.monitored) return { label: "Wanted", tone: "var(--avoid)", soft: "var(--avoid-soft)" };
  return { label: "Unmonitored", tone: "var(--ink-faint)", soft: "var(--panel-2)" };
}

function fmtRuntime(min: number): string {
  const h = Math.floor(min / 60);
  const m = min % 60;
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

function fmtSize(bytes: number): string {
  if (bytes <= 0) return "—";
  const gb = bytes / 1024 ** 3;
  if (gb >= 1) return `${gb.toFixed(2)} GB`;
  return `${(bytes / 1024 ** 2).toFixed(0)} MB`;
}

function DownloadBar({ dl }: { dl: { state: string; progress: number } }) {
  const pct = Math.round(dl.progress * 100);
  return (
    <div className="mt-6 rounded-xl p-3.5" style={{ border: "1px solid var(--accent)", background: "var(--accent-soft)" }}>
      <div className="mb-2 flex items-center justify-between text-[12px]">
        <span className="font-semibold" style={{ color: "var(--accent)" }}>Downloading — {dl.state}</span>
        <span className="font-mono text-ink-dim">{pct}%</span>
      </div>
      <div className="h-2 overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
        <div className="h-full rounded-full transition-all" style={{ width: `${pct}%`, background: "var(--accent)" }} />
      </div>
    </div>
  );
}

// VersionsArea keeps single-file movies looking exactly as before (one file
// panel), and switches to a multi-track view once an extra version is added.
function VersionsArea({ movie, onChange, flash }: { movie: Movie; onChange: () => void; flash: (m: string) => void }) {
  const [adding, setAdding] = useState(false);
  const [profileNames, setProfileNames] = useState<Record<string, string>>({});
  const versions = movie.versions ?? [];
  const extras = versions.filter((v) => !v.is_default);

  useEffect(() => {
    api
      .qualityProfiles("movie")
      .then((r) => setProfileNames(Object.fromEntries(r.profiles.map((p) => [p.key, p.name]))))
      .catch(() => {});
  }, []);
  const profileName = (ref: string) => (ref === "n/a" ? "Not set" : profileNames[ref] ?? ref);

  if (extras.length === 0) {
    return (
      <>
        {movie.file && <FilePanel file={movie.file} movieId={movie.id} onChange={onChange} flash={flash} />}
        <button onClick={() => setAdding(true)} className="mt-4 inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-[12px] font-semibold" style={{ border: "1px dashed var(--line)", color: "var(--ink-dim)" }}>
          ＋ Keep another version <span className="text-ink-faint">(e.g. 1080p + 4K, or a Director's Cut)</span>
        </button>
        {adding && <AddVersionModal movieId={movie.id} onClose={() => setAdding(false)} onAdded={() => { setAdding(false); onChange(); flash("Version added — searching for it."); }} />}
      </>
    );
  }

  return (
    <div className="mt-6">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="m-0 text-[14px] font-bold">Versions <span className="font-normal text-ink-faint">· {versions.length} tracks</span></h2>
        <button onClick={() => setAdding(true)} className="rounded-lg px-3 py-1.5 text-[12px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>＋ Add version</button>
      </div>
      <div className="flex flex-col gap-2.5">
        {versions.map((v) => (
          <VersionCard key={`${v.is_default ? "d" : v.id}`} movieId={movie.id} version={v} onChange={onChange} flash={flash} profileName={profileName} />
        ))}
      </div>
      {adding && <AddVersionModal movieId={movie.id} onClose={() => setAdding(false)} onAdded={() => { setAdding(false); onChange(); flash("Version added — searching for it."); }} />}
    </div>
  );
}

function VersionCard({ movieId, version, onChange, flash, profileName }: { movieId: number; version: MovieVersion; onChange: () => void; flash: (m: string) => void; profileName: (ref: string) => string }) {
  const [busy, setBusy] = useState(false);
  const f = version.file;

  const toggleMonitor = async () => {
    setBusy(true);
    try {
      if (version.is_default) await api.setMonitored(movieId, !version.monitored);
      else await api.updateVersion(movieId, version.id, { label: version.label, quality_profile: version.quality_profile, edition: version.edition, monitored: !version.monitored });
      onChange();
    } finally {
      setBusy(false);
    }
  };

  const removeVersion = async () => {
    setBusy(true);
    try {
      await api.deleteVersion(movieId, version.id);
      onChange();
      flash(`Removed "${version.label}" version.`);
    } finally {
      setBusy(false);
    }
  };

  const deleteFile = async () => {
    setBusy(true);
    try {
      await api.deleteVersionFile(movieId, version.id);
      onChange();
    } finally {
      setBusy(false);
    }
  };

  const status = f ? { label: "Downloaded", tone: "var(--good)" } : version.monitored ? { label: "Wanted", tone: "var(--avoid)" } : { label: "Unmonitored", tone: "var(--ink-faint)" };
  const chips: string[] = [];
  if (f?.codec) chips.push(f.codec);
  if (f?.audio) chips.push(...f.audio);
  if (f?.hdr) chips.push(...f.hdr);

  return (
    <div className="rounded-xl p-3.5" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[13.5px] font-semibold">{version.label}</span>
            {version.is_default && <span className="rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>Default</span>}
            <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{profileName(version.quality_profile)}</span>
            {version.edition && <span className="rounded px-1.5 py-0.5 text-[10.5px]" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{version.edition}</span>}
            <span className="font-mono text-[9.5px] uppercase" style={{ color: status.tone }}>{status.label}</span>
          </div>
          {f ? (
            <>
              <div className="mt-1.5 flex flex-wrap items-center gap-2 text-[12px]">
                {f.quality && <span className="rounded px-2 py-0.5 text-[11px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{f.quality}</span>}
                {chips.map((c) => <span key={c} className="rounded px-2 py-0.5 text-[11px]" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{c}</span>)}
                <span className="font-mono text-ink-dim">{fmtSize(f.size_bytes)}</span>
              </div>
              <div className="mt-1 break-all font-mono text-[11px] text-ink-faint">{f.path}</div>
            </>
          ) : (
            <div className="mt-1.5 text-[12px] text-ink-dim">{version.monitored ? "No file yet — Arrmada is searching for this track." : "Not monitored."}</div>
          )}
        </div>
        <div className="flex flex-none flex-col items-end gap-1.5">
          <button onClick={toggleMonitor} disabled={busy} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>{version.monitored ? "Monitored" : "Unmonitored"}</button>
          {f && <button onClick={deleteFile} disabled={busy} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Delete file</button>}
          {!version.is_default && <button onClick={removeVersion} disabled={busy} className="rounded-lg px-2.5 py-1 text-[11px]" style={{ color: "var(--ink-faint)" }}>Remove version</button>}
        </div>
      </div>
    </div>
  );
}

function AddVersionModal({ movieId, onClose, onAdded }: { movieId: number; onClose: () => void; onAdded: () => void }) {
  const [label, setLabel] = useState("");
  const [profile, setProfile] = useState("");
  const [edition, setEdition] = useState("");
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.qualityProfiles("movie").then((r) => {
      setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })));
      const def = r.profiles.find((p) => p.is_default) ?? r.profiles[0];
      if (def) setProfile(def.key);
    }).catch(() => {});
  }, []);

  const add = async () => {
    if (!label.trim()) {
      setError("Give the version a label (e.g. 4K, Director's Cut).");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await api.addVersion(movieId, { label: label.trim(), quality_profile: profile, edition: edition.trim(), monitored: true });
      onAdded();
    } catch (e) {
      setError((e as Error).message);
      setSaving(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-16 w-full max-w-[460px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <h2 className="m-0 mb-1 text-[15px] font-bold">Add a version</h2>
        <p className="mb-4 text-[12px] text-ink-dim">A separate track with its own quality target — kept alongside your existing file, not replacing it.</p>

        <label className="mb-1 block font-mono text-[10px] font-bold uppercase text-accent">Label</label>
        <input autoFocus value={label} onChange={(e) => setLabel(e.target.value)} placeholder="4K · Director's Cut · Remux" className="mb-3 w-full rounded-lg px-3 py-2 text-[13px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />

        <label className="mb-1 block font-mono text-[10px] font-bold uppercase text-accent">Quality profile</label>
        <select value={profile} onChange={(e) => setProfile(e.target.value)} className="mb-3 w-full rounded-lg px-3 py-2 text-[12.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
          {profiles.map((p) => <option key={p.key} value={p.key}>{p.name}</option>)}
        </select>

        <label className="mb-1 block font-mono text-[10px] font-bold uppercase text-accent">Edition <span className="text-ink-faint">(optional)</span></label>
        <input value={edition} onChange={(e) => setEdition(e.target.value)} placeholder="e.g. Director's Cut" className="mb-4 w-full rounded-lg px-3 py-2 text-[13px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />

        {error && <div className="mb-3 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
        <div className="flex gap-2.5">
          <button onClick={add} disabled={saving} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{saving ? "Adding…" : "Add version"}</button>
          <button onClick={onClose} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
        </div>
      </div>
    </div>
  );
}

function ExternalLinks({ movie }: { movie: Movie }) {
  return (
    <span className="flex items-center gap-2">
      {movie.imdb_id && (
        <a href={`https://www.imdb.com/title/${movie.imdb_id}`} target="_blank" rel="noreferrer" className="rounded px-1.5 py-0.5 font-mono text-[10px] font-bold" style={{ background: "#f5c518", color: "#000" }}>IMDb</a>
      )}
      <a href={`https://www.themoviedb.org/movie/${movie.tmdb_id}`} target="_blank" rel="noreferrer" className="rounded px-1.5 py-0.5 font-mono text-[10px] font-bold" style={{ background: "#01b4e4", color: "#fff" }}>TMDB</a>
    </span>
  );
}

function FactRow({ movie }: { movie: Movie }) {
  const ex = movie.extra;
  const [showCollection, setShowCollection] = useState(false);
  if (!ex) return null;
  return (
    <div className="mt-3 flex flex-col gap-2 text-[12px]">
      {ex.genres && ex.genres.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          {ex.genres.map((g) => (
            <span key={g} className="rounded-full px-2 py-0.5 text-[11px]" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{g}</span>
          ))}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-1 text-ink-faint">
        {ex.studios && ex.studios.length > 0 && <span><span className="text-ink-faint">Studio</span> <span className="text-ink-dim">{ex.studios.slice(0, 2).join(", ")}</span></span>}
        {ex.original_language && <span><span className="text-ink-faint">Language</span> <span className="text-ink-dim uppercase">{ex.original_language}</span></span>}
        {ex.collection_name && (
          <button onClick={() => setShowCollection(true)} className="inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-[11.5px] font-medium" style={{ background: "var(--accent-soft)", color: "var(--accent)", border: "1px solid var(--accent-line)" }}>
            {ex.collection_name} <span className="text-[10px]">· view collection</span>
          </button>
        )}
      </div>
      {showCollection && <CollectionModal movieId={movie.id} onClose={() => setShowCollection(false)} />}
    </div>
  );
}

function CollectionModal({ movieId, onClose }: { movieId: number; onClose: () => void }) {
  const [name, setName] = useState("");
  const [members, setMembers] = useState<CollectionMember[]>([]);
  const [loading, setLoading] = useState(true);
  const [profile, setProfile] = useState("");
  const [adding, setAdding] = useState<Set<number>>(new Set());
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    api.movieCollection(movieId)
      .then((r) => { setName(r.name); setMembers(r.members); })
      .catch((e: Error) => setError(e.message))
      .finally(() => setLoading(false));
  }, [movieId]);

  useEffect(() => {
    load();
    api.qualityProfiles("movie").then((r) => {
      const def = r.profiles.find((p) => p.is_default) ?? r.profiles[0];
      if (def) setProfile(def.key);
    }).catch(() => {});
  }, [load]);

  const addOne = async (tmdbId: number) => {
    setAdding((s) => new Set(s).add(tmdbId));
    setError(null);
    try {
      await api.addMovie({ tmdb_id: tmdbId, quality_profile: profile, monitored: true });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setAdding((s) => { const n = new Set(s); n.delete(tmdbId); return n; });
      load();
    }
  };

  const missing = members.filter((m) => !m.in_library);
  const addAll = async () => {
    for (const m of missing) await addOne(m.tmdb_id);
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[560px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-3 flex items-center justify-between gap-3">
          <div>
            <h2 className="m-0 text-[15px] font-bold">{name || "Collection"}</h2>
            <p className="m-0 mt-0.5 text-[11.5px] text-ink-dim">{members.length} films · {missing.length} not in your library</p>
          </div>
          {missing.length > 0 && (
            <button onClick={addAll} disabled={!profile || adding.size > 0} className="flex-none rounded-lg px-3.5 py-2 text-[12.5px] font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: !profile || adding.size > 0 ? 0.6 : 1 }}>
              {adding.size > 0 ? "Adding…" : `Add all ${missing.length}`}
            </button>
          )}
        </div>
        {error && <div className="mb-3 rounded-lg p-2.5 text-[12px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>}
        {loading ? (
          <p className="py-6 text-center text-[12.5px] text-ink-dim">Loading collection…</p>
        ) : (
          <div className="flex max-h-[60vh] flex-col gap-1.5 overflow-y-auto">
            {members.map((m) => (
              <div key={m.tmdb_id} className="flex items-center gap-3 rounded-lg p-2" style={{ background: "var(--panel-2)" }}>
                {m.poster_url
                  ? <img src={m.poster_url} alt="" className="h-14 w-9 flex-none rounded object-cover" />
                  : <div className="h-14 w-9 flex-none rounded" style={{ background: "var(--line)" }} />}
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[13px] font-semibold">{m.title}</div>
                  <div className="text-[11px] text-ink-faint">{m.year || "—"}{m.vote_average ? ` · ★ ${m.vote_average.toFixed(1)}` : ""}</div>
                </div>
                {m.in_library ? (
                  <span className="flex-none rounded px-2 py-1 font-mono text-[10px] uppercase" style={{ background: "var(--good-soft)", color: "var(--good)" }}>In library</span>
                ) : (
                  <button onClick={() => addOne(m.tmdb_id)} disabled={!profile || adding.has(m.tmdb_id)} className="flex-none rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>
                    {adding.has(m.tmdb_id) ? "Adding…" : "Add"}
                  </button>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function ProfileSelector({ movie, onChange }: { movie: Movie; onChange: () => void }) {
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);

  useEffect(() => {
    api
      .qualityProfiles("movie")
      .then((r) => setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name }))))
      .catch(() => {});
  }, []);

  const [downgrade, setDowngrade] = useState(false);
  const [regrabbing, setRegrabbing] = useState(false);

  const change = async (profile: string) => {
    if (profile === movie.quality_profile) return;
    setSaving(true);
    setSaved(false);
    setDowngrade(false);
    try {
      const res = await api.setQualityProfile(movie.id, profile);
      if (res.downgrade) {
        setDowngrade(true);
      } else {
        setSaved(true);
        window.setTimeout(() => setSaved(false), 2000);
      }
      onChange();
    } finally {
      setSaving(false);
    }
  };

  const doRegrab = async () => {
    setRegrabbing(true);
    try {
      await api.regrabMovie(movie.id);
      setDowngrade(false);
    } finally {
      setRegrabbing(false);
    }
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[10.5px] uppercase text-ink-faint">Quality</span>
        <select value={movie.quality_profile} onChange={(e) => change(e.target.value)} disabled={saving} className="rounded-lg px-2.5 py-1.5 text-[12px] font-medium" style={{ background: "var(--accent-soft)", border: "1px solid var(--line)", color: "var(--accent)" }}>
          {(() => {
            const opts = [...profiles];
            // Surface an unset ("n/a") profile as a real option so the select shows it.
            const cur = movie.quality_profile;
            if (cur && !opts.some((o) => o.key === cur)) opts.unshift({ key: cur, name: cur === "n/a" ? "Not set (n/a)" : cur });
            return opts.map((p) => (
              <option key={p.key} value={p.key} style={{ background: "var(--panel)", color: "var(--ink)" }}>{p.name}</option>
            ));
          })()}
        </select>
        {saved && <span className="text-[11px]" style={{ color: "var(--good)" }}>Saved ✓</span>}
      </div>
      {downgrade && (
        <div className="rounded-lg p-3 text-[12px]" style={{ background: "var(--avoid-soft, var(--panel-2))", border: "1px solid var(--avoid, var(--line))" }}>
          <div className="mb-2 text-ink-dim">Your current file is higher quality than this profile targets. Download a smaller release to match it, or keep the file you have?</div>
          <div className="flex gap-2">
            <button onClick={doRegrab} disabled={regrabbing} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>
              {regrabbing ? "Searching…" : "Download smaller version"}
            </button>
            <button onClick={() => setDowngrade(false)} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Keep current file</button>
          </div>
        </div>
      )}
    </div>
  );
}

function AvailabilitySelector({ movie, onChange }: { movie: Movie; onChange: () => void }) {
  const [saving, setSaving] = useState(false);
  const change = async (v: string) => {
    if (v === movie.min_availability) return;
    setSaving(true);
    try {
      await api.setAvailability(movie.id, v);
      onChange();
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="font-mono text-[10.5px] uppercase text-ink-faint">Search when</span>
      <select value={movie.min_availability} onChange={(e) => change(e.target.value)} disabled={saving} className="rounded-lg px-2.5 py-1.5 text-[12px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
        {Object.entries(AVAILABILITY_LABELS).map(([key, label]) => (
          <option key={key} value={key}>{label}</option>
        ))}
      </select>
    </div>
  );
}

function WhyPanel({ movie }: { movie: Movie }) {
  let msg: string;
  let tone = "var(--ink-dim)";
  if (movie.has_file) {
    msg = "You have this movie. If your quality profile allows upgrades, Arrmada keeps watching for a clearly-better release and grabs it automatically (checked every 6 hours); otherwise it stays put until you raise the profile or delete the file.";
    tone = "var(--good)";
  } else if (!movie.monitored) {
    msg = "Not monitored — Arrmada won't search for this. Turn on monitoring to start looking.";
    tone = "var(--avoid)";
  } else {
    const avail = AVAILABILITY_LABELS[movie.min_availability] ?? movie.min_availability;
    msg = `Monitored and missing — Arrmada searches automatically once the movie is "${avail}", and grabs the best release for your quality profile.`;
    tone = "var(--accent)";
  }
  return (
    <div className="mt-4 rounded-lg p-3 text-[12px] leading-relaxed" style={{ border: "1px solid var(--line)", color: tone }}>
      {msg}
    </div>
  );
}

function Toolbar({ movie, onChange, flash }: { movie: Movie; onChange: () => void; flash: (m: string) => void }) {
  const [busy, setBusy] = useState<string | null>(null);
  const [showImport, setShowImport] = useState(false);
  const [showSearch, setShowSearch] = useState(false);
  const [showPaste, setShowPaste] = useState(false);

  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    try {
      await fn();
    } catch (e) {
      flash((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const btn = "rounded-lg px-3 py-2 text-[12.5px] font-semibold disabled:opacity-50";
  const ghost = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;

  const rename = async () => {
    const p = await api.renamePreview(movie.id);
    if (p.matches) {
      flash("Already named correctly.");
      return;
    }
    await api.renameMovie(movie.id);
    flash(`Renamed to "${p.proposed}"`);
    onChange();
  };

  return (
    <>
      <div className="mt-4 flex flex-wrap items-center gap-3">
        <button
          role="switch"
          aria-checked={movie.monitored}
          disabled={busy !== null}
          onClick={() => run("monitor", async () => { await api.setMonitored(movie.id, !movie.monitored); onChange(); })}
          className="inline-flex items-center gap-2 text-[12.5px] font-semibold disabled:opacity-50"
          title={movie.monitored ? "Monitored — click to stop" : "Not monitored — click to monitor"}
        >
          <span className="relative inline-block h-[22px] w-[38px] rounded-full transition-colors" style={{ background: movie.monitored ? "var(--accent)" : "var(--line)" }}>
            <span className="absolute top-[3px] h-[16px] w-[16px] rounded-full bg-white transition-all" style={{ left: movie.monitored ? "19px" : "3px" }} />
          </span>
          <span style={{ color: movie.monitored ? "var(--ink)" : "var(--ink-dim)" }}>{movie.monitored ? "Monitored" : "Monitor"}</span>
        </button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => run("refresh", async () => { await api.refreshMovie(movie.id); onChange(); flash("Refreshed metadata and rescanned disk."); })}>
          {busy === "refresh" ? "Refreshing…" : "Refresh & rescan"}
        </button>
        {!movie.has_file && (
          <button className={btn} style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }} disabled={busy !== null} onClick={() => run("search", async () => { await api.searchMovie(movie.id); flash("Searching — it'll show in Activity once grabbed."); })}>
            {busy === "search" ? "Searching…" : "Auto-grab best"}
          </button>
        )}
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => setShowSearch(true)}>Search indexers</button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => setShowPaste(true)}>Paste link</button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => setShowImport(true)}>Manual import</button>
        {movie.has_file && (
          <button className={btn} style={ghost} disabled={busy !== null} onClick={() => run("rename", rename)}>
            {busy === "rename" ? "Renaming…" : "Rename"}
          </button>
        )}
      </div>
      {showPaste && (
        <PasteLinkModal
          what={movie.title}
          onPreview={(link) => api.previewLink(link)}
          onGrab={async (link, title) => { await api.grabMovieLink(movie.id, link, title); onChange(); }}
          onClose={() => setShowPaste(false)}
        />
      )}
      {showImport && <ManualImportModal movie={movie} onClose={() => setShowImport(false)} onImported={() => { setShowImport(false); onChange(); flash("Imported."); }} />}
      {showSearch && (
        <ReleaseSearchModal
          title={`Search indexers — ${movie.title}`}
          subtitle="Pick a release to grab, or blocklist one to search for an alternate."
          fetchReleases={() => api.movieReleases(movie.id)}
          onGrab={async (rel) => { await api.grab({ indexer: rel.indexer, download_url: rel.download_url, title: rel.title, movie_id: movie.id }); onChange(); }}
          onBlock={async (rel) => { await api.blockRelease(movie.id, { title: rel.title, indexer: rel.indexer, download_url: rel.download_url, search_again: true }); flash(`Blocklisted "${rel.summary}" — searching for an alternate.`); onChange(); }}
          onClose={() => setShowSearch(false)}
        />
      )}
    </>
  );
}

function FilePanel({ file, movieId, onChange, flash }: { file: MovieFile; movieId: number; onChange: () => void; flash: (m: string) => void }) {
  const [confirming, setConfirming] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const del = async () => {
    setDeleting(true);
    try {
      await api.deleteMovieFile(movieId);
      onChange();
    } catch (e) {
      flash((e as Error).message);
      setDeleting(false);
    }
  };

  const tone = file.missing ? "var(--avoid)" : "var(--good)";
  const chips: string[] = [];
  if (file.codec) chips.push(file.codec);
  if (file.audio) chips.push(...file.audio);
  if (file.hdr) chips.push(...file.hdr);

  return (
    <div className="mt-6 rounded-xl p-4" style={{ border: `1px solid ${tone}`, background: "var(--panel)" }}>
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="font-mono text-[10.5px] uppercase" style={{ color: tone }}>{file.missing ? "File missing from disk" : "On disk"}</div>
          <div className="mt-1.5 flex flex-wrap items-center gap-2 text-[12.5px]">
            {file.quality && <span className="rounded px-2 py-0.5 text-[11px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{file.quality}</span>}
            {chips.map((c) => (
              <span key={c} className="rounded px-2 py-0.5 text-[11px]" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{c}</span>
            ))}
            <span className="font-mono text-ink-dim">{fmtSize(file.size_bytes)}</span>
            {file.duration_min ? <span className="font-mono text-[11px] text-ink-faint">{file.duration_min}m</span> : null}
            {file.group && <span className="font-mono text-[11px] text-ink-faint">{file.group}</span>}
            {file.probed && <span title="Read from the actual file (ffprobe)" className="font-mono text-[10px]" style={{ color: "var(--good)" }}>✓ probed</span>}
          </div>
          <div className="mt-1.5 break-all font-mono text-[11.5px] text-ink-faint">{file.path}</div>
          {file.subtitles && file.subtitles.length > 0 && (
            <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-[11px] text-ink-dim">
              <span className="font-mono uppercase text-ink-faint">Subs</span>
              {file.subtitles.map((s) => (
                <span key={s} className="rounded px-1.5 py-0.5" style={{ background: "var(--panel-2)" }}>{s}</span>
              ))}
            </div>
          )}
          {file.missing && <div className="mt-1.5 text-[11.5px]" style={{ color: "var(--avoid)" }}>Tracked but not on disk. Refresh & rescan, or clear the record to search again.</div>}
        </div>
        <div className="flex-none">
          {confirming ? (
            <div className="flex items-center gap-2">
              <button onClick={del} disabled={deleting} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--reject)", color: "#fff" }}>{deleting ? "Deleting…" : "Delete file"}</button>
              <button onClick={() => setConfirming(false)} disabled={deleting} className="rounded-lg px-3 py-1.5 text-[11.5px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
            </div>
          ) : (
            <button onClick={() => setConfirming(true)} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{file.missing ? "Clear record" : "Delete file"}</button>
          )}
        </div>
      </div>
    </div>
  );
}

function CastRow({ cast }: { cast?: { name: string; character?: string; profile_url?: string }[] }) {
  if (!cast || cast.length === 0) return null;
  return (
    <div className="mt-8">
      <h2 className="m-0 mb-3 text-[14px] font-bold">Cast</h2>
      <div className="thin-scroll flex gap-3 overflow-x-auto pb-2">
        {cast.map((c) => (
          <div key={c.name} className="w-[92px] flex-none text-center">
            <div className="mb-1.5 overflow-hidden rounded-lg" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }}>
              {c.profile_url ? <img src={c.profile_url} alt={c.name} className="h-full w-full object-cover" loading="lazy" /> : null}
            </div>
            <div className="truncate text-[11px] font-semibold" title={c.name}>{c.name}</div>
            {c.character && <div className="truncate text-[10px] text-ink-faint" title={c.character}>{c.character}</div>}
          </div>
        ))}
      </div>
    </div>
  );
}

function ManualImportModal({ movie, onClose, onImported }: { movie: Movie; onClose: () => void; onImported: () => void }) {
  const [cands, setCands] = useState<ImportCandidate[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [importing, setImporting] = useState<string | null>(null);

  useEffect(() => {
    api
      .manualImportList(movie.id)
      .then((r) => setCands(r.candidates))
      .catch((e: Error) => setError(e.message));
  }, [movie.id]);

  const doImport = async (path: string) => {
    setImporting(path);
    setError(null);
    try {
      await api.manualImport(movie.id, path);
      onImported();
    } catch (e) {
      setError((e as Error).message);
      setImporting(null);
    }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[680px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-1 flex items-center justify-between">
          <h2 className="m-0 text-[15px] font-bold">Manual import</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <p className="mb-3 text-[12px] text-ink-dim">Pick a file already on disk to import as <b>{movie.title}</b>. It'll be renamed to the library scheme.</p>
        {error && <div className="mb-2 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
        <div className="thin-scroll max-h-[52vh] overflow-y-auto">
          {cands === null ? (
            <div className="p-6 text-center text-[12.5px] text-ink-dim">Scanning…</div>
          ) : cands.length === 0 ? (
            <div className="p-6 text-center text-[12.5px] text-ink-dim">No importable video files found in the downloads folder.</div>
          ) : (
            cands.map((c) => (
              <button key={c.path} onClick={() => doImport(c.path)} disabled={importing !== null} className="flex w-full items-center gap-3 rounded-lg p-2.5 text-left transition-colors hover:bg-[var(--panel-2)]">
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[12.5px] font-medium" title={c.filename}>{c.filename}</div>
                  <div className="mt-0.5 flex items-center gap-3 font-mono text-[10.5px] text-ink-faint">
                    {c.quality && <span>{c.quality}</span>}
                    <span>{fmtSize(c.size_bytes)}</span>
                  </div>
                </div>
                <span className="flex-none rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{importing === c.path ? "Importing…" : "Import"}</span>
              </button>
            ))
          )}
        </div>
      </div>
    </div>
  );
}


function BlocklistPanel({ movieId, refreshKey }: { movieId: number; refreshKey: unknown }) {
  const [entries, setEntries] = useState<BlockEntry[] | null>(null);

  const load = () => api.blocklist(movieId).then(setEntries).catch(() => setEntries([]));
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [movieId, refreshKey]);

  const unblock = async (bid: number) => {
    await api.unblock(movieId, bid);
    load();
  };

  if (!entries || entries.length === 0) return null;
  return (
    <div className="mt-8">
      <h2 className="m-0 mb-3 text-[14px] font-bold">Blocklist <span className="font-normal text-ink-faint">· {entries.length}</span></h2>
      <div className="flex flex-col gap-2">
        {entries.map((e) => (
          <div key={e.id} className="flex items-center gap-3 rounded-lg p-2.5 text-[12px]" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
            <div className="min-w-0 flex-1">
              <div className="truncate font-mono text-[11.5px]" title={e.title}>{e.title}</div>
              <div className="mt-0.5 text-[10.5px] text-ink-faint">{e.reason}{e.indexer ? ` · ${e.indexer}` : ""}</div>
            </div>
            <button onClick={() => unblock(e.id)} className="flex-none rounded-lg px-2.5 py-1 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Remove</button>
          </div>
        ))}
      </div>
    </div>
  );
}

const EVENT_TONES: Record<string, string> = {
  added: "var(--ink-faint)",
  grabbed: "var(--accent)",
  imported: "var(--good)",
  upgraded: "var(--good)",
  detected: "var(--good)",
  deleted: "var(--reject)",
  missing: "var(--avoid)",
  renamed: "var(--ink-dim)",
  refreshed: "var(--ink-faint)",
};

function HistoryPanel({ movieId, refreshKey }: { movieId: number; refreshKey: unknown }) {
  const [events, setEvents] = useState<MovieEvent[] | null>(null);

  useEffect(() => {
    api.movieHistory(movieId).then(setEvents).catch(() => setEvents([]));
  }, [movieId, refreshKey]);

  return (
    <div className="mt-8">
      <h2 className="m-0 mb-3 text-[14px] font-bold">History</h2>
      {events === null ? (
        <div className="text-[12.5px] text-ink-dim">Loading…</div>
      ) : events.length === 0 ? (
        <div className="rounded-xl p-6 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>No activity yet.</div>
      ) : (
        <div className="flex flex-col">
          {events.map((e, i) => (
            <div key={i} className="flex items-center gap-3 border-b py-2 text-[12px]" style={{ borderColor: "var(--line)" }}>
              <span className="w-[74px] flex-none font-mono text-[10px] font-bold uppercase" style={{ color: EVENT_TONES[e.event] ?? "var(--ink-dim)" }}>{e.event}</span>
              <span className="min-w-0 flex-1 truncate text-ink-dim" title={e.detail}>{e.detail || "—"}</span>
              <span className="flex-none font-mono text-[10.5px] text-ink-faint">{fmtTime(e.created_at)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function fmtTime(s: string): string {
  const d = new Date(s.includes("T") ? s : s.replace(" ", "T") + "Z");
  if (isNaN(d.getTime())) return s;
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}
