import { api, type AttemptSummary, type SearchAttempt } from "../lib/api";
import { invalidate, useQuery } from "../lib/query";
import { attemptLine, emptyTriesText, latestPerScope, nextTryText, outcomeTone, scopeLabel } from "../lib/searchOutcome";

type Kind = SearchAttempt["media_type"];

const searchesKey = (kind: Kind, id: number) => `searches:${kind}:${id}:`;

// useSearchAttempts is a title's stored search attempts, newest first (GET
// /api/v1/searches; staff only — anyone else gets an empty list). Pages sharing a title
// share one request.
export function useSearchAttempts(kind: Kind, id: number): SearchAttempt[] {
  const { data } = useQuery(searchesKey(kind, id), () => api.searches(kind, id, { limit: 20 }), { staleMs: 15_000 });
  return data ?? [];
}

// refreshSearches refetches a title's attempts, after a search of it finished.
export function refreshSearches(kind: Kind, id: number) {
  invalidate(searchesKey(kind, id));
}

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
// seasons, episodes) or a book (each edition). Renders nothing until there is one.
export function LastSearches({ kind, id, className = "mt-2", max = 4 }: { kind: Kind; id: number; className?: string; max?: number }) {
  const rows = latestPerScope(useSearchAttempts(kind, id), max);
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
