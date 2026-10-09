import { Button } from "./Button";

export interface ErrorStateProps {
  /** What we were trying to load, for the heading: "Couldn't load your movies". */
  what: string;
  /** The server's own words. */
  message?: string;
  onRetry?: () => void;
  busy?: boolean;
}

// ErrorState is what a list shows when its first load failed: plainly an error,
// with the server's message and a Retry — never the onboarding "nothing here yet"
// text, which would claim the library is empty when it may well be full.
export function ErrorState({ what, message, onRetry, busy }: ErrorStateProps) {
  return (
    <div role="alert" className="rounded-xl p-8 text-center" style={{ border: "1px solid var(--reject)", background: "var(--reject-soft)" }}>
      <div className="text-[13.5px] font-semibold" style={{ color: "var(--reject-text)" }}>Couldn’t load {what}</div>
      <p className="mx-auto mt-1.5 max-w-[60ch] text-[12.5px] text-ink-dim">
        {message ? `${message} — this is a loading error, not an empty list.` : "This is a loading error, not an empty list."}
      </p>
      {onRetry && (
        <Button className="mt-4" onClick={onRetry} busy={busy} busyLabel="Retrying…">Retry</Button>
      )}
    </div>
  );
}

// StaleBanner is the thin notice above content that's still showing from the last
// good load while refreshing it failed.
export function StaleBanner({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div role="status" className="mb-3 flex items-center gap-3 rounded-lg px-3 py-2 text-[12px]" style={{ border: "1px solid var(--line)", background: "var(--panel)", color: "var(--ink-dim)" }}>
      <span className="h-2 w-2 flex-none rounded-full" style={{ background: "var(--avoid)" }} />
      <span className="min-w-0 flex-1">Showing the last loaded list — refreshing failed: {message}</span>
      {onRetry && <button type="button" onClick={onRetry} className="flex-none font-semibold" style={{ color: "var(--accent-text)" }}>Retry</button>}
    </div>
  );
}
