import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build output lands directly in the Go binary's embed directory so
// `go build` ships one binary with the UI baked in. In dev, /api is proxied
// to the running Go backend (default port 7878).
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:7878",
    },
  },
});
