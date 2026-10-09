import { Link } from "react-router-dom";
import type { IndexerIssue, ReleaseList } from "../lib/api";
import { LINKS } from "../lib/links";

// What a search's indexers did, in numbers: how many were asked, how many failed and how
// many were left alone because they're paused. `searched` counts only the ones asked.
export function issueCounts(list: Pick<ReleaseList, "indexer_issues" | "searched"> | null | undefined) {
  const issues = list?.indexer_issues ?? [];
  const failed = issues.filter((i) => !i.skipped).length;
  const paused = issues.length - failed;
  const total = (list?.searched ?? 0) + paused;
  return { failed, paused, total, allFailed: issues.length > 0 && failed + paused >= total };
}

// The headline: "2 of 6 indexers failed", "Every indexer failed", "1 indexer is paused".
export function issueHeadline(list: Pick<ReleaseList, "indexer_issues" | "searched">): string {
  const { failed, paused, total, allFailed } = issueCounts(list);
  if (allFailed) {
    if (failed === 0) return total === 1 ? "Your indexer is paused after repeated failures" : `All ${total} indexers are paused after repeated failures`;
    return total === 1 ? "Your indexer failed" : `All ${total} indexers failed`;
  }
  const parts: string[] = [];
  if (failed > 0) parts.push(`${failed} of ${total} indexer${total === 1 ? "" : "s"} failed`);
  if (paused > 0) parts.push(`${paused} paused after repeated failures`);
  return parts.join(" · ");
}

// The empty-list line. A search nobody could answer must never read as "nothing exists".
export function emptyReleasesMessage(list: Pick<ReleaseList, "indexer_issues" | "searched"> | null | undefined, what = "releases"): string {
  const { allFailed, failed } = issueCounts(list);
  if (allFailed) return "Couldn't search: every indexer failed. See the errors above.";
  const n = Math.max(0, (list?.searched ?? 0) - failed); // the ones that answered
  if (n > 0) return `No ${what} found on ${n} indexer${n === 1 ? "" : "s"}.`;
  return `No ${what} found on your indexers.`;
}

// IndexerIssues is the banner at the top of every search modal naming the indexers that
// failed, so "nothing found" and "the tracker is down" no longer look the same. Renders
// nothing when every indexer answered.
export function IndexerIssues({ list, className = "mb-3" }: { list: Pick<ReleaseList, "indexer_issues" | "searched"> | null | undefined; className?: string }) {
  const issues: IndexerIssue[] = list?.indexer_issues ?? [];
  if (!list || issues.length === 0) return null;
  return (
    <div role="status" className={`${className} rounded-lg p-2.5 text-[12px]`} style={{ background: "var(--reject-soft)", border: "1px solid var(--reject)", color: "var(--reject)" }}>
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <span className="font-semibold">{issueHeadline(list)}</span>
        <Link to={LINKS.indexers} className="text-[11px] font-semibold hover:underline" style={{ color: "var(--reject)" }}>Indexers →</Link>
      </div>
      <ul className="m-0 mt-1 list-none space-y-0.5 p-0 text-[11.5px]">
        {issues.map((i) => (
          <li key={i.indexer} className="break-words">
            <span className="font-semibold">{i.indexer}</span>
            <span style={{ opacity: 0.85 }}> — {i.error || (i.skipped ? "paused" : "failed")}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
