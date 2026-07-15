import { NavLink } from "react-router-dom";
import { NAV } from "../lib/nav";
import { useMe } from "../lib/me";
import { FleetMark } from "./FleetMark";

function toggleTheme() {
  const root = document.documentElement;
  const current =
    root.getAttribute("data-theme") ??
    (window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
  root.setAttribute("data-theme", current === "dark" ? "light" : "dark");
}

export function Sidebar({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { booksEnabled, musicEnabled } = useMe();
  // Hide nav entries for modules an admin has turned off.
  const nav = NAV.map((group) => ({
    ...group,
    items: group.items.filter((item) =>
      (booksEnabled || item.to !== "/books") && (musicEnabled || item.to !== "/music")),
  }));
  return (
    <>
      {/* Dim + click-to-close backdrop, only while the drawer is open on narrow windows. */}
      {open && <div className="fixed inset-0 z-40 bg-black/50 lg:hidden" onClick={onClose} />}

      <aside
        className={`fixed inset-y-0 left-0 z-50 flex w-[236px] flex-none transform flex-col overflow-y-auto bg-sidebar transition-transform duration-200 lg:static lg:z-auto lg:translate-x-0 ${
          open ? "translate-x-0" : "-translate-x-full"
        }`}
        style={{ borderRight: "1px solid var(--line)" }}
      >
        <div className="flex items-center gap-2.5 px-[18px] pb-3 pt-[18px]">
          <span
            className="grid h-[30px] w-[30px] place-items-center rounded-[9px]"
            style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}
          >
            <FleetMark className="h-[18px] w-[18px]" />
          </span>
          <span className="text-[15px] font-extrabold tracking-[0.12em]">ARRMADA</span>
        </div>

        <nav className="flex flex-col gap-0.5 px-2.5 pt-2">
          {nav.map((group, gi) => (
            <div key={gi} className="flex flex-col gap-0.5">
              {group.group && (
                <div className="px-2.5 pb-1.5 pt-3.5 font-mono text-[9.5px] font-bold uppercase tracking-[0.12em] text-ink-faint">
                  {group.group}
                </div>
              )}
              {group.items.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.end}
                  onClick={onClose}
                  className="flex w-full items-center gap-2.5 rounded-[9px] px-2.5 py-2 text-left text-[13.5px] transition-colors"
                  style={({ isActive }) =>
                    isActive
                      ? { background: "var(--accent-soft)", color: "var(--accent)", fontWeight: 600 }
                      : { color: "var(--ink-dim)" }
                  }
                >
                  {item.label}
                </NavLink>
              ))}
            </div>
          ))}
        </nav>

        <div className="mt-auto flex items-center gap-2.5 p-3.5" style={{ borderTop: "1px solid var(--line)" }}>
          <span className="grid h-[30px] w-[30px] place-items-center rounded-full bg-[#5f5142] text-xs font-bold text-white">T</span>
          <div>
            <div className="text-[12.5px] font-semibold">local-dev</div>
            <div className="font-mono text-[10.5px] text-ink-faint">Admin</div>
          </div>
          <button
            onClick={toggleTheme}
            title="Toggle theme"
            aria-label="Toggle theme"
            className="ml-auto grid h-[30px] w-[30px] place-items-center rounded-lg"
            style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}
          >
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none">
              <path d="M21 12.8A9 9 0 1111.2 3a7 7 0 009.8 9.8z" fill="currentColor" />
            </svg>
          </button>
        </div>
      </aside>
    </>
  );
}
