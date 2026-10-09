import { api, type AttemptSummary, type SearchAttempt } from "../lib/api";
import { useQuery } from "../lib/query";
import { attemptLine, emptyTriesText, latestPerScope, nextTryText, outcomeTone, scopeLabel } from "../lib/searchOutcome";

// LastSearchLine is the "why isn't it downloading?" line for one title: how its last
// search went, the run of empty searches behind it, and when the sweep looks next.
// Presentational only, so a page that already has the summary (the movie DTO) or a card
// that wants it (the Acquisition card, the Wanted view) can reuse it.
export function LastSearchLine({ summary, nextSearchAt, className = "mt-2" }: { summary?: AttemptSummary | null; nextSearchAt?: string | null; className?: string }) {
  const next = nextTryText(nextSearchAt);
  if (!summary && !next) return null;
  const tail = [emptyTriesText(summary), next].filter(Boolean).join(" · ");
  return (
    <div className={`${className} text-[11.5px] leading-relaxed`} data-testid="last-search">
      {summary ? (
        <span style={{ color: outcomeTone(summary.latest.outcome) }}>{attemptLine(summary.latest)}</span>
      ) : (
        <span className="text-ink-dim">Not searched yet</span>
      )}
      {tail && <span className="text-ink-faint"> · {tail}</span>}
    </div>
  );
}

// LastSearches lists the newest search under each scope for a series (whole show,
// seasons, episodes) or a book (each edition), fetched from GET /api/v1/searches. Staff
// only, like the endpoint: for anyone else it renders nothing. refreshKey refetches.
export function LastSearches({ kind, id, refreshKey, className = "mt-2" }: { kind: SearchAttempt["media_type"]; id: number; refreshKey?: unknown; className?: string }) {
  const { data } = useQuery(`searches:${kind}:${id}:${String(refreshKey ?? "")}`, () => api.searches(kind, id, { limit: 20 }), { staleMs: 15_000 });
  const rows = latestPerScope(data ?? []);
  if (rows.length === 0) return null;
  return (
    <div className={`${className} flex flex-col gap-0.5 text-[11.5px] leading-relaxed`} data-testid="last-searches">
      {rows.map((a) => (
        <div key={a.scope || "all"} style={{ color: outcomeTone(a.outcome) }}>
          {scopeLabel(a.scope) && <span className="mr-1.5 font-mono text-[10.5px] font-bold text-ink-faint">{scopeLabel(a.scope)}</span>}
          {attemptLine(a)}
        </div>
      ))}
    </div>
  );
}
