import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { MeProvider } from "./lib/me";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { reloadOnce } from "./lib/chunkReload";
import { ConfirmProvider, ToastProvider } from "./ui";
import "./index.css";

// Vite fires this when a lazily imported chunk (or its preload) fails to load,
// typically a tab opened before a deploy asking for a chunk that's gone. Reload
// once to pick up the new build; the guard stops a loop if that doesn't help.
window.addEventListener("vite:preloadError", (e) => {
  if (reloadOnce()) e.preventDefault();
});

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ErrorBoundary>
      {/* The kit's providers sit above the router (App owns the RouterProvider),
          so a toast or confirm body can't use router hooks. */}
      <ToastProvider>
        <ConfirmProvider>
          <MeProvider>
            <App />
          </MeProvider>
        </ConfirmProvider>
      </ToastProvider>
    </ErrorBoundary>
  </React.StrictMode>,
);

// Register the service worker for PWA install + offline shell.
if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => { /* non-fatal */ });
  });
}
