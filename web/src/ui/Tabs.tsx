import { useRef, type KeyboardEvent, type ReactNode } from "react";

export interface TabItem<T extends string> {
  key: T;
  label: ReactNode;
  /** A count beside the label (queue lengths and the like); 0 still shows. */
  count?: number;
}

// rovingTarget is where an arrow, Home or End key moves focus in a row of `count` tabs, or
// null for any other key. The arrows wrap around, as the ARIA tabs pattern expects.
export function rovingTarget(key: string, index: number, count: number): number | null {
  if (count <= 0) return null;
  switch (key) {
    case "ArrowRight":
      return (index + 1) % count;
    case "ArrowLeft":
      return (index - 1 + count) % count;
    case "Home":
      return 0;
    case "End":
      return count - 1;
    default:
      return null;
  }
}

// tabIds ties a tab to its panel for screen readers (aria-controls / aria-labelledby).
export function tabIds(idPrefix: string, key: string) {
  return { tab: `${idPrefix}-tab-${key}`, panel: `${idPrefix}-panel-${key}` };
}

// Tabs is the one underline tab bar: 13.5px semibold labels, a 2px accent bar under the
// active one, scrolling sideways on a phone rather than widening the page. It is a real
// ARIA tablist: only the active tab is in the Tab order, the arrow keys (and Home/End)
// move between tabs and select as they go, and a screen reader hears "tab, 2 of 7".
// Pair it with TabPanel around the content. value may be null when no tab is current
// (Discover while it shows search results).
export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
  idPrefix,
  label,
  className = "mb-5 border-b",
}: {
  tabs: readonly TabItem<T>[];
  value: T | null;
  onChange: (key: T) => void;
  /** Unique on the page; prefixes the tab and panel ids. */
  idPrefix: string;
  /** What the tabs switch between, for screen readers ("Insights sections"). */
  label: string;
  /** Spacing and border of the bar; defaults to a bottom rule with space below. */
  className?: string;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const current = tabs.findIndex((t) => t.key === value);
  const focusable = current >= 0 ? current : 0;

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, i: number) => {
    const to = rovingTarget(e.key, i, tabs.length);
    if (to === null) return;
    e.preventDefault();
    refs.current[to]?.focus();
    onChange(tabs[to].key);
  };

  return (
    <div
      role="tablist"
      aria-label={label}
      className={`thin-scroll flex min-w-0 gap-1 overflow-x-auto overflow-y-hidden ${className}`}
      style={{ borderColor: "var(--line)" }}
    >
      {tabs.map((t, i) => {
        const active = i === current;
        const ids = tabIds(idPrefix, t.key);
        return (
          <button
            key={t.key}
            ref={(el) => { refs.current[i] = el; }}
            type="button"
            role="tab"
            id={ids.tab}
            aria-selected={active}
            aria-controls={active ? ids.panel : undefined}
            tabIndex={i === focusable ? 0 : -1}
            onClick={() => onChange(t.key)}
            onKeyDown={(e) => onKeyDown(e, i)}
            className="relative flex-none px-3 py-2.5 text-[13.5px] font-semibold transition-colors sm:px-4"
            style={{ color: active ? "var(--ink)" : "var(--ink-faint)" }}
          >
            {t.label}
            {t.count !== undefined && (
              <span
                className="ml-1.5 rounded px-1.5 py-0.5 font-mono text-[10px]"
                style={{ background: active ? "var(--accent-soft)" : "var(--panel-2)", color: active ? "var(--accent)" : "var(--ink-faint)" }}
              >
                {t.count}
              </span>
            )}
            {active && <span className="absolute inset-x-2 bottom-0 h-[2px] rounded-full" style={{ background: "var(--accent)" }} />}
          </button>
        );
      })}
    </div>
  );
}

// TabPanel wraps the content a tab shows, labelled by its tab.
export function TabPanel({ idPrefix, value, children, className }: { idPrefix: string; value: string; children: ReactNode; className?: string }) {
  const ids = tabIds(idPrefix, value);
  return (
    <div role="tabpanel" id={ids.panel} aria-labelledby={ids.tab} className={className}>
      {children}
    </div>
  );
}
