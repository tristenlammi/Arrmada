import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { useMe, isStaff } from "../lib/me";

// NotFound is the answer to a mistyped or stale address. It says plainly that the page
// doesn't exist (not that it's "coming") and offers the way back.
export function NotFound() {
  const staff = isStaff(useMe().user);
  const btn = "rounded-lg px-3.5 py-2 text-[12.5px] font-semibold";
  return (
    <>
      <PageHeader title="Page not found" crumb={null} />
      <div className="mx-auto grid w-full max-w-[900px] place-items-center px-6 py-24">
        <div className="max-w-[380px] text-center">
          <div className="mx-auto mb-4 grid h-11 w-11 place-items-center rounded-xl" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>
            <svg width="22" height="22" viewBox="0 0 24 24" fill="none" aria-hidden>
              <circle cx="11" cy="11" r="7" stroke="currentColor" strokeWidth="2" />
              <path d="M20 20l-3.5-3.5" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
            </svg>
          </div>
          <h2 className="m-0 text-base font-bold">Page not found</h2>
          <p className="mx-auto mt-2 text-[12.5px] leading-relaxed text-ink-dim">That address doesn’t exist in Arrmada.</p>
          <div className="mt-4 flex justify-center gap-2">
            {staff && <Link to="/" className={btn} style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Dashboard</Link>}
            <Link to="/discover" className={btn} style={staff ? { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } : { background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Discover</Link>
          </div>
        </div>
      </div>
    </>
  );
}
