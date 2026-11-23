# Jellyfin integration plan

Status: planned (2026-10-14). Checked against the code at `5c5d2b10` and Jellyfin's stable
OpenAPI spec (`https://api.jellyfin.org/openapi/jellyfin-openapi-stable.json`, 12.2.0).
Items marked **[verify live]** are confirmed against the pinned Docker image by lane J0.

Goal: Jellyfin works the way Plex does today, both can be connected at once, and the UI
treats them as equals.

## 1. Plex touchpoints today

| # | Feature | Where | Plex API | Stores |
|---|---|---|---|---|
| 1 | Connection (URL+token, plex.tv PIN, discovery, identity) | `internal/plex/*`, `insights/{service,connect,plexclient}.go`, `httpapi/insights.go`, `httpapi/connections.go`, `PlexConnection.tsx`, `Connections.tsx` | `/identity`, plex.tv pins/resources/user | `insights_plex_*` settings; `ARRMADA_PLEX_URL/TOKEN` |
| 2 | Partial library scans after import/rename/delete/convert/subtitles | `internal/plexscan/*`, `cmd/arrmada/main.go`, `outbox.go registerPlex`, `subtitles/events.go`, `httpapi/plexscan.go`, `PlexLibraryUpdates.tsx` | `/library/sections`, `…/refresh?path=` | `plex_scan_on_import`, `plex_path_map` |
| 3 | Library index, Watch-on-Plex links, Recently added | `insights/plexlinks.go`, `httpapi/plexlinks.go`, `discover.go`, `discover_recent.go`, `PlexBits.tsx` | `/library/sections/{k}/all?includeGuids=1`, `/library/recentlyAdded` | memory |
| 4 | Request "ready" = in Plex | `requests/plexready.go`, `cmd/arrmada/plexready.go`, `lib/requestStage.ts` | the index | `requests.on_disk_at`, `season_disk_at` |
| 5 | Watch link on notices/push | `httpapi/usernotify.go`, `push.Message.PlexURL`, `web/public/sw.js` | — | `user_notifications.plex_url` |
| 6 | Insights (live, recorder, history, stats, reliability, bandwidth, library, people) | `internal/insights/*`, `httpapi/insights.go`, `pages/Insights*`, `pages/insights/*` | `/status/sessions`, image paths | `plex_users`, `stream_sessions`, `buffer_events`, `bandwidth_samples`, `insights_live_sessions`; `insights_*` settings |
| 7 | Tautulli import | `insights/import*.go`, `internal/tautulli`, `TautulliImport.tsx` | Tautulli API | `insights_import_runs` |
| 8 | Sign-in, family sign-in, link, merge, blocks | `httpapi/auth_plex.go`, `plexpin.go`, `plexlink.go`, `plexmerge.go`, `plex_blocks.go`, `auth/{service,plexlink,merge}.go`, `Login.tsx`, `lib/plexSignIn.ts` | plex.tv PIN/user/resources | `users.plex_id`, `plex_username`, `password_login`; `plex_login_*` settings |
| 9 | Convert pauses while someone watches | `convert/service.go SetWatching`, `runner.go`, `ConvertSettings.tsx` | live count from the poller | `convert_pause_watching` |
| 10 | Your devices / HW transcode badges | `insights/devices.go`, `DevicesPanel.tsx`, `lib/plexHw.ts` | session transcode fields | `stream_sessions` columns |
| 11 | Health and setup | `health/checks_integrations.go PlexCheck`, `setup.go`, `setup_checklist.go`, `SetupWizard.tsx` | identity | — |
| 12 | Alerts | `notify/catalog.go` (`plex.stream.started`, `plex.buffering`), `notify/filters.go`, `notify/attach.go` | image proxy | subscription keys |
| 13 | Dashboard Now playing | `httpapi/dashboard.go`, `Dashboard.tsx` | Activity | — |
| 14 | Discover personal rows from history | `discover_rows.go`, `discover_recommended.go` | recorded plays | — |
| 15 | Watched by | `insights/watchstats.go`, `httpapi/watchstats.go` | index + plays | — |
| 16 | Hidden files | `library/recyclebins.go`, `convert/hold.go`, `recyclebin/service.go` (`.plexignore`) | — | marker files |
| 17 | Naming | `library/importer.go` (`{plexedition}`, `{tmdb-N}`), `Naming.tsx` preset | — | — |
| 18 | Other | Overseerr import, support redaction, copy guard, nav group, `LINKS.plex*`, Settings section `plex` | — | — |

