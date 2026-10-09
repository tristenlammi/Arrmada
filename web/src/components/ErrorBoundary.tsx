import { Component, type ErrorInfo, type ReactNode } from "react";
import { reloadForChunkError } from "../lib/chunkReload";

interface Props {
  children: ReactNode;
  // When this changes (the layouts pass the pathname) a caught error is cleared,
  // so navigating to another page renders it normally.
  resetKey?: unknown;
  fallback?: ReactNode;
}

interface State {
  error: Error | null;
  reloading: boolean;
}

// ErrorBoundary stops one render exception from blanking the whole app. The root
// one wraps everything; each layout wraps its page outlet so the sidebar and top
// bar keep working around a broken page.
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null, reloading: false };

  static getDerivedStateFromError(error: unknown): Partial<State> {
    return { error: error instanceof Error ? error : new Error(String(error)) };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Render error:", error, info.componentStack);
    // A stale chunk after a deploy: reload once rather than show the card.
    if (reloadForChunkError(error)) this.setState({ reloading: true });
  }

  componentDidUpdate(prev: Props) {
    if (this.state.error && !Object.is(prev.resetKey, this.props.resetKey)) {
      this.setState({ error: null, reloading: false });
    }
  }

  render() {
    const { error, reloading } = this.state;
    if (!error) return this.props.children;
    if (reloading) return null;
    if (this.props.fallback !== undefined) return this.props.fallback;
    return <ErrorCard error={error} />;
  }
}

export function ErrorCard({ error }: { error: Error }) {
  return (
    <div className="grid min-h-full place-items-center px-5 py-10">
      <div role="alert" className="w-full max-w-[520px] rounded-2xl border border-line bg-panel p-5 shadow-panel">
        <h2 className="m-0 text-[16px] font-bold">Something broke on this page</h2>
        <p className="m-0 mt-1.5 text-[12.5px] text-ink-dim">
          Reloading usually fixes it. If it keeps happening, the details below help track it down.
        </p>
        <details className="mt-3 rounded-lg border border-line bg-panel-2 px-3 py-2">
          <summary className="cursor-pointer text-[12px] font-semibold text-ink-dim">Details</summary>
          <div className="mt-2 break-words font-mono text-[11.5px] text-ink">{error.message || String(error)}</div>
          {error.stack && (
            <pre className="thin-scroll mt-2 max-h-[220px] overflow-auto whitespace-pre-wrap break-words font-mono text-[10.5px] text-ink-faint">{error.stack}</pre>
          )}
        </details>
        <div className="mt-4 flex gap-2">
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="rounded-lg bg-accent-grad px-4 py-2 text-[12.5px] font-semibold text-accent-ink"
          >
            Reload
          </button>
          {/* A full load of /, which sends requesters on to /discover. */}
          <a href="/" className="rounded-lg border border-line px-4 py-2 text-[12.5px] font-semibold text-ink-dim no-underline">
            Go home
          </a>
        </div>
      </div>
    </div>
  );
}
