import { useEffect, useState } from "react";

// The same media query Tailwind's hoverOnlyWhenSupported gates hover: on, so JS
// and CSS agree on which devices get hover-revealed controls.
const QUERY = "(hover: hover) and (pointer: fine)";

function matches(): boolean {
  try { return typeof window !== "undefined" && window.matchMedia(QUERY).matches; }
  catch { return false; }
}

// useCanHover reports whether the main pointer can hover (a mouse or trackpad).
// Touch screens can't, and an opacity-0 hover layer there is still tappable —
// a stray tap on a poster corner used to file a request nobody could see. Hover-only
// controls render only when this is true; touch gets visible buttons instead.
// The first value is read synchronously so desktop doesn't flash the touch layout,
// and it follows changes (a tablet docking a mouse, devtools device emulation).
export function useCanHover(): boolean {
  const [canHover, setCanHover] = useState(matches);
  useEffect(() => {
    let mq: MediaQueryList;
    try { mq = window.matchMedia(QUERY); } catch { return; }
    const onChange = () => setCanHover(mq.matches);
    onChange();
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);
  return canHover;
}