Insights already records its own history by polling, so Jellyfin fits the same recorder.

## 2. Jellyfin equivalents

Auth: `Authorization: MediaBrowser Token="<key>", Client="Arrmada", Device="Arrmada", DeviceId="<id>", Version="<build>"` (don't rely on `X-Emby-Token`/`api_key` **[verify live]**).

| Touchpoint | Jellyfin | Notes / fallback |
|---|---|---|
| Connect | `GET /System/Info`, `/System/Info/Public` | URL + API key; no discovery |
| Partial scan | `POST /Library/Media/Updated` (`Updates:[{Path,UpdateType}]`); libraries from `GET /Library/VirtualFolders`; full `POST /Library/Refresh` | reuse `plexscan.Resolve` + Scanner via an adapter; wait ~120 s before re-indexing **[verify live]** |
| Index / Locate | `GET /Items?recursive=true&includeItemTypes=Movie,Series&fields=ProviderIds,DateCreated,Path` paged | in-memory index by provider ids (no `anyProviderIdEquals` in spec); API key without userId **[verify live]** |
| Watch link | `{public_url}/web/#/details?id={itemId}&serverId={SystemInfo.Id}` **[verify live]** | needs a public address setting |
| Recently added | `GET /Items?…sortBy=DateCreated&sortOrder=Descending&limit=60&fields=ProviderIds,SeriesId` | episodes map via SeriesId |
| Live sessions | `GET /Sessions?activeWithinSeconds=…` (NowPlayingItem, PlayState, TranscodingInfo) | poll like Plex |
| History | none built in | Arrmada's own recorder; history starts when monitoring is on |
| Buffering | not reported | record 0, leave Jellyfin out of reliability rates, say so in the UI |
| Bandwidth / LAN-WAN | TranscodingInfo.Bitrate; MediaSource Bitrate for direct play **[verify live]**; RemoteEndPoint | |
| HW badge | `HardwareAccelerationType` (configured type) | show "HW (vaapi)", never "fell back" |
| Images | `GET /Items/{id}/Images/Primary` | proxy through `/insights/image`; `jellyfin:` prefix |
| Users | `GET /Users` | |
| Sign-in | `POST /Users/AuthenticateByName`; Quick Connect (`/QuickConnect/Initiate`, `/Connect?secret=`, `/Users/AuthenticateWithQuickConnect`); `POST /Sessions/Logout` | sign-in proves server access; `IsAdministrator` ≈ Plex owner; never log/store passwords; throttle per username+IP |
| Hidden folders | dot folders skipped; `.ignore` **[verify live]** | add `.ignore` markers next to `.plexignore` |
| Naming | `[tmdbid-N]`; `{tmdb-N}`/`{edition-…}` grouping **[verify live]** | add a "Jellyfin naming" preset if needed |

## 3. Architecture

- New `internal/mediaserver` (types only): `Kind`, `ExternalIDs`, `Item`, neutral `Session` (aliased from `plex.Session`), `Library`, `Server` interface (`Kind, Name, Configured, IndexReady, IndexBuiltAt, IndexStale, OnIndexRebuilt, Locate, WatchURL, RecentlyAddedTMDB`), `Registry` (Plex then Jellyfin; `LocateAny`, `WatchLinks`, oldest `IndexBuiltAt`).
- `internal/jellyfin` client (mirrors `internal/plex`, fixtures captured from a real container); `internal/jellyfinsvc` for settings, index, links, identity, poll health.
- Scans: `plexscan.Options.Keys{Enabled, PathMap}` and a second Scanner with a Jellyfin adapter; producers get a fan-out. Outbox consumer name unchanged.
- Insights: per-server tracker + `sessionSource`; Jellyfin session key `jellyfin:<Id>`; one loop per source; `Watching()` = any server playing.
- Migrations: **0310** `stream_sessions.server`, `plex_users.server`, `insights_live_sessions.server` (default `plex`); **0311** `users.jellyfin_id`, `jellyfin_username`; **0312** `user_notifications.jellyfin_url`; **0313** spare.
- Settings: `jellyfin_url`, `jellyfin_public_url`, `jellyfin_server_id`, `jellyfin_server_name`, `jellyfin_device_id`, `jellyfin_monitoring_enabled`, `jellyfin_scan_on_import`, `jellyfin_path_map`, `jellyfin_login_enabled`, `jellyfin_quickconnect_enabled`, `jellyfin_signin_staff`, `jellyfin_login_auto_approve_types`, `jellyfin_login_blocked`. API key = hidden `apikeys` entry `jellyfin` (env `ARRMADA_JELLYFIN_API_KEY`); `jellyfin_` added to support-bundle redaction.
- Ready: a `mediaReadyLocator` over the Registry keeps `requests.PlexLocator`'s shape — ready when any server has it.
- Watch links: `watch_links:[{server,label,url}]`, `plex_url` kept; order follows the requester's linked server; push gets `watch_url`/`watch_label`; `sw.js` accepts any http(s) watch URL.
- Alerts: keep topic keys, add `server` to the payload, relabel group "Plex" → "Watching".
- Accounts: `FindOrCreateJellyfinUser`, `LinkJellyfin`/`UnlinkJellyfin`, `UserByJellyfinID`; `MergePlexDuplicate` → `MergeDuplicate` moving both links; unlinking the last external sign-in refused when `password_login=0`.

## 4. UI

- Brand tokens `--plex`/`--plex-ink`, `--jellyfin`/`--jellyfin-ink` (dark ink on the Jellyfin gradient), contrast-tested; small Plex/Jellyfin marks.
- Connections: a "Media servers" section with a card per server (status, "used for" chips, Test/Edit), quiet "Connect Jellyfin" card when absent.
- Settings: `plex` section → "Media servers" (`/settings/media-servers`) with old links redirected.
- Setup wizard: "Plex" step → "Media server" (Plex, Jellyfin or both).
- Login: "Sign in with Plex" and "Sign in with Jellyfin" (sheet with username/password and Quick Connect), both lazy.
- Watch buttons per server; request stage "Adding to Plex/Jellyfin/your library…".
- Insights: server chips (All · Plex · Jellyfin), a server badge on streams and history, `?server=` filter, Jellyfin note on Reliability; nav group "Plex" → "Watching".
- Me/People: linked Plex and Jellyfin accounts with Link/Unlink.

## 5. Delivery

| Wave | Lane | Owns | Migration |
|---|---|---|---|
| 1 | J0 Jellyfin client + real-container test rig | `internal/jellyfin/**`, `scripts/jellyfin-it.sh` | — |
| 1 | J5a UI shell (mocked) | brand tokens, brand marks, `JellyfinConnection.tsx`, Connections layout, e2e fixtures | — |
| 2 | J1 Core: connection, index, scans | `internal/mediaserver/**`, `internal/jellyfinsvc/**`, `plexscan`, `cmd/arrmada/mediaservers.go`, `httpapi/jellyfin.go`, health, apikeys, `.ignore` | 0313 |
| 3 | J2 Insights multi-server | `internal/insights/**`, `httpapi/insights.go`, dashboard, Insights pages, notify catalog/attach | 0310 |
| 3 | J3 Ready + watch links | `cmd/arrmada/plexready.go`, `httpapi/{plexlinks,usernotify,discover}.go`, push, `sw.js`, request UI | 0312 |
| 3 | J4 Sign-in + accounts | `auth/*`, `httpapi/auth_jellyfin.go`, Login, account/People UI | 0311 |
| 4 | J5b Settings, wizard, nav, copy | `sections.ts`, `Settings.tsx`, `SetupWizard.tsx`, `setup*.go`, `nav.ts`, `links.ts`, copy guard | — |

API contract fixed in J1's first commit: `GET /api/v1/jellyfin` → `{url, public_url, key_set, server_name, server_id, version, monitoring_enabled, poll_seconds}`; Connections items gain `card:"jellyfin"`.

Real-server rig: synthetic ffmpeg clips named for real TMDB/TVDB titles with NFO uniqueids (never the owner's library), a pinned `jellyfin/jellyfin` container on 127.0.0.1, first-run setup and API key scripted over the API, IT tests behind `ARRMADA_JELLYFIN_IT=1`, cleaned up on exit.

## 6. Owner decisions (2026-10-14)

1. **Ready rule:** a request is ready as soon as **either** connected server has it; the requester sees a Watch button per server that has it, their own server's first.
2. **Jellyfin sign-in:** username + password **and** Quick Connect. The password passes through Arrmada once — never stored or logged; throttled per username and IP.
3. **Settings:** one **"Media servers"** page (`/settings/media-servers`) with Plex and Jellyfin sections; old `/settings/plex` links redirect.
4. **History:** Jellyfin history starts when monitoring is switched on; importing Playback Reporting / Jellystat history is later.
5. Defaults taken (not asked): a separate **public address** for Jellyfin watch links, pre-filled from the connection URL, warning when it looks LAN-only; Insights shows **one row per server identity** with a server badge, grouped on the profile when both are linked to the same Arrmada account.

## 7. Emby (#60, 2026-11)

Emby connects through the Jellyfin slot: `jellyfin_server_kind` = `emby` (unset or anything
else = Jellyfin, so existing installs are untouched). `jellyfin.NewFor(kind, …)` switches the
client's dialect; everything above it (jellyfinsvc, index, scanner, Insights source, sign-in,
`mediaserver.Jellyfin`) is shared. `mediaserver.Kind.Label()` and the web's `SERVER_LABEL`
name the slot "Emby" when it holds one. No real Emby server was available: the Emby side is
checked against Emby's REST API reference (dev.emby.media/reference/RestAPI, generated from
current 4.8/4.9 servers), its API docs pages and the old open-source server code
(MediaBrowser/Emby), with fakes in `internal/jellyfin/testdata/emby/` built from those shapes.

