import type { MediaRequest } from "../lib/api";
import { formatAgo } from "../lib/format";
import { StatusChip } from "../ui";

// Bits of a request decision shared by the RequestSheet and the Requests page: the reason
// box staff fill in when declining, and the lines that say who decided and that a request
// was asked for again. Notes and reasons are only ever rendered as text.

export const DECLINE_REASON_MAX = 280;

// Reasons staff give often enough to be one tap.
export const DECLINE_PICKS = ["Already available elsewhere", "Not something we’ll add", "Couldn’t find a good copy"];

export function DeclineReasonField({ value, onChange, disabled }: { value: string; onChange: (v: string) => void; disabled?: boolean }) {
  return (
    <div className="flex flex-col gap-1.5">
      <label className="flex flex-col gap-1 text-[12px]">
        <span className="font-mono text-[9.5px] uppercase text-ink-faint">Reason (they’ll see it)</span>
        <textarea
          value={value}
          onChange={(e) => onChange(e.target.value.slice(0, DECLINE_REASON_MAX))}
          maxLength={DECLINE_REASON_MAX}
          rows={2}
          disabled={disabled}
          placeholder="Optional"
          className="rounded-lg px-3 py-2 text-[12.5px]"
          style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
        />
      </label>
      <div className="flex flex-wrap gap-1.5">
        {DECLINE_PICKS.map((p) => (
          <button
            key={p}
            type="button"
            onClick={() => onChange(p)}
            disabled={disabled}
            aria-pressed={value === p}
            className="min-h-[32px] rounded-full px-3 text-[11.5px]"
            style={{ background: value === p ? "var(--accent-soft)" : "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}
          >
            {p}
          </button>
        ))}
      </div>
    </div>
  );
}

// decisionLine says who made the last decision and when, for staff: "Approved by sam ·
// 2h ago", "Auto-approved · 3d ago", "Declined by sam · 1d ago". Empty while undecided.
export function decisionLine(rq: MediaRequest, now = Date.now()): string {
  if (!rq.decided_at || rq.status === "pending") return "";
  const when = formatAgo(rq.decided_at * 1000, now);
  const who = rq.decided_by_name ? ` by ${rq.decided_by_name}` : "";
  if (rq.status === "approved") return `${rq.decided_by_name ? `Approved${who}` : "Auto-approved"} · ${when}`;
  return `Declined${who} · ${when}`;
}

// ReRequestFlag marks a pending request asked for again after a decline, with the reason
// it was declined before.
export function ReRequestFlag({ rq }: { rq: MediaRequest }) {
  if (!rq.rerequest || rq.status !== "pending") return null;
  return (
    <span className="inline-flex min-w-0 flex-wrap items-center gap-1.5 text-[11.5px] text-ink-dim">
      <StatusChip tone="avoid">Re-request</StatusChip>
      {rq.decline_reason && <span className="min-w-0">Declined before: {rq.decline_reason}</span>}
    </span>
  );
}
