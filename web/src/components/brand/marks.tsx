import { jellyfinSlotIsEmby, type MediaServerKind } from "./servers";

// Small marks for the media servers: simple geometric versions, not the official logos.
// They draw in currentColor, so they take the ink of whatever they sit on, and they are
// decoration only (aria-hidden): the server's name is always written next to them.

interface MarkProps {
  /** Pixels; the marks are square. */
  size?: number;
  className?: string;
}

// Plex: the chevron.
export function PlexMark({ size = 16, className }: MarkProps) {
  return (
    <svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true" focusable="false" className={className}>
      <path fill="currentColor" stroke="currentColor" strokeWidth={1.2} strokeLinejoin="round" d="M5.5 3h6.6l6.4 9-6.4 9H5.5l6.4-9z" />
    </svg>
  );
}

// Jellyfin: a triangle with a smaller one inside it.
export function JellyfinMark({ size = 16, className }: MarkProps) {
  return (
    <svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true" focusable="false" className={className}>
      <path fill="none" stroke="currentColor" strokeWidth={2.6} strokeLinejoin="round" d="M12 4 20.6 19.2H3.4z" />
      <path fill="currentColor" stroke="currentColor" strokeWidth={1.4} strokeLinejoin="round" d="M12 10.2l3 5.3H9z" />
    </svg>
  );
}

// Emby: a play triangle in a hexagon.
export function EmbyMark({ size = 16, className }: MarkProps) {
  return (
    <svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true" focusable="false" className={className}>
      <path fill="none" stroke="currentColor" strokeWidth={2.4} strokeLinejoin="round" d="M12 2.8 20 7.4v9.2l-8 4.6-8-4.6V7.4z" />
      <path fill="currentColor" stroke="currentColor" strokeWidth={1.2} strokeLinejoin="round" d="M10 8.6v6.8l5.6-3.4z" />
    </svg>
  );
}

// The Jellyfin slot draws Emby's mark when it's connected to Emby.
export function ServerMark({ server, ...rest }: MarkProps & { server: MediaServerKind }) {
  if (server === "plex") return <PlexMark {...rest} />;
  return jellyfinSlotIsEmby() ? <EmbyMark {...rest} /> : <JellyfinMark {...rest} />;
}