| What Arrmada does | Jellyfin | Emby | Source |
|---|---|---|---|
| API root | `/` | `/emby/…` (an address already ending in `/emby` isn't doubled) | docs (base URL `…/emby/{apipath}`) |
| Credentials | `Authorization: MediaBrowser Token=…, Client=…` | `X-Emby-Authorization: Emby Client=…, Device=…, DeviceId=…, Version=…[, Token=…]` + `X-Emby-Token` | docs + old AuthorizationContext.cs |
| Identity | `/System/Info`, `/System/Info/Public` (`ProductName` "Jellyfin Server") | same routes, no `ProductName` | reference |
| Libraries | `/Library/VirtualFolders` (list) | `/Library/VirtualFolders/Query` (`{Items}`), falling back to the list on 404 | reference (Query); old code (list) |
| Partial scan | `POST /Library/Media/Updated {Updates:[{Path,UpdateType}]}` | same | reference |
| Refresh one item | `POST /Items/{id}/Refresh` + `regenerateTrickplay` | same, without `regenerateTrickplay` | reference |
| Index | `/Items?recursive&includeItemTypes&fields=ProviderIds,…` with the API key, no user | same | reference; **assumed** that a key without `UserId` lists everything (forum reports say it does on most versions) |
| Item ids | 32-hex GUIDs | numbers (`"1201"`); users are GUIDs | **assumed** (how Emby 4.x answers in practice; the reference types ids as plain strings). GUID ids still work either way |
| Watch link | `/web/#/details?id=…&serverId=…` | `/web/index.html#!/item?id=…&serverId=<lower-case>` | **assumed** (community forum, not the docs) |
| Sessions | `/Sessions`; `TranscodingInfo.HardwareAccelerationType` (configured) | same shape; hardware as `VideoEncoderIsHardware`/`VideoEncoderHwAccel` (+ decoder), folded into `HardwareAccelerationType` | reference |
| Users | `GET /Users` (list) | `GET /Users/Query` (`{Items}`), falling back to `/Users` on 404 | reference (Query); old code (list) |
| User picture | `/UserImage?userId=` | `/Users/{id}/Images/Primary` | docs (Images page) |
| Item picture | `/Items/{id}/Images/Primary` | same | docs |
| Sign-in | `POST /Users/AuthenticateByName {Username,Pw}`, `POST /Sessions/Logout` | same | reference + docs |
| Quick Connect | `/QuickConnect/*` | none (Emby Connect is something else) → `ErrUnsupported`, never offered | reference (no such routes) |
| Premiere | — | not needed by anything Arrmada calls; hardware transcoding itself needs it | emby.media/premiere |

Insights records an Emby play under rating key `emby-<n>` (a bare number could match a Plex
rating key) and its posters as `emby:/Items/<n>/Images/Primary`, a second strict image form
next to `jellyfin:/Items/<32 hex>/…`. Where the API key menu lives in Emby
(Dashboard → Advanced → API Keys) is **assumed** from current third-party guides; Emby's own
API-key page still shows an older menu.
