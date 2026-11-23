import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { SERVER_LABEL } from "../components/brand";
import { ApiError, type PersonProfile } from "../lib/api";
import { insightsApi } from "../lib/insightsApi";
import { useSearchParamState } from "../lib/useSearchState";
import { EmptyState, ErrorState } from "../ui";
import { Page } from "../ui/Page";
import { HBarChart } from "./insights/charts";
import { Avatar, Chip, DECISION, RowServer, StatTile, fmtDate, fmtDay, fmtDur, fmtStall, geoLabel } from "./insights/shared";
import { progressBadge } from "./insights/HistoryView";

// A person's People page: who they are on their server (Plex or Jellyfin — someone on both
// has a page per server), how much they watch, what, on which devices and from where, how
// far they get, and how often their streams stall (Plex only: Jellyfin reports no
// buffering). It is the owner's view of their own server, for staff only (the API refuses
// anyone else), and it shows recorded video plays only — audiobook listening never appears
// here.

const WINDOWS = ["0", "30", "90", "365"] as const;
type Win = (typeof WINDOWS)[number];
const WINDOW_LABEL: Record<Win, string> = { "0": "All time", "30": "30d", "90": "90d", "365": "1y" };

const card = { border: "1px solid var(--line)", background: "var(--panel)" };

export function InsightsPerson({ id }: { id: string }) {
  const [win, setWin] = useSearchParamState<Win>("window", "0", { allowed: WINDOWS });
  const [p, setP] = useState<PersonProfile | null>(null);
  const [err, setErr] = useState<{ missing: boolean; message: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    setBusy(true);
    return insightsApi.insightsPerson(id, Number(win))
      .then((r) => { setP(r); setErr(null); })
      .catch((e) => setErr({ missing: e instanceof ApiError && e.status === 404, message: (e as Error).message }))
      .finally(() => setBusy(false));
  }, [id, win]);
  useEffect(() => { load(); }, [load]);

  const name = p?.username || "Person";
  return (
    <>
      <PageHeader title={p ? name : "Person"} tail="People" />
      <Page>
        <Link to="/insights?tab=people" className="mb-3 inline-block text-sm font-semibold" style={{ color: "var(--accent-text)" }}>← All people</Link>
        {err?.missing ? (
          <EmptyState title="Nobody by that id" body={<p className="text-sm">Nobody with this id on Plex or Jellyfin has watched anything here.</p>} />
        ) : err && !p ? (
          <ErrorState what="this person" message={err.message} onRetry={load} busy={busy} />
        ) : !p ? (
          <div className="rounded-xl p-10 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>Loading…</div>
        ) : (
          <Profile p={p} win={win} setWin={setWin} />
        )}
      </Page>
    </>
  );
}

