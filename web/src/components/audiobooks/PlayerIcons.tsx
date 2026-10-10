// Transport icons for the audiobook player (mini and full). Stroke icons in the shell's
// style; fills where a solid shape reads better at small sizes (play, pause).

export type PlayerIconName = "play" | "pause" | "back30" | "fwd30" | "prev" | "next" | "close" | "sleep" | "bookmark" | "chevron";

export function PlayerIcon({ name, size = 22 }: { name: PlayerIconName; size?: number }) {
  const common = { width: size, height: size, viewBox: "0 0 24 24", "aria-hidden": true } as const;
  switch (name) {
    case "play":
      return <svg {...common} fill="currentColor"><path d="M8 5.5v13a1 1 0 0 0 1.52.85l10.4-6.5a1 1 0 0 0 0-1.7L9.52 4.65A1 1 0 0 0 8 5.5Z" /></svg>;
    case "pause":
      return <svg {...common} fill="currentColor"><rect x="6" y="5" width="4.2" height="14" rx="1.2" /><rect x="13.8" y="5" width="4.2" height="14" rx="1.2" /></svg>;
    case "back30":
    case "fwd30": {
      const back = name === "back30";
      return (
        <svg {...common} fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
          <g transform={back ? undefined : "translate(24 0) scale(-1 1)"}>
            <path d="M4.5 12a7.5 7.5 0 1 0 2.2-5.3" />
            <path d="M4 3.8v3.6h3.6" />
          </g>
          <text x="12" y="15.2" textAnchor="middle" fontSize="7.2" fontWeight="700" fill="currentColor" stroke="none">30</text>
        </svg>
      );
    }
    case "prev":
      return <svg {...common} fill="currentColor"><path d="M6 5h2v14H6zM19 6.2v11.6a.9.9 0 0 1-1.38.76L9.9 13.4a1.6 1.6 0 0 1 0-2.8l7.72-5.16A.9.9 0 0 1 19 6.2Z" /></svg>;
    case "next":
      return <svg {...common} fill="currentColor"><path d="M16 5h2v14h-2zM5 6.2v11.6a.9.9 0 0 0 1.38.76l7.72-5.16a1.6 1.6 0 0 0 0-2.8L6.38 5.44A.9.9 0 0 0 5 6.2Z" /></svg>;
    case "close":
      return <svg {...common} fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><path d="M6 6l12 12M18 6 6 18" /></svg>;
    case "sleep":
      return <svg {...common} fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round"><path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5Z" /></svg>;
    case "bookmark":
      return <svg {...common} fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round"><path d="M7 4h10v16l-5-3.5L7 20z" /></svg>;
    case "chevron":
      return <svg {...common} fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="m6 9 6 6 6-6" /></svg>;
  }
}
