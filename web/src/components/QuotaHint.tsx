import { useEffect } from "react";
import { quotaLeft, quotaLine, useQuota, type QuotaKind } from "../lib/quota";

// QuotaHint is the line under a title sheet's Request button: "3 movie requests left this
// week", or at zero when the next frees up. onOut tells the sheet to disable the button.
// Loaded with the sheet, not with Discover; nothing shows when there's no limit.
export function QuotaHint({ kind, onOut }: { kind: QuotaKind; onOut?: (out: boolean) => void }) {
  const q = useQuota();
  const left = quotaLeft(q, kind);
  useEffect(() => { onOut?.(left === 0); }, [left, onOut]);
  const line = quotaLine(q, kind);
  if (!line) return null;
  return (
    <div className="mt-1.5 text-[11.5px]" style={{ color: left === 0 ? "var(--avoid-text)" : "var(--ink-faint)" }}>
      {line}
    </div>
  );
}
