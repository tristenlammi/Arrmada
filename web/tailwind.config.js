/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  // hover: and group-hover: only apply on devices that can really hover. Without
  // this, a tap on a phone leaves hover styles "stuck" on, and a tap could land
  // on a control that had just faded in under the finger.
  future: { hoverOnlyWhenSupported: true },
  theme: {
    // The type scale (FE-30), replacing Tailwind's defaults: every size in the UI is one of
    // these. Text that carries information is at least xs (11px); 2xs is only for the
    // uppercase mono eyebrow labels, whose capitals and tracking carry them. Sizes only, no
    // line-heights, so each element keeps the leading it already had.
    // scripts/type-scale.mjs moved the old text-[Npx] sizes here; eslint keeps them out.
    fontSize: {
      "2xs": "10.5px",
      xs: "11px",
      sm: "12px",
      base: "12.5px",
      md: "13.5px",
      lg: "15px",
      xl: "17px",
      "2xl": "20px",
      "3xl": "24px",
      display: "30px",
      "display-lg": "38px",
    },
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
        "line-soft": "var(--line-soft)",
        "accent-ink": "var(--accent-ink)",
        "accent-soft": "var(--accent-soft)",
        "accent-line": "var(--accent-line)",
        good: "var(--good)",
        "good-soft": "var(--good-soft)",
        avoid: "var(--avoid)",
        "avoid-soft": "var(--avoid-soft)",
        reject: "var(--reject)",
        "reject-soft": "var(--reject-soft)",
        under: "var(--under)",
        "under-soft": "var(--under-soft)",
        mismatch: "var(--mismatch)",
        "mismatch-soft": "var(--mismatch-soft)",
        // Status hues tuned for text (at least 4.5:1): use these for words and
        // the plain ones above for fills, borders and bars.
        "accent-text": "var(--accent-text)",
        "good-text": "var(--good-text)",
        "avoid-text": "var(--avoid-text)",
        "reject-text": "var(--reject-text)",
        // Media server brands: a fill and the ink on it (components/brand).
        plex: "var(--plex)",
        "plex-ink": "var(--plex-ink)",
        "jellyfin-ink": "var(--jellyfin-ink)",
        // Emby, when it's connected through the Jellyfin slot.
        emby: "var(--emby)",
        "emby-ink": "var(--emby-ink)",
      },
      boxShadow: {
        panel: "var(--shadow)",
      },
      // One stacking order for everything that floats, lowest to highest: the
      // sticky page header, the requester bottom bar, the audiobook mini-player
      // just above it, the sidebar drawer (and dropdown menus), modals and sheets
      // (src/ui/Modal), then toasts, which must stay readable over an open modal.
      zIndex: {
        header: "30",
        tabbar: "40",
        player: "45",
        drawer: "50",
        modal: "60",
        toast: "70",
      },
      // The two page widths and the form column (ui/Page): every page uses one of them,
      // so the content's left edge doesn't jump from page to page.
      maxWidth: {
        page: "1240px",
        "page-wide": "1600px",
        form: "760px",
      },
      backgroundImage: {
        "accent-grad": "linear-gradient(150deg, var(--accent), var(--accent-deep))",
        // Jellyfin's purple-to-blue: bg-jellyfin.
        jellyfin: "var(--jellyfin)",
      },
      fontFamily: {
        sans: ["-apple-system", "Segoe UI", "Inter", "Roboto", "system-ui", "sans-serif"],
        mono: ["ui-monospace", "SF Mono", "JetBrains Mono", "Consolas", "monospace"],
      },
    },
  },
  plugins: [],
};
