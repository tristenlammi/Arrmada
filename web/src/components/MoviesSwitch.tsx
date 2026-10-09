import { Link } from "react-router-dom";

// MoviesSwitch is the Library | Wanted switch in the Movies header: the library grid, or
// what Arrmada is still looking for among the films (Missing and Cutoff unmet).
export function MoviesSwitch({ active }: { active: "library" | "wanted" }) {
  const items = [
    { key: "library", label: "Library", to: "/movies" },
    { key: "wanted", label: "Wanted", to: "/movies/wanted" },
  ] as const;
  return (
    <nav aria-label="Movies views" className="inline-flex rounded-lg p-0.5" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
      {items.map((it) => {
        const on = it.key === active;
        return (
          <Link
            key={it.key}
            to={it.to}
            aria-current={on ? "page" : undefined}
            className="rounded-md px-3 py-1.5 text-[11.5px] font-semibold"
            style={{ background: on ? "var(--accent)" : "transparent", color: on ? "var(--accent-ink)" : "var(--ink-faint)" }}
          >
            {it.label}
          </Link>
        );
      })}
    </nav>
  );
}
