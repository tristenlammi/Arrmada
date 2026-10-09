import { useLocation } from "react-router-dom";
import { crumbFor } from "../lib/nav";
import { useTitle } from "../lib/title";

// The breadcrumb comes from the sidebar (crumbFor), so moving a page in nav.ts moves its
// crumb too. tail adds a level below the sidebar entry ("Library / Books / Author"); crumb
// overrides the whole thing and null hides it. The header names the browser tab as well,
// so a detail page's tab reads "Dune · Arrmada" rather than the route's generic "Movie".
export function PageHeader({ title, crumb, tail }: { title: string; crumb?: string | null; tail?: string }) {
  useTitle(title);
  const { pathname } = useLocation();
  const base = crumb === undefined ? crumbFor(pathname) : crumb;
  const line = base && tail ? `${base} / ${tail}` : base;
  return (
    <div
      className="sticky top-0 z-30 flex items-center gap-4 px-6 py-3.5"
      style={{ borderBottom: "1px solid var(--line)", background: "var(--bg)" }}
    >
      <div>
        <h1 className="m-0 text-[17px] font-bold tracking-[-0.01em]">{title}</h1>
        {line && <div className="font-mono text-[11px] text-ink-faint">{line}</div>}
      </div>
    </div>
  );
}
