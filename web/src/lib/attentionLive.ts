import { SIGNED_OUT_EVENT } from "./api";
import { loadAttention } from "./useAttention";

// The staff shell's live line for the Needs-you badges. The server publishes
// attention.changed (counts only, staff-only) about a second after a request is made or
// decided, a review lands or health moves; asking for the snapshot then puts a family
// member's new request on the sidebar within seconds instead of at the next 30-second
// poll. Its own chunk, started by the staff layout: requesters never download it.

let started = false;

export function startAttentionLive() {
  if (started || typeof WebSocket === "undefined") return;
  started = true;
  let ws: WebSocket | null = null;
  let stopped = false;
  let retry: ReturnType<typeof setTimeout> | undefined;
  const connect = () => {
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    ws = new WebSocket(`${proto}://${window.location.host}/api/v1/ws`);
    ws.onmessage = (e) => {
      try {
        // A hidden tab catches up when it's shown again (the poll runs on return).
        if ((JSON.parse(e.data) as { topic?: string }).topic === "attention.changed" && !document.hidden) void loadAttention();
      } catch { /* not a frame we read */ }
    };
    ws.onclose = () => { if (!stopped) retry = setTimeout(connect, 5000); };
  };
  // Signed out: stop for good (the next staff sign-in starts a fresh line).
  window.addEventListener(SIGNED_OUT_EVENT, () => {
    stopped = true;
    started = false;
    clearTimeout(retry);
    ws?.close();
  }, { once: true });
  connect();
}
