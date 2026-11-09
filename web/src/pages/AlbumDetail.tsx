import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { EditionPicker } from "../components/music/EditionPicker";
import { AlbumSearch } from "../components/music/AlbumSearch";
import { trackFileMark } from "../components/music/fileMarks";
import { MissingFiles } from "../components/music/MissingFiles";
import { api, type MusicAlbum, type MusicEdition, type MusicNote, type MusicTrack } from "../lib/api";
import { formatBytes } from "../lib/format";
import { MusicRenameModal } from "../components/music/MusicRenameModal";
import { musicApi } from "../lib/musicApi";
import { musicNamingApi } from "../lib/musicNamingApi";
import { libraryStatus } from "../lib/status";
import { jobFailed, jobToast, useJob } from "../lib/useJob";
import { Button, StatusChip, useConfirm, useToast } from "../ui";
import { Page } from "../ui/Page";

// isLosslessTier reads a track's stored format: a tier name ("FLAC-24") or an extension tag
// from an unmeasured scan ("FLAC", "AIFF").
function isLosslessTier(format: string): boolean {
  return /^(FLAC|FLAC-24|ALAC|WAV|AIFF|APE|WV)$/i.test(format);
}

function fmtDuration(sec?: number): string {
  if (!sec || sec <= 0) return "";
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

// fileFacts is what was measured in a track's file, for the format badge's tooltip:
// "flac · 24-bit / 96 kHz · 2100 kbps".
function fileFacts(t: MusicTrack): string {
  const parts: string[] = [];
  if (t.codec) parts.push(t.codec);
  const depth = t.bit_depth ? `${t.bit_depth}-bit` : "";
  const rate = t.sample_rate_hz ? `${t.sample_rate_hz % 1000 === 0 ? t.sample_rate_hz / 1000 : (t.sample_rate_hz / 1000).toFixed(1)} kHz` : "";
  if (depth || rate) parts.push([depth, rate].filter(Boolean).join(" / "));
  if (t.bitrate_kbps) parts.push(`${t.bitrate_kbps} kbps`);
  if (!t.codec) parts.push("not measured yet");
  return parts.join(" · ");
}

// editionLabel names the edition the tracks are listed from.
function editionLabel(e: MusicEdition): string {
  if (e.legacy) return "the listing kept from before editions";
  const bits = [e.release_date, e.country, e.media_summary, e.disambiguation].filter(Boolean);
  return bits.length > 0 ? bits.join(" · ") : `${e.track_count} tracks`;
}


export function AlbumDetail() {
  const { id } = useParams();
  const alid = Number(id);
  const [al, setAl] = useState<MusicAlbum | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [picking, setPicking] = useState(false);

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
        <div className="py-10 text-center text-md text-ink-dim">
          That album isn't in your library.{" "}
          <Link to="/music" className="underline" style={{ color: "var(--accent-text)" }}>
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
        <p className="text-base text-ink-dim">{error ?? "Loading the track listing…"}</p>
      </Shell>
    );

  const st = libraryStatus({ multi: true, hasFile: al.have_tracks > 0, monitored: al.monitored, have: al.have_tracks, total: al.track_count });
  const target = al.editions?.find((e) => e.target);
  const tracks = al.tracks ?? [];
  // Tracks by disc, each disc under its own heading when there's more than one.
  const discs = [...new Set(tracks.map((t) => t.disc_number))].sort((x, y) => x - y);
  const discTitle = (n: number) => al.discs?.find((d) => d.position === n)?.title;

  return (
    <>
      <PageHeader title={al.title} />
      <Page>
        <Link to={`/music/${al.artist_id}`} className="mb-4 inline-flex items-center gap-1 text-sm text-ink-dim hover:text-[var(--ink)]">
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
              {al.stale_since ? (
                // Kept with its files, never searched by itself: not "wanted".
                <StatusChip tone="faint" size="md" title="MusicBrainz no longer lists this album. It's kept with its files; search for it by hand if you want it.">
                  No longer on MusicBrainz
                </StatusChip>
              ) : (
                <StatusChip tone={st.tone} size="md">
                  {st.label}
                </StatusChip>
              )}
              {al.year ? <span className="font-mono text-xs text-ink-faint">{al.year}</span> : null}
              {al.album_type ? <span className="font-mono text-xs text-ink-faint">{al.album_type}</span> : null}
              <a
                href={`https://musicbrainz.org/release-group/${al.mbid}`}
                target="_blank"
                rel="noopener noreferrer"
                className="rounded px-1.5 py-0.5 font-mono text-xs font-bold"
                style={{ background: "#4a2a6a", color: "#fff" }}
              >
                MusicBrainz
              </a>
            </div>
            <div className="mt-2 font-mono text-sm text-ink-faint">
              {al.have_tracks}/{al.track_count} tracks{al.size_bytes ? ` · ${formatBytes(al.size_bytes)}` : ""}
              {al.held_tier ? ` · Held: ${al.held_tier}` : ""}
            </div>
            {target && (
              <div className="mt-1 flex flex-wrap items-center gap-x-2 text-sm text-ink-faint">
                <span>
                  Edition: {editionLabel(target)}
                  {al.edition_pinned ? " (kept)" : ""}
                  {(al.editions?.length ?? 0) > 1 ? ` · ${al.editions!.length} editions known` : ""}
                </span>
                {/* Always offered: opening it lists the album's editions from MusicBrainz
                    when that hasn't happened yet (the page itself never asks). */}
                <button className="font-semibold" style={{ color: "var(--accent-text)" }} onClick={() => setPicking(true)}>
                  Change edition…
                </button>
              </div>
            )}
            <div className="mt-4 flex flex-wrap items-center gap-2">
              <Button
                size="sm"
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
                className="disabled:opacity-50"
                style={{ border: `1px solid ${al.monitored ? "var(--accent)" : "var(--line)"}`, background: "transparent", color: al.monitored ? "var(--accent-text)" : "var(--ink-dim)" }}
              >
                {al.monitored ? "Monitored" : "Monitor"}
              </Button>
              {al.have_tracks > 0 && <FileActions albumId={al.id} onChanged={load} />}
            </div>
            <AlbumSearch album={al} onChanged={load} />
          </div>
        </div>

        {error && (
          <div className="mt-4 rounded-lg p-3 text-sm" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>
            {error}
          </div>
        )}

        {al.notes && al.notes.length > 0 && <UpgradeNotes albumId={al.id} notes={al.notes} onResolved={load} />}
        {(al.missing_tracks ?? 0) > 0 && <MissingFiles albumId={al.id} count={al.missing_tracks ?? 0} onDone={load} />}
        {picking && <EditionPicker albumId={al.id} onClose={() => setPicking(false)} onChanged={load} />}

        <h2 className="m-0 mb-3 mt-8 text-md font-bold">Tracks</h2>
        {tracks.length === 0 ? (
          <div className="rounded-xl p-8 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No track listing. MusicBrainz has no official release for this album yet — an
            announced record often has none until it ships.
          </div>
        ) : (
          discs.map((disc) => (
            <section key={disc} aria-label={discs.length > 1 ? `Disc ${disc}` : undefined} className="mb-4">
              {discs.length > 1 && (
                <h3 className="m-0 mb-2 text-sm font-semibold text-ink-dim">
                  Disc {disc}
                  {discTitle(disc) ? ` — ${discTitle(disc)}` : ""}
                </h3>
              )}
              <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
                {tracks.filter((t) => t.disc_number === disc).map((t) => (
                  <TrackRow key={t.id} t={t} />
                ))}
              </div>
            </section>
          ))
        )}

        {al.extras && al.extras.length > 0 && (
          <>
            {/* Files the album holds that are on no track of this edition: kept, never deleted. */}
            <h2 className="m-0 mb-1 mt-8 text-md font-bold">Other files</h2>
            <p className="m-0 mb-3 text-sm text-ink-dim">Files on record for this album that are on no track of this edition. They're kept as they are.</p>
            <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
              {al.extras.map((f) => (
                <div key={f.id} className="flex items-center gap-3 border-b px-4 py-2 text-base last:border-b-0" style={{ background: "var(--panel)", borderColor: "var(--line-soft)" }}>
                  <span className="min-w-0 flex-1 truncate font-mono text-sm" title={f.path}>{f.path.split(/[\\/]/).pop()}</span>
                  {f.quality ? (
                    <span className="flex-none rounded px-1.5 py-0.5 font-mono text-2xs font-bold uppercase" style={{ background: "var(--panel-2)", color: f.lossless ? "var(--good-text)" : "var(--ink-dim)" }}>
                      {f.quality}
                    </span>
                  ) : null}
                  <span className="flex-none font-mono text-xs text-ink-faint">{formatBytes(f.size_bytes)}</span>
                </div>
              ))}
            </div>
          </>
        )}
      </Page>
    </>
  );
}

