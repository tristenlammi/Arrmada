// Pages are split into their own chunks, so the first visit to each one waits for a
// download. Staff open Movies, Series and Downloads far more than anything else, so once
// the console is up those three are fetched while the browser is idle and open instantly.
// The same import() calls as the router's, so the browser caches one copy.
export function prefetchStaffPages(): () => void {
  const run = () => {
    for (const load of [() => import("../pages/Movies"), () => import("../pages/Series"), () => import("../pages/Downloads")]) {
      load().catch(() => { /* only a head start: the real visit retries and reports */ });
    }
  };
  if (typeof window.requestIdleCallback === "function") {
    const id = window.requestIdleCallback(run, { timeout: 5000 });
    return () => window.cancelIdleCallback(id);
  }
  const id = window.setTimeout(run, 2000);
  return () => window.clearTimeout(id);
}
