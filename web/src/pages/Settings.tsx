import { useEffect, useState } from "react";
import { Navigate, NavLink, useLocation, useNavigate } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { inputStyle } from "../components/settings/ui";
import { useMe, isAdmin } from "../lib/me";
import { SettingsProvider, useSettingsDraft } from "../lib/useSettings";
import { searchSettings, sectionFromPath, settingsRedirect, visibleSections, type SearchEntry, type SectionId } from "./settings/sections";
import { LibrarySettings } from "./settings/LibrarySettings";
import { NamingSettings } from "./settings/Naming";
import { DownloadsSettings } from "./settings/DownloadsSettings";
import { UsersSettings } from "./settings/UsersSettings";
import { ImportSettings } from "./settings/ImportSettings";
import { SystemSettings } from "./settings/SystemSettings";
import { StatusSection } from "./settings/Status";

// What each section renders; sections.ts says who sees it and where its cards are.
const SECTION_COMPONENTS: Record<SectionId, React.ComponentType> = {
  library: LibrarySettings,
  media: NamingSettings,
  downloads: DownloadsSettings,
  users: UsersSettings,
  import: ImportSettings,
  system: SystemSettings,
  status: StatusSection,
};

// Settings is the hub at /settings/<section>: a rail of sections (a select on a phone), a
// search box over every card, and the section itself. It reads the section from the URL
// itself, so it works mounted at "/settings/*" or as "/settings" plus "/settings/:section".
export function Settings() {
  return (
    <SettingsProvider>
      <SettingsHub />
    </SettingsProvider>
  );
}

export default Settings;

function SettingsHub() {
  const { user } = useMe();
  const sections = visibleSections(isAdmin(user));
  const ids = sections.map((s) => s.id);
  const { pathname, search, hash } = useLocation();
  const navigate = useNavigate();
  const { s, error } = useSettingsDraft();
  const [query, setQuery] = useState("");

  // Bare /settings, an old ?tab=system#api-keys link, or a section this viewer
  // can't open lands on the right section instead.
  const redirect = settingsRedirect(pathname, search, hash, ids);
  const section = sectionFromPath(pathname) as SectionId;

  // A link like /settings/system#api-keys lands on that card. The cards only exist once
  // the settings have loaded, so wait for them rather than scrolling to nothing.
  const ready = !!s;
  useEffect(() => {
    if (ready && hash) document.getElementById(hash.slice(1))?.scrollIntoView({ block: "start" });
  }, [ready, hash, section]);

  if (redirect) return <Navigate to={redirect} replace />;

  const results = searchSettings(query, ids);
  const open = (r: SearchEntry) => {
    setQuery("");
    navigate(`/settings/${r.section}#${r.anchor}`);
    // Same section and same card: the URL doesn't change, so scroll here.
    if (r.section === section && hash === `#${r.anchor}`) document.getElementById(r.anchor)?.scrollIntoView({ block: "start" });
  };

  const Body = SECTION_COMPONENTS[section];

  return (
    <>
      <PageHeader title="Settings" />
      <div className="mx-auto flex w-full max-w-[1100px] flex-col gap-6 px-4 py-6 sm:px-6 lg:flex-row lg:items-start lg:gap-8">
        <aside className="flex w-full flex-col gap-3 lg:sticky lg:top-20 lg:w-[200px] lg:flex-none">
          <div className="relative">
            <input
              type="search"
              aria-label="Search settings"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && results[0]) open(results[0]);
                if (e.key === "Escape") setQuery("");
              }}
              placeholder="Search settings…"
              className="w-full rounded-lg px-3 py-2 text-[12.5px]"
              style={inputStyle}
            />
            {query.trim() && (
              <div className="absolute inset-x-0 top-full z-20 mt-1 flex max-h-[320px] flex-col overflow-y-auto rounded-lg p-1" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}>
                {results.length === 0 ? (
                  <div className="px-2.5 py-2 text-[12px] text-ink-faint">Nothing matches.</div>
                ) : (
                  results.map((r) => (
                    <button key={r.anchor} onClick={() => open(r)} className="flex flex-col rounded-md px-2.5 py-1.5 text-left hover:bg-[var(--panel-2)]">
                      <span className="text-[12.5px] font-semibold">{r.label}</span>
                      <span className="text-[10.5px] text-ink-faint">{sections.find((x) => x.id === r.section)?.label}</span>
                    </button>
                  ))
                )}
              </div>
            )}
          </div>

          {/* Below lg the rail would squeeze the cards, so it becomes a select. */}
          <select
            aria-label="Settings section"
            value={section}
            onChange={(e) => navigate(`/settings/${e.target.value}`)}
            className="rounded-lg px-3 py-2 text-[12.5px] lg:hidden"
            style={inputStyle}
          >
            {sections.map((x) => <option key={x.id} value={x.id}>{x.label}</option>)}
          </select>
          <nav aria-label="Settings sections" className="hidden flex-col gap-0.5 lg:flex">
            {sections.map((x) => (
              <NavLink
                key={x.id}
                to={`/settings/${x.id}`}
                className="rounded-[9px] px-2.5 py-2 text-[13px] transition-colors"
                style={({ isActive }) =>
                  isActive
                    ? { background: "var(--accent-soft)", color: "var(--accent)", fontWeight: 600 }
                    : { color: "var(--ink-dim)" }
                }
              >
                {x.label}
              </NavLink>
            ))}
          </nav>
        </aside>

        <div className="w-full min-w-0 max-w-[820px] flex-1">
          {error && <div className="mb-3 rounded-lg p-3 text-[12.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{error}</div>}
          {!s ? <p className="text-[12.5px] text-ink-dim">Loading…</p> : <Body />}
        </div>
      </div>
    </>
  );
}