// FileActions are the album's file actions (MUS-33, MUS-34): rename its files to the
// current music naming (a preview first; nothing moves until it's applied), and write the
// house tags into its files (staged copies swapped in, originals to the recycle bin).
function FileActions({ albumId, onChanged }: { albumId: number; onChanged: () => void }) {
  const confirm = useConfirm();
  const toast = useToast();
  const [renaming, setRenaming] = useState(false);
  const [jobId, setJobId] = useState<number | null>(null);
  const [starting, setStarting] = useState(false);
  const job = useJob(jobId, {
    giveUpMs: 2 * 60 * 60 * 1000,
    onDone: (j) => {
      toast(jobToast(j, "House tags written."), { tone: jobFailed(j) ? "error" : undefined });
      setJobId(null);
      onChanged();
    },
  });
  const retag = async () => {
    const ok = await confirm({
      title: "Write house tags into this album's files?",
      body: "Each file is copied, tagged in the house style (every MusicBrainz id, each credited artist, the cover) and checked, then swapped in. The originals go to the recycle bin. Files that are still seeding aren't touched: their copies in the library are replaced, not edited.",
      confirmLabel: "Write tags",
    });
    if (!ok) return;
    setStarting(true);
    try {
      const r = await musicNamingApi.retag(albumId);
      if (r.job_id) setJobId(r.job_id);
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setStarting(false);
    }
  };
  return (
    <>
      <Button size="sm" variant="secondary" onClick={() => setRenaming(true)}>Rename files…</Button>
      <Button size="sm" variant="secondary" onClick={retag} busy={starting || job.running} busyLabel="Writing tags…">Write house tags</Button>
      {renaming && <MusicRenameModal albumId={albumId} onClose={() => setRenaming(false)} onRenamed={onChanged} />}
    </>
  );
}