function Profile({ p, win, setWin }: { p: PersonProfile; win: Win; setWin: (w: Win) => void }) {
  const windowed = win !== "0";
  const span = windowed ? `in the last ${WINDOW_LABEL[win]}` : "all time";
  const decTotal = p.decisions.direct_play + p.decisions.direct_stream + p.decisions.transcode;
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <Avatar url={p.avatar_url} name={p.username} size={56} />
          <div className="min-w-0">
            <div className="flex min-w-0 items-center gap-2"><span className="truncate text-2xl font-bold">{p.username || p.id}</span><RowServer server={p.server} /></div>
            <div className="text-sm text-ink-dim">
              {p.total_plays > 0 ? <>First play {fmtDay(p.first_seen)} · last {fmtDate(p.last_seen)}{windowed ? ` (${span})` : ""}</> : `No plays ${span}`}
            </div>
          </div>
        </div>
        <div className="flex flex-wrap gap-1">{WINDOWS.map((w) => <Chip key={w} active={win === w} onClick={() => setWin(w)}>{WINDOW_LABEL[w]}</Chip>)}</div>
      </div>

      <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(min(170px, 100%), 1fr))" }}>
        <StatTile label="Plays" value={p.total_plays.toLocaleString()} sub={span} />
        <StatTile label="Watch time" value={fmtDur(p.total_secs)} sub={span} />
        <StatTile label="Finished" value={p.completion_of ? `${p.completion_pct}%` : "—"}
          sub={p.completion_of ? `of ${p.completion_of.toLocaleString()} plays reached 90%` : "no runtimes recorded"} />
        {p.server === "jellyfin" ? (
          <StatTile label="Stalls" value="—" sub={`${SERVER_LABEL.jellyfin} doesn't report buffering`} />
        ) : (
          <StatTile label="Stalls" value={p.stalls.events.toLocaleString()}
            sub={`${p.stalls.rate_pct}% of plays${p.stalls.stall_ms ? ` · ${fmtStall(p.stalls.stall_ms)} stalled` : ""}`}
            color={p.stalls.events ? "var(--avoid-text)" : undefined} />
        )}
      </div>

      {p.top_titles.length > 0 && (
        <section className="rounded-xl p-4" style={card}>
          <h3 className="mb-2.5 text-md font-bold">Top titles</h3>
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(88px, 1fr))" }}>
            {p.top_titles.map((t) => (
              <div key={t.title} className="min-w-0">
                <div className="aspect-[2/3] w-full overflow-hidden rounded-lg" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
                  {t.thumb_url ? <img src={t.thumb_url} alt="" loading="lazy" className="h-full w-full object-cover" /> : null}
                </div>
                <div className="mt-1 truncate text-xs font-semibold" title={t.title}>{t.title}</div>
                <div className="text-xs text-ink-faint">{t.plays} play{t.plays === 1 ? "" : "s"} · {fmtDur(t.secs)}</div>
              </div>
            ))}
          </div>
        </section>
      )}

      <div className="grid gap-4" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(min(300px, 100%), 1fr))" }}>
        <section className="rounded-xl p-4" style={card}>
          <h3 className="mb-2 text-base font-bold">How they stream</h3>
          {decTotal === 0 ? <div className="text-xs text-ink-faint">—</div> : (
            <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
              {(["direct_play", "direct_stream", "transcode"] as const).map((k) => {
                const n = p.decisions[k];
                return (
                  <li key={k} className="flex items-center gap-2 text-sm">
                    <span className="inline-block h-2 w-2 flex-none rounded-full" style={{ background: DECISION[k].color }} />
                    <span className="flex-1 text-ink-dim">{DECISION[k].label}</span>
                    <span className="font-mono text-xs tabular-nums">{n.toLocaleString()} · {Math.round((n * 100) / decTotal)}%</span>
                  </li>
                );
              })}
            </ul>
          )}
        </section>
        <section className="rounded-xl p-4" style={card}>
          <h3 className="mb-2 text-base font-bold">Platforms</h3>
          <HBarChart rows={p.platforms.map((x) => ({ label: x.name, value: x.plays }))} />
        </section>
        <section className="rounded-xl p-4" style={card}>
          <h3 className="mb-2 text-base font-bold">Players</h3>
          <HBarChart rows={p.players.map((x) => ({ label: x.name, value: x.plays }))} />
        </section>
      </div>

      <section className="rounded-xl p-4" style={card}>
        <h3 className="mb-2 text-base font-bold">Where they watch from</h3>
        {p.ips.length === 0 ? <div className="text-xs text-ink-faint">No addresses recorded.</div> : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <thead><tr>{["Address", "Location", "Plays", "Last seen"].map((h) => <th key={h} scope="col" className="px-2 py-1.5 text-left font-mono text-2xs font-bold uppercase tracking-wide text-ink-faint">{h}</th>)}</tr></thead>
              <tbody>
                {p.ips.map((ip) => (
                  <tr key={ip.ip} style={{ borderTop: "1px solid var(--line-soft)" }}>
                    <td className="px-2 py-1.5 font-mono text-xs">{ip.ip}</td>
                    <td className="px-2 py-1.5 text-ink-dim">{geoLabel(ip.geo)}</td>
                    <td className="px-2 py-1.5 font-mono tabular-nums">{ip.plays.toLocaleString()}</td>
                    <td className="whitespace-nowrap px-2 py-1.5 font-mono text-xs text-ink-dim">{fmtDate(ip.last_seen)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="rounded-xl p-4" style={card}>
        <div className="mb-2 flex items-center justify-between gap-2">
          <h3 className="m-0 text-base font-bold">Recent plays</h3>
          <Link to={`/insights?tab=history&user=${encodeURIComponent(p.id)}`} className="text-sm font-semibold" style={{ color: "var(--accent-text)" }}>All plays →</Link>
        </div>
        {p.recent.length === 0 ? <div className="text-xs text-ink-faint">Nothing {span}.</div> : (
          <ul className="m-0 flex list-none flex-col p-0">
            {p.recent.map((r, i) => {
              const badge = progressBadge(r);
              return (
                <li key={r.id} className="flex items-center gap-3 py-1.5" style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                  <span className="h-12 w-8 flex-none overflow-hidden rounded" style={{ background: "var(--panel-2)" }}>
                    {r.thumb_url ? <img src={r.thumb_url} alt="" loading="lazy" className="h-full w-full object-cover" /> : null}
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-semibold">{r.title}</div>
                    <div className="truncate text-xs text-ink-faint">{[r.subtitle, r.player || r.platform].filter(Boolean).join(" · ")}</div>
                  </div>
                  <div className="flex-none text-right">
                    <div className="font-mono text-xs text-ink-dim">{fmtDate(r.started_at)}</div>
                    <div className="text-xs text-ink-faint">{fmtDur(r.watched_secs)}{badge ? ` · ${badge.label}` : ""}</div>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
}
