/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  // hover: and group-hover: only apply on devices that can really hover. Without
  // this, a tap on a phone leaves hover styles "stuck" on, and a tap could land
  // on a control that had just faded in under the finger.
  future: { hoverOnlyWhenSupported: true },
  theme: {
    extend: {
      // Design tokens are defined as CSS variables in index.css (so light/dark
      // theming lives in one place); these aliases let Tailwind utilities reach
      // them, e.g. `text-accent`, `bg-panel`.
      colors: {
        bg: "var(--bg)",
        sidebar: "var(--sidebar)",
        panel: "var(--panel)",
        "panel-2": "var(--panel-2)",
        line: "var(--line)",
        ink: "var(--ink)",
        "ink-dim": "var(--ink-dim)",
        "ink-faint": "var(--ink-faint)",
        accent: "var(--accent)",
        "accent-deep": "var(--accent-deep)",
      },
      fontFamily: {
        sans: ["-apple-system", "Segoe UI", "Inter", "Roboto", "system-ui", "sans-serif"],
        mono: ["ui-monospace", "SF Mono", "JetBrains Mono", "Consolas", "monospace"],
      },
    },
  },
  plugins: [],
};
