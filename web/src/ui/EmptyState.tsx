import type { ReactNode } from "react";

export interface EmptyStateProps {
  icon?: ReactNode;
  title?: ReactNode;
  body?: ReactNode;
  /** A button or link: the next step ("Add movie"). */
  action?: ReactNode;
}

// EmptyState is the genuine "there's nothing here" message, shown only once the
// server has answered with an empty list.
export function EmptyState({ icon, title, body, action }: EmptyStateProps) {
  return (
    <div className="rounded-xl p-12 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>
      {icon && <div className="mb-3 flex justify-center" style={{ color: "var(--ink-faint)" }}>{icon}</div>}
      {title && <div className="mb-1 text-[13.5px] font-semibold" style={{ color: "var(--ink)" }}>{title}</div>}
      {body}
      {action && <div className="mt-4 flex justify-center">{action}</div>}
    </div>
  );
}
