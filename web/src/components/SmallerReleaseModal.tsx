import { useState } from "react";
import { api, type ReleaseList, type SmallerList } from "../lib/api";
import { formatBytes } from "../lib/format";
import { ReleaseSearchModal } from "./ReleaseSearchModal";

// "Find a smaller release": the interactive search for one of a film's files, narrowed by
// the server to releases that would be a sensible smaller replacement — same resolution and
// edition, HEVC or AV1 inside the profile's bitrate window and never under its floor, and
// nothing the file has given up (HDR, Dolby Vision, Atmos, lossless audio, the profile's
// languages). Each says why it qualifies and what it gives up; the ones left out are listed
// with why. A grab is an ordinary manual grab, marked so the download is checked against
// this file when it lands.
export function SmallerReleaseModal({ movieId, versionId = 0, title, convertSaveBytes, onGrabbed, onClose }: {
  movieId: number;
  versionId?: number;
  title: string;
  // What Convert expects re-encoding the file would save, shown beside the releases.
  convertSaveBytes?: number;
  onGrabbed?: () => void;
  onClose: () => void;
}) {
  return (
    <ReleaseSearchModal
      title={`Find a smaller release — ${title}`}
      subtitle="Releases that could replace this file with a smaller one without losing what it has."
      fetchReleases={() => api.movieSmallerReleases(movieId, versionId)}
      onGrab={async (rel) => {
        await api.grab({ token: rel.token ?? "", movie_id: movieId, version_id: versionId || undefined, replace_smaller: true });
        onGrabbed?.();
      }}
      onClose={onClose}
      header={(list) => <CurrentFile list={list as SmallerList} convertSaveBytes={convertSaveBytes} />}
      rowExtra={(rel) => rel.replace && (
        <div className="mt-1.5 text-xs" data-testid="replace-fit">
          <div style={{ color: "var(--good-text)" }}>{rel.replace.why.map((w) => `✓ ${w}`).join("  ·  ")}</div>
          {rel.replace.loses?.map((l) => <div key={l} className="text-ink-faint">− {l}</div>)}
        </div>
      )}
      emptyMessage={(list: ReleaseList) => {
        const n = (list as SmallerList).not_suitable?.length ?? 0;
        return n > 0
          ? `None of the ${n} release${n === 1 ? "" : "s"} found would be a sensible replacement — Convert is the way to shrink this one.`
          : "No releases found for this film.";
      }}
    />
  );
}

function CurrentFile({ list, convertSaveBytes }: { list: SmallerList; convertSaveBytes?: number }) {
  const [showOthers, setShowOthers] = useState(false);
  const c = list.current;
  if (!c) return null;
  const facts = [
    c.resolution, c.codec?.toUpperCase(), c.dolby_vision ? "Dolby Vision" : c.hdr && c.hdr !== "SDR" ? c.hdr : null,
    c.atmos ? "Atmos" : null, c.lossless ? "lossless audio" : null, c.edition,
    c.audio_langs?.length ? `audio ${c.audio_langs.map((l) => l.toUpperCase()).join("·")}` : null,
  ].filter(Boolean);
  const others = list.not_suitable ?? [];
  return (
    <div className="mb-3 rounded-lg p-3 text-sm" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }} data-testid="smaller-current">
      <div>
        <b>Your file:</b> {c.size_gb.toFixed(1)} GB{c.bitrate_mbps ? ` · ${c.bitrate_mbps.toFixed(1)} Mb/s` : ""}{facts.length ? ` · ${facts.join(" · ")}` : ""}
        {c.runtime_min ? ` · ${c.runtime_min}m` : ""}
      </div>
      <div className="mt-0.5 text-xs text-ink-dim">
        A replacement must be at least 20% smaller, HEVC or AV1, and stay above {c.floor_mbps.toFixed(1)} Mb/s
        {c.floor_from_profile ? " (your profile's floor)" : " (the built-in floor — your profile sets none for this resolution)"}
        {c.ceiling_mbps ? ` and under ${c.ceiling_mbps.toFixed(1)} Mb/s` : ""}.
        {convertSaveBytes && convertSaveBytes > 0 ? ` Converting it is expected to save about ${formatBytes(convertSaveBytes)}.` : ""}
      </div>
      {list.notes?.map((n) => <div key={n} className="mt-0.5 text-xs text-ink-faint">{n}</div>)}
      {others.length > 0 && (
        <div className="mt-1.5">
          <button type="button" onClick={() => setShowOthers((v) => !v)} aria-expanded={showOthers} className="text-xs font-semibold" style={{ color: "var(--accent-text)" }}>
            {showOthers ? "Hide" : "Show"} the {others.length} left out
          </button>
          {showOthers && (
            <ul className="m-0 mt-1 flex list-none flex-col gap-1 p-0 text-xs" data-testid="not-suitable">
              {others.map((o) => (
                <li key={o.title} className="min-w-0">
                  <span className="break-all font-mono text-ink-faint">{o.title}</span>
                  <span className="text-ink-dim"> · {o.size_gb.toFixed(1)} GB — {o.why}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
