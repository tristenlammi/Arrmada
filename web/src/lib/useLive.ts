import { useEffect, useState } from "react";

export interface LiveEvent {
  topic: string;
  data: unknown;
}

/**
 * useLive connects to the realtime websocket and exposes connection state plus
 * the most recent event. Reconnects automatically after a drop.
 */
export function useLive() {
  const [connected, setConnected] = useState(false);
  const [last, setLast] = useState<LiveEvent | null>(null);

  useEffect(() => {
    let ws: WebSocket | null = null;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let closed = false;

    const connect = () => {
      const proto = window.location.protocol === "https:" ? "wss" : "ws";
      ws = new WebSocket(`${proto}://${window.location.host}/api/v1/ws`);
      ws.onopen = () => setConnected(true);
      ws.onmessage = (e) => {
        try {
          setLast(JSON.parse(e.data) as LiveEvent);
        } catch {
          /* ignore malformed frames */
        }
      };
      ws.onclose = () => {
        setConnected(false);
        if (!closed) retry = setTimeout(connect, 2000);
      };
    };

    connect();
    return () => {
      closed = true;
      if (retry) clearTimeout(retry);
      ws?.close();
    };
  }, []);

  return { connected, last };
}