// UpgradeNotes explains the tracks the old library said had a file that the upgrade to
// editions couldn't give one (two tracks recorded one file, or a file with no path), and
// lets a person resolve each once they've fixed it or accepted it. Until then the search
// sweep leaves the album alone rather than grab it all again over a mix-up.
function UpgradeNotes({ albumId, notes, onResolved }: { albumId: number; notes: MusicNote[]; onResolved: () => void }) {
  const toast = useToast();
  const [busy, setBusy] = useState<number | null>(null);
  const resolve = async (id: number) => {
    setBusy(id);
    try {
      await musicApi.resolveAlbumNote(albumId, id);
      onResolved();
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setBusy(null);
    }
  };
  return (
    <section aria-label="Notes from the upgrade" className="mt-6 rounded-xl p-4" style={{ border: "1px solid var(--avoid)", background: "var(--panel)" }}>
      <h2 className="m-0 mb-1 text-md font-bold">Notes from the upgrade</h2>
      <p className="m-0 mb-3 text-sm text-ink-dim">
        Your library said these tracks had a file, but it couldn't be given to them. Searching skips this album until each note is resolved.
      </p>
      {notes.map((n) => (
        <div key={n.id} className="flex items-center gap-3 border-t py-2 text-sm" style={{ borderColor: "var(--line-soft)" }}>
          <span className="min-w-0 flex-1" title={n.path || undefined}>{n.note}</span>
          <Button size="sm" busy={busy === n.id} busyLabel="Resolving…" disabled={busy !== null} onClick={() => resolve(n.id)}>
            Resolve
          </Button>
        </div>
      ))}
    </section>
  );
}

function TrackRow({ t }: { t: MusicTrack }) {
  const lossless = t.lossless ?? (t.format ? isLosslessTier(t.format) : false);
  // The file check's marker (MUS-15): gone from disk, not found once, or changed elsewhere.
  const mark = trackFileMark(t);
  return (
    <div
      className="flex items-center gap-3 border-b px-4 py-2 last:border-b-0 text-base"
      style={{ background: "var(--panel)", borderColor: "var(--line-soft)" }}
    >
      <span className="w-[42px] flex-none font-mono text-xs text-ink-faint">{t.number || String(t.track_number).padStart(2, "0")}</span>
      <span className="min-w-0 flex-1">
        <span className="block truncate" style={{ color: t.has_file ? "var(--ink)" : "var(--ink-dim)" }}>
          {t.title}
        </span>
        {/* The track's own artist, only when it isn't the album's ("feat." on one song). */}
        {t.artist_credit ? <span className="block truncate text-xs text-ink-faint">{t.artist_credit}</span> : null}
      </span>
      {mark && (
        <span
          className="flex-none rounded px-1.5 py-0.5 font-mono text-2xs font-semibold uppercase"
          title={mark[1]}
          style={{ background: "var(--panel-2)", color: t.missing ? "var(--reject-text)" : "var(--avoid-text)" }}
        >
          {mark[0]}
        </span>
      )}
      {t.format ? (
        // Green only for lossless: the tier is measured from the file, so an MP3 in a
        // release labelled FLAC shows as the MP3 it is.
        <span
          className="flex-none rounded px-1.5 py-0.5 font-mono text-2xs font-bold uppercase"
          title={fileFacts(t)}
          style={{ background: "var(--panel-2)", color: lossless ? "var(--good-text)" : "var(--ink-dim)" }}
        >
          {t.format}
        </span>
      ) : null}
      <span className="w-[52px] flex-none text-right font-mono text-xs text-ink-faint">{fmtDuration(t.duration_sec)}</span>
      <span
        className="w-[74px] flex-none text-right font-mono text-2xs font-semibold uppercase"
        style={{ color: t.has_file ? "var(--good-text)" : t.missing ? "var(--reject-text)" : "var(--avoid-text)" }}
      >
        {t.has_file ? "downloaded" : t.missing ? "file missing" : "wanted"}
      </span>
    </div>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <>
      <PageHeader title="Music" />
      <Page>{children}</Page>
    </>
  );
}
