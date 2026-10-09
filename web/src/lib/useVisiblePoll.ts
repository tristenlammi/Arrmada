import { useEffect, useRef } from "react";

// useVisiblePoll runs fn now, then every ms while the tab is visible, and straight away
// when the tab comes back into view. A hidden tab makes no requests at all: pages people
// leave open (the Dashboard, Review) shouldn't poll the server from a background tab.
// fn receives the tick number (0 for the first call) so a page can refresh slower data
// every few ticks.
export function useVisiblePoll(fn: (tick: number) => void, ms: number) {
  const ref = useRef(fn);
  useEffect(() => { ref.current = fn; });
  useEffect(() => {
    let tick = 0;
    const run = () => ref.current(tick++);
    run();
    const t = setInterval(() => { if (!document.hidden) run(); }, ms);
    const onVis = () => { if (!document.hidden) run(); };
    document.addEventListener("visibilitychange", onVis);
    return () => {
      clearInterval(t);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [ms]);
}
