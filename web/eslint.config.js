// ESLint 9 flat config for the web UI.
//
// Bar for now: `npm run lint` must pass on today's code without a mass rewrite.
// Errors are reserved for real bugs (rules-of-hooks); rules that would flag a
// large backlog of existing code start as warnings and are tightened by later
// roadmap tasks (FE-14 confirm/alert, FE-15 a11y, FE-09 overlays).
import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import jsxA11y from "eslint-plugin-jsx-a11y";
import globals from "globals";
import { defineConfig } from "eslint/config";

export default defineConfig(
  { ignores: ["dist/**", "node_modules/**"] },
  js.configs.recommended,
  tseslint.configs.recommended,
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: {
      ecmaVersion: 2020,
      sourceType: "module",
      globals: globals.browser,
    },
    plugins: {
      "react-hooks": reactHooks,
      "jsx-a11y": jsxA11y,
    },
    rules: {
      // Hooks called conditionally or out of order are real bugs: error.
      "react-hooks/rules-of-hooks": "error",
      // Warn only: many existing omissions are deliberate (run-once / run-on-change effects).
      "react-hooks/exhaustive-deps": "warn",

      // jsx-a11y recommended at warn: too many existing hits to fail on yet (FE-15 flips key rules).
      ...Object.fromEntries(
        Object.entries(jsxA11y.flatConfigs.recommended.rules)
          .filter(([, v]) => (Array.isArray(v) ? v[0] : v) !== "off")
          .map(([r, v]) => [r, Array.isArray(v) ? ["warn", ...v.slice(1)] : "warn"]),
      ),

      // Native dialogs are being replaced by the UI kit's Confirm/Toast (FE-14 flips to error).
      "no-restricted-globals": [
        "warn",
        { name: "confirm", message: "Use the UI kit's Confirm instead of a native dialog." },
        { name: "alert", message: "Use a toast instead of a native alert." },
      ],
      "no-restricted-properties": [
        "warn",
        { object: "window", property: "confirm", message: "Use the UI kit's Confirm instead of a native dialog." },
        { object: "window", property: "alert", message: "Use a toast instead of a native alert." },
      ],

      // `set.has(k) ? set.delete(k) : set.add(k)` toggles are an established idiom here.
      "@typescript-eslint/no-unused-expressions": ["error", { allowTernary: true }],
    },
  },
  {
    // Hand-built overlays: the UI kit's Modal/Sheet (src/ui, FE-09) should own these.
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["src/ui/**"],
    rules: {
      "no-restricted-syntax": [
        "warn",
        {
          selector: "Literal[value=/fixed inset-0/]",
          message: "Hand-built overlay: use the UI kit's Modal/Sheet (src/ui).",
        },
        {
          selector: "TemplateElement[value.raw=/fixed inset-0/]",
          message: "Hand-built overlay: use the UI kit's Modal/Sheet (src/ui).",
        },
      ],
    },
  },
);
