import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type MusicAlbum } from "../lib/api";

function fmtDuration(sec?: number): string {
  if (!sec || sec <= 0) return "";
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

function fmtBytes(n?: number): string {
  if (!n || n <= 0) return "";
  const mb = n / 1024 ** 2;
  return mb >= 1000 ? `${(mb / 1024).toFixed(2)} GB` : `${mb.toFixed(0)} MB`;
}

export function AlbumDetail() {
  const { id } = useParams();
  const alid = Number(id);
  const [al, setAl] = useState<MusicAlbum | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    api
      .albumDetail(alid)
      .then(setAl)
      .catch((e: Error) => {
        if (e.message.toLowerCase().includes("not found")) setNotFound(true);
        else setError(e.message);
      });
  }, [alid]);
  useEffect(() => {
    load();
  }, [load]);

  if (notFound)
    return (
      <Shell>
        <div className="py-10 text-center text-[13px] text-ink-dim">
          That album isn't in your library.{" "}
          <Link to="/music" className="underline" style={{ color: "var(--accent)" }}>
            Back to Music
          </Link>
        </div>
      </Shell>
    );
  if (!al)
    return (
      <Shell>
        {/* The first open fetches the track listing from MusicBrainz, which is paced at one
            request per second — so say so rather than showing a bare spinner. */}
        <p className="text-[12.5px] text-ink-dim">{error ?? "Loading the track listing…"}</p>
      </Shell>
    );

  const complete = al.track_count > 0 && al.have_tracks >= al.track_count;

  return (
    <>
      <PageHeader title={al.title} crumb="Library / Music" />
      <div className="mx-auto w-full max-w-[1000px] px-4 py-6 sm:px-6">
        <Link to={`/music/${al.artist_id}`} className="mb-4 inline-flex items-center gap-1 text-[12px] text-ink-dim hover:text-[var(--ink)]">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d="M15 19l-7-7 7-7" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          Back to the artist
        </Link>

        <div className="flex flex-col gap-4 sm:flex-row">
          <div className="flex-none">
            <div className="h-[160px] w-[160px] overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
              {al.cover_url ? (
                // Cover Art Archive 404s for releases with no artwork; fall back silently
                // rather than showing a broken image.
                <img
                  src={al.cover_url}
                  alt={al.title}
                  className="h-full w-full object-cover"
                  onError={(e) => {
                    (e.currentTarget as HTMLImageElement).style.display = "none";
                  }}
                />
              ) : null}
            </div>
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2.5">
              <span
                className="rounded-full px-2.5 py-1 font-mono text-[10.5px] font-semibold uppercase"
                style={{ background: "var(--panel-2)", color: complete ? "var(--good)" : al.monitored ? "var(--avoid)" : "var(--ink-faint)" }}
              >
                {complete ? "Complete" : al.monitored ? "Wanted" : "Unmonitored"}
              </span>
              {al.year ? <span className="font-mono text-[11px] text-ink-faint">{al.year}</span> : null}
              {al.album_type ? <span className="font-mono text-[11px] text-ink-faint">{al.album_type}</span> : null}
              <a
                href={`https://musicbrainz.org/release-group/${al.mbid}`}
                target="_blank"
                rel="noreferrer"
                className="rounded px-1.5 py-0.5 font-mono text-[10px] font-bold"
                style={{ background: "#4a2a6a", color: "#fff" }}
              >
                MusicBrainz
              </a>
            </div>
            <div className="mt-2 font-mono text-[11.5px] text-ink-faint">
              {al.have_tracks}/{al.track_count} tracks{al.size_bytes ? ` · ${fmtBytes(al.size_bytes)}` : ""}
            </div>
            <div className="mt-4">
              <button
                disabled={busy}
                onClick={async () => {
                  setBusy(true);
                  try {
                    await api.setAlbumMonitored(al.id, !al.monitored);
                    load();
                  } finally {
                    setBusy(false);
                  }
                }}
                className="rounded-lg px-3 py-1.5 text-[12px] font-semibold disabled:opacity-50"
                style={{ border: `1px solid ${al.monitored ? "var(--accent)" : "var(--line)"}`, color: al.monitored ? "var(--accent)" : "var(--ink-dim)" }}
              >
                {al.monitored ? "Monitored" : "Monitor"}
              </button>
            </div>
          </div>
        </div>

        {error && (
          <div className="mt-4 rounded-lg p-3 text-[12px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>
            {error}
          </div>
        )}

        <h2 className="m-0 mb-3 mt-8 text-[14px] font-bold">Tracks</h2>
        {!al.tracks || al.tracks.length === 0 ? (
          <div className="rounded-xl p-8 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No track listing. MusicBrainz has no official release for this album yet — an
            announced record often has none until it ships.
          </div>
        ) : (
          <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
            {al.tracks.map((t) => (
              <div
                key={t.id}
                className="flex items-center gap-3 border-b px-4 py-2 last:border-b-0 text-[12.5px]"
                style={{ background: "var(--panel)", borderColor: "var(--line-soft)" }}
              >
                <span className="w-[42px] flex-none font-mono text-[10.5px] text-ink-faint">
                  {t.disc_number > 1 ? `${t.disc_number}-` : ""}
                  {String(t.track_number).padStart(2, "0")}
                </span>
                <span className="min-w-0 flex-1 truncate" style={{ color: t.has_file ? "var(--ink)" : "var(--ink-dim)" }}>
                  {t.title}
                </span>
                {t.format ? (
                  <span className="flex-none rounded px-1.5 py-0.5 font-mono text-[9px] font-bold uppercase" style={{ background: "var(--panel-2)", color: "var(--good)" }}>
                    {t.format}
                  </span>
                ) : null}
                <span className="w-[52px] flex-none text-right font-mono text-[10.5px] text-ink-faint">{fmtDuration(t.duration_sec)}</span>
                <span
                  className="w-[74px] flex-none text-right font-mono text-[10px] font-semibold uppercase"
                  style={{ color: t.has_file ? "var(--good)" : "var(--avoid)" }}
                >
                  {t.has_file ? "have" : "missing"}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <>
      <PageHeader title="Music" crumb="Library / Music" />
      <div className="mx-auto w-full max-w-[1000px] px-4 py-6 sm:px-6">{children}</div>
    </>
  );
}
