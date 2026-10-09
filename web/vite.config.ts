/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { visualizer } from "rollup-plugin-visualizer";

// The build output lands directly in the Go binary's embed directory so
// `go build` ships one binary with the UI baked in. In dev, /api is proxied
// to the running Go backend (default port 7878).
export default defineConfig({
  plugins: [
    react(),
    // ANALYZE=1 npm run build writes web/bundle-stats.html: which modules sit in which
    // chunk, to check that the requester pages don't drag the console along.
    ...(process.env.ANALYZE ? [visualizer({ filename: "bundle-stats.html", gzipSize: true, brotliSize: true })] : []),
  ],
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    // scripts/check-size.mjs reads the manifest to work out what a requester's first
    // load costs, then deletes it so it isn't embedded in the binary.
    manifest: true,
    rollupOptions: {
      output: {
        // React and the router change far less often than the app, so they get their own
        // chunk whose hash (and browser cache) survives app-only deploys.
        manualChunks: { vendor: ["react", "react-dom", "react-router-dom"] },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:7878",
    },
  },
  // `npm test` (vitest run): unit tests are colocated as src/**/*.test.ts(x).
  test: {
    include: ["src/**/*.test.{ts,tsx}"],
    environment: "node",
  },
});
