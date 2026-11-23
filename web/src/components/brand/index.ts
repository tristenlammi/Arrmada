// Media server branding: marks, the server badge and tile, and the Watch links. Import
// from here (or the files themselves); nothing in it fetches.
export { PlexMark, JellyfinMark, EmbyMark, ServerMark } from "./marks";
export { ServerBadge, ServerTile } from "./ServerBadge";
export { WatchButton, WatchPills } from "./WatchPills";
export {
  MEDIA_SERVERS, SERVER_LABEL, SERVER_FILL, SERVER_INK,
  isMediaServer, watchLabel, setJellyfinSlotKind, jellyfinSlotIsEmby, safeWatchURL, watchLinksOf,
  type MediaServerKind, type WatchLink,
} from "./servers";
