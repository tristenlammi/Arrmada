# AUD — Audiobook server & listening

_Part of the [Arrmada roadmap](../../ROADMAP.md). 18 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make the audiobook server something the family can trust and actually use. A place is never silently lost, reset or dragged back, whatever an app sends. Every rejected, held or discarded place can be seen and put back. Arrmada has its own listening API, so the built-in web player (UI in APP) runs under the same guards. Replies are checked against recorded real Audiobookshelf replies, and at least one iPhone app is verified. Apps see fresh covers and titles, narrators and the ebook. The audiobook tree stops being walked every 30 minutes.

**Why.** The engine under the audiobook server is the best part of this area: durable sessions, held rewinds, newer-only offline uploads, and a listen_log with no book column. The 2026-10-08 audit found that the code around the engine breaks those promises in ways a listener notices.

- **Places get reset or dragged back.** A session closed with no body counts as a jump to 0:00. Early in a book the place silently resets. Later in a book the person gets a scary "An app jumped back to 0:00 … Use this spot" banner, and tapping it really does reset them (audiobooks-3). A PATCH of progress from an app counts as "Manual", so it skips every guard and stamps now, which makes later genuine offline uploads look older (audiobooks-2). One bad forward report can mark a book finished and drop it off Continue Listening (audiobooks-10).
- **"Listen Again" doesn't work.** A finished book reopens at its last seconds and stops (audiobooks-4).
- **"You can always put your place back" isn't true.** Uploads judged older or unproven leave no trace. A discard from an app hard-deletes the row, despite the code comment, and the UI has no way to restore it (audiobooks-5). Plane scenario: you listen offline from 1h to 4h, briefly open the book on a tablet, then the phone's upload is silently rejected.
- **No iPhone path, and nowhere to listen in Arrmada.** /audiobooks is a settings page that names only Lissen, which is Android-only. No iOS app has been verified (audiobooks-7).
- **Client compatibility is guesswork.** Tests check that keys exist, not types or shapes. Ids aren't UUID-shaped. Each new client costs one commit per guess (audiobooks-6).
- **Smaller issues.** The warm-up walks and stats the whole audiobook tree every 30 minutes and on every admin page load, which can wake Unraid array disks (audiobooks-8). Apps keep old covers because updatedAt is always addedAt, and narrators are never shown (audiobooks-11). The official app can't open the EPUB of a book the family also listens to (books-7).

**Depends on:** SEC: audiobook request-log redaction (draft audiobooks.t1, finding audiobooks-1). [AUD-11](#aud-11) (trace switch), [AUD-12](#aud-12) (iOS debugging) and [AUD-18](#aud-18) (new ebook routes) need logRequest to already log route patterns and query keys only, so no new or traced line reveals which book someone played.; BOOK: shared ebook-file helper (draft books.t12, moving httpapi/mybooks.go ebookFile and ebookContentType into internal/books). [AUD-18](#aud-18) serves the EPUB on the ABS server through it.; APP (provides, not needs): the requester app shell's Listen tab, mini-player, Media Session and PWA, plus the Apps & devices checklist, are built on [AUD-07](#aud-07) and [AUD-08](#aud-08)'s /api/v1/me/audio API and use [AUD-12](#aud-12)'s verified-app list. [AUD-05](#aud-05)'s PlaceTimeline and OfferBanner components are written to be reused there.; All epics adding migrations (BE, BOOK, REQ and others): [AUD-04](#aud-04), [AUD-14](#aud-14), [AUD-16](#aud-16) and [AUD-17](#aud-17) add migrations after 0089. Take the next free number at implementation time, not a pre-reserved one.

#### Design

## Target shape

Keep `internal/listening` (the pure `Decide` rules engine plus the durable store) and the auth model. Tighten the rules, make the history a real per-book timeline, and add a second front door: Arrmada's own `/api/v1/me/audio/*` API, used by the web player. Every input path goes through `listening.Store`: ABS live sessions, offline uploads, PATCHes, the web player, and Arrmada's own restore and accept actions. All of them get the same guards and history.

### 1. Listening engine: report kinds and rules (internal/listening/rules.go)

| Kind | Source | Rule |
|---|---|---|
| Live | play session sync/close (ABS or web player) | No `currentTime` means touch the session only (listened, last_at, closed); the place is never moved. Back ≤120 s applies. Forward applies, except a jump of more than 120 s into the last 10 min (`endZone`=600 s) that exceeds `listened×4+30`: that is held as a forward hold. Back >120 s is held. A held jump is proven by 30 s of continuous listening from the new spot, or, for a forward hold, by playing on to the end with the position advancing. A Live session adopts a hold that has no session (`PendingSession==""`) when it plays on continuously from it. |
| Offline | `/api/session/local(-all)` | `at ≤ saved.updatedAt` is rejected (`older`). Back >120 s with <30 s listened is rejected (`unproven`). An end-zone forward that this session's own `startTime` and `listened` can't explain is rejected (`unproven`). Rejections are **recorded** and offered to the person, never silently dropped. |
| **Reported** (new) | `PATCH /api/me/progress/{id}` and the batch route | `lastUpdate` is honoured only when it is more than 2 min old (clock-skew margin). An older report is rejected (`older`, recorded). Back >120 s, or a forward jump into the end zone, is held with no session, and a Live session or "Use this spot" can confirm it. Anything else applies (`set`). |
| Manual | isFinished true/false, restore, accept, undiscard | Always applied. |

A finished book reopened by a play session starts at 0, with a pre-filled hold at 0 tied to that session. Thirty seconds of listening confirms the restart and clears Finished; tapping and closing leaves the book finished.

### 2. One timeline per book (listen_history + soft delete)

- `listen_history.kind`: `applied | before | rejected | held | discarded | restored`, plus `dismissed`. The `reason` keeps the existing words.
- Before any non-routine change (rewind, manual, restore, accept, offline, set, forward-proven, finish), a `before` row records the previous place. This closes the 5-minute routine-history gap. Pruning is per kind: 50 applied/before rows, 30 others.
- `listen_progress.discarded_at`: DELETE from an app becomes a soft delete. Apps no longer see the place (404 and absent from lists). The person sees it under "Recently removed" for 90 days and can restore it. A new report after a discard starts fresh, and the upsert clears `discarded_at`.
- The You page shows, per place: the timeline, with a reason in words and "Go back here" on each row; a held-jump banner that names the direction; an **offer banner** ("Your Pixel uploaded a later spot (4:02:11) from listening offline — use it?"); and a Recently removed card.

### 3. ABS protocol layer (internal/audioserver)

- **Fidelity by fixture.** `cmd/abs-capture` (dev-only, reads `ABS_URL/ABS_USER/ABS_PASS` from env) records a throwaway ABS container pinned to `ServerVersion`. The library holds one public-domain book with mp3s and an EPUB. Output goes to `testdata/abs/<version>/<step>.json`, scrubbed. `shape_test.go` diffs every reply recursively (keys, JSON kinds, id shapes) against it. `allowed_diffs.txt` lists the deliberate differences, so CI fails only on new drift.
- **Ids.** An `idCodec` interface with `legacyCodec` (today's `b12`, `b12v3`, `arrmada-audiobooks`, `u3`, hash ids) and `uuidCodec`, a reversible UUID with a kind marker in group 1, the version id in 40 bits across groups 2–4 with the v4/variant bits set, and the book or user id in group 5. Author and series ids are `sha1(name)` formatted as a UUID. Every input accepts both shapes. Output uses the codec from the request context, set from `audio_tokens.id_style`, which is stamped at sign-in from the setting `audioserver_id_style_new` (default `legacy`). Internal keys (`b12v3`) never change; translation happens only at the JSON boundary.
- **Catalogue state from the DB.** Readiness and size/duration totals come from `audio_file_meta` range sums under each item root, with no filesystem access. Warm probes only items with no known rows or flagged dirty by `book.imported` or the new `book.files_changed` event. A daily `audioserver-verify` job does the one full walk. The file-list TTL goes from 2 to 30 min, with invalidation on events and a re-list when a cached path has vanished.
- **Metadata.** `books.meta_updated_at` is kept current by a SQLite trigger on the metadata columns, plus a TouchMeta call on cover upload. Item `updatedAt` is the max of the item and edit times, and covers requested with `?ts=`/`?v=` are cached for a year as immutable. The narrator comes from file tags (narrator, composer, album_artist when it isn't the author, then performer), with a manual override per book or version. When the book has an ebook, `media.ebookFile` is filled in the recorded shape and served on the ABS ebook routes. `audiobooksOnly` stays true.
- **Tracing.** An admin "Trace app requests for 24 h" switch (`audioserver_trace_until`) logs the normally skipped play, cover, file and sync traffic, still as route patterns and query keys only (SEC's redaction rule).

### 4. Arrmada listening API (for the web player; APP builds the UI)

All routes are `a.protected` under `/api/v1/me/audio/`, so they are already in `externalAllowedPrefixes` and work through the tunnel. Each requires the server to be switched on and `AudioServer.Allowed(u)`, and returns 403 with plain-language messages otherwise. The main-API request log records `r.Pattern`, never the item key.

| Route | Purpose |
|---|---|
| GET `shelves` | Continue listening, Continue series, Recently added, Listen again (the same `Shelves()` that `/personalized` maps to ABS JSON) |
| GET `library?q=&sort=&page=` | The catalogue as cards |
| GET `items/{key}` | Detail: description, chapters, tracks `{ino,index,start_offset,duration,mime}`, other versions, bookmarks, progress |
| GET `items/{key}/cover` | Cover (the books cover route isn't externally reachable) |
| POST `items/{key}/play` | `OpenSession` (device "Web player · <browser>", client "Arrmada web") returns `{session_id,start_time,duration,tracks,chapters,restart}` |
| POST `sessions/{sid}/sync` and `/close` | `{current_time?, time_listened, duration}` returns `{position, held_position, finished}`. A missing `current_time` never moves the place. |
| GET `items/{key}/file/{ino}` | Range streaming (`http.ServeFile`), `Cache-Control: private` |
| GET/POST/DELETE `items/{key}/bookmarks` | Bookmarks |
| POST `undiscard`, POST `dismiss` | Timeline actions (with the existing `restore` and `accept`) |

### 5. Migrations and settings (numbers assigned at implementation time, next free ≥0090; coordinate with other epics)

- `listen_timeline`: `listen_history ADD kind, dismissed`; `listen_progress ADD discarded_at`.
- `audio_id_style`: `audio_tokens ADD id_style TEXT NOT NULL DEFAULT 'legacy'`.
- `books_meta_updated`: `books ADD meta_updated_at`, plus the AFTER UPDATE OF trigger.
- `audio_narrator`: `audio_file_meta ADD narrator, meta_version`; `books ADD audiobook_narrator`; `book_audio_versions ADD narrator`.
- New setting keys: `audioserver_trace_until`, `audioserver_id_style_new`.

### 6. Invariants every task must keep

- `listen_log` keeps no book column. Admin routes never expose places, history, timelines or titles. Users see only their own.
- Logs never contain item, author or series ids or query values: the audioserver uses route patterns (SEC), and so does the main API for `/api/v1/me/audio/`. Debug lines never pair a username with an item.
- No credentials in code, fixtures or chat. The capture tool reads env, and fixtures are scrubbed and reviewed before commit.
- UI follows the existing palette, type scale and the card, button and banner styles in `Audiobooks.tsx`, and is checked at phone width.
- Race tests run in Docker before push. Commits end with the Co-Authored-By trailer.

#### Milestone: M1: Places are safe, honest and restorable

_An empty close never moves anyone's place. Listen Again starts from 0. App PATCHes go through the same guards as everything else. Every rejected, held or discarded place appears in a per-book timeline with one-tap restore, a 'later spot' offer and a Recently removed card. A bad forward report can no longer finish a book. Each task ships alone; AUD-06 is P2 and can slide after M2._

<a id="aud-01"></a>
- [x] **AUD-01 · A session close or sync with no position never moves anyone's place** — `P1` · `S` · Phase 1
  - **Problem:** `syncBody.CurrentTime` is a plain float64 (internal/audioserver/handlers_play.go:79-83). `readOptionalJSON` leaves it at 0 when ContentLength==0 (handlers_play.go:428-431), and a `null` or `{}` body does the same through decodeLenient (media.go:21-27). `sync()` then hands 0 to `listening.Store.Sync` as a Live report (store.go:114-117). If the saved place is within 120 s, `accept("back")` resets it to 0. Further in, a pending hold at 0 is stored and `cur_pos=0` is written to the session (store.go:123-130). The You page then shows 'An app jumped back to 0:00 … Use this spot' (Audiobooks.tsx:373-377), and tapping it really does reset the place. No test covers close or an empty-body sync.
  - **Approach:** 1. handlers_play.go: add `type optNum struct{ V float64; OK bool }`. Its UnmarshalJSON sets OK only when the value is a JSON number or a numeric string (reuse flexNum's parsing). `null`, `""`, garbage and a missing key leave OK=false. A plain `*flexNum` is not enough, because it would turn a garbage string into a pointer to 0. Change syncBody to `CurrentTime optNum; TimeListened flexNum; Duration flexNum`, so numeric strings stop failing the whole body.
    2. internal/listening/store.go: change the signature to `Sync(ctx, userID int64, sessionID string, position *float64, listened, duration float64, closeIt bool) (Decision, error)`. When position == nil: skip apply; still cap listened by wall time (unchanged maxListen logic); `UPDATE listen_sessions SET last_at=?, listened=?, closed=? WHERE id=?`, leaving cur_pos alone; call touchLog; return `Decision{Progress: <current place from s.progress>, Reason: "no-position"}`. When position is set, behaviour is unchanged.
    3. handlers_play.go `sync()`: pass `&body.CurrentTime.V` only when `body.CurrentTime.OK`. The duration fallback via itemDuration is unchanged. Don't add any new username/item log pairing; SEC owns removing the existing debug line.
    4. Factor the body of `sync()` into `func (s *Server) syncSession(ctx, userID int64, sid string, pos *float64, listened, dur float64, closeIt bool) (listening.Decision, error)`, which resolves the duration as today, so [AUD-08](#aud-08)'s web-player sync and close reuse it.
    5. store_test.go: update the existing Sync call sites with a `at(v float64) *float64` helper.
  - **Files:** `internal/audioserver/handlers_play.go`, `internal/listening/store.go`, `internal/listening/store_test.go`, `internal/audioserver/server_test.go`
  - **Acceptance:**
    - POST /api/session/{sid}/close with no body, with `null` or with `{}` leaves listen_progress position, finished and pending_* untouched, and marks the session closed=1.
    - A sync carrying only timeListened increases listen_sessions.listened and the listen_log seconds, and leaves the place and cur_pos unchanged.
    - An empty close never produces the 'jumped back to 0:00' banner, whether the saved place is under 120 s into the book or deep into it.
    - A sync with currentTime sent as a numeric string ("120.5") applies as a number.
  - **Tests:** Go listening: TestSyncWithoutPositionKeepsPlace. Save place 3600; Sync with nil position, listened 20, close=true. Position stays 3600, PendingPosition is nil, the session has closed=1 and listened +20, and listen_log seconds are updated.; Go audioserver: TestCloseWithoutBodyKeepsPlace. Play, sync to 120, then close with ContentLength 0, then 'null', then '{}'. GET /api/me/progress/{id} shows currentTime 120 and the store shows no pending. Repeat with a saved place of 90 s, which must stay 90.; Go audioserver: TestSyncNumericStringsAccepted ({"currentTime":"150","timeListened":"30"}).
  - **Risk:** Low. Real ABS treats a missing currentTime as no update, so no client relies on an empty close saving 0. The signature change touches every Sync caller: there's one in production and a handful in tests.
  - **Resolves:** audiobooks-3
<a id="aud-02"></a>
- [x] **AUD-02 · Replaying a finished audiobook starts from the beginning (Listen Again works)** — `P1` · `S` · Phase 1
  - **Problem:** OpenSession (internal/listening/store.go:60-69) sets StartPos and CurPos to the saved position with no Finished check. setFinished moves Position to the full duration on an explicit finish (rules.go:193-196), and an auto-finish leaves it within 5 s of the end (rules.go:205-207). handlePlay returns `startTime/currentTime = sess.StartPos` (handlers_play.go:74). So tapping a book on the Listen Again shelf (handlers_library.go:264-286) plays the last seconds and stops. Scrubbing back to 0 is then a held jump that needs 30 s of proof and shows the 'jumped back' banner.
  - **Approach:** 1. listening/store.go OpenSession: when the saved place was found and `p.Finished`, start the session at 0 (StartPos=CurPos=0) and pre-fill the hold: PendingPosition=0, PendingSession=sess.ID, PendingListened=0, PendingAt=now. Persist it with writeProgress, without a history row. Finished stays true until the restart is proven. Add a non-persisted `Restart bool` to Session so callers know. Decide's existing held-jump continuity (rules.go:168-186) then confirms it after 30 s of continuous listening from 0. `accept("rewind")` clears Finished because back > threshold and pos < dur-finishedTail.
    2. handlers_play.go handlePlay: when `sess.Restart`, build the reply's `libraryItem.userMediaProgress` from a copy of the progress with Position 0, Finished false and PendingPosition nil. Clients that seek from userMediaProgress instead of the session's startTime then also start at 0. Use the same view in sessionJSON's libraryItem.
    3. Tap and close: with [AUD-01](#aud-01), an open followed by an empty close doesn't touch the place, so the book stays finished. The pending restart hold stays until the next accepted report clears it (accept() calls clearPending).
    4. `Store.AcceptPending` already applies Manual with Finished=false, so 'Start over now' works unchanged.
    5. web/src/pages/Audiobooks.tsx PlaceRow: when `p.finished && p.pending_position === 0`, replace the warning banner with neutral copy in the same banner style: 'Listening again from the start — saved once you've listened for a moment', with a 'Start over now' button (api.acceptAudioJump).
  - **Files:** `internal/listening/store.go`, `internal/listening/store_test.go`, `internal/audioserver/handlers_play.go`, `internal/audioserver/server_test.go`, `web/src/pages/Audiobooks.tsx`
  - **Acceptance:**
    - POST /api/items/{id}/play on a finished book returns startTime and currentTime 0, and libraryItem.userMediaProgress.currentTime 0 with isFinished false.
    - After syncs continuing from 0 for 30 s of listening, the place is about 30 s, finished is false, and a 'rewind' history row exists.
    - Opening and then closing without listening (empty body) leaves the book finished, and it stays on the Listen Again shelf.
    - The You page shows the gentle restart message, not 'An app jumped back to 0:00', and 'Start over now' sets the place to 0, unfinished.
  - **Tests:** Go listening: TestFinishedBookReopensAtStart. OpenSession on a finished place gives StartPos 0, Restart true and pending 0 for that session; Live syncs at 15 and 30 (15 s listened each) give accepted, Finished=false, Position 30.; Go audioserver: TestListenAgainStartsFromZero. PATCH isFinished=true, then play gives startTime 0; syncs at 15 and 30 give isFinished false and currentTime 30. Separate case: play then an empty close keeps isFinished true.
  - **Depends on:** [AUD-01](#aud-01)
  - **Risk:** An app that keeps its own local position for a downloaded book (Lissen offline) may still resume at the end. That's outside the server's control; check Lissen and note it in memory if it does. Books shorter than about 2 minutes can't be proven through the rewind path; ignore them, since no real audiobook is that short.
  - **Resolves:** audiobooks-4
<a id="aud-03"></a>
- [x] **AUD-03 · Progress PATCHes from apps go through the sync guards instead of always winning** — `P1` · `M` · Phase 1
  - **Problem:** patchProgress (handlers_play.go:292-320) calls `listening.Store.SetProgress`, which always uses `Kind: Manual` with At=now (store.go:202-207). Decide applies Manual unconditionally (rules.go:140-147). So `PATCH /api/me/progress/{id}` and `PATCH /api/me/progress/batch/update` (handlers_play.go:340-354) skip the newer-only and 30-s-proof rules. Stamping UpdatedAt=now also makes later genuine offline uploads look older. The Server tab's promise that 'Older offline listening never replaces a newer place' (Audiobooks.tsx:456) doesn't hold on this route. No client has been seen sending stale PATCHes yet, but the module exists to stop exactly this.
  - **Approach:** 1. internal/listening/rules.go: add `Reported` to Kind, meaning an app setting a position outside a play session. In Decide:
       - If `r.At <= cur.UpdatedAt`, return `Decision{Progress:*cur, Reason:"older"}` (not applied).
       - If `cur.Position-pos > rewindThreshold`, hold it: PendingPosition=pos, PendingSession="", PendingListened=0, PendingAt=r.At, Reason "held".
       - Otherwise `accept("set")`.
       [AUD-06](#aud-06) later adds the end-zone forward rule here.
    2. In the Live branch, a pending hold with `PendingSession==""` may be adopted by the first Live report that is continuous with it (the same continuity window as today: `pos >= last-continuitySlack && pos <= last+listened*speedAllowance+continuitySlack`). Set PendingSession=r.SessionID and start counting PendingListened from that report. Then 30 s confirms it via accept("rewind"). Keep the existing rule that a big-back report from a different session starts a new hold.
    3. internal/listening/store.go: add `ReportPosition(ctx, userID int64, key string, pos, dur float64, at int64, device string) (Decision, error)` using Reported. Keep SetProgress (Manual) for explicit finished/unfinished and for Arrmada's own restore and accept.
    4. handlers_play.go: add `LastUpdate flexNum \`json:"lastUpdate"\`` to progressPatch, and make CurrentTime, Progress and Duration lenient (optNum/flexNum from [AUD-01](#aud-01)). patchProgress handles the cases in this order:
       (a) `isFinished==true`: SetProgress Manual finished (unchanged).
       (b) `isFinished==false`: Manual unfinish that keeps the saved position.
       (c) If currentTime or progress is present: ReportPosition with `at = lastUpdate` when `0 < lastUpdate <= now+5min && lastUpdate < now-2min` (the reportSkew constant: a phone clock a few seconds behind must not make a fresh seek 'older'), otherwise at = now.
       (d) Hide is unchanged.
       Reply with mediaProgress of the saved place, so a held or older PATCH returns the place actually kept. handleBatchProgress uses the same function per entry.
    5. `AcceptPending` and the You page banner work unchanged for a hold with no session.
    6. Audiobooks.tsx ServerView 'How places are kept': add 'A place an app sets without playing follows the same rules.'
  - **Files:** `internal/listening/rules.go`, `internal/listening/rules_test.go`, `internal/listening/store.go`, `internal/listening/store_test.go`, `internal/audioserver/handlers_play.go`, `internal/audioserver/server_test.go`, `web/src/pages/Audiobooks.tsx`
  - **Acceptance:**
    - A PATCH whose lastUpdate is more than 2 min older than the saved place's updated_at leaves the place unchanged, and the reply returns the saved place.
    - A PATCH more than 120 s backward (no lastUpdate) leaves the place unchanged, sets pending_position, and the You page shows the banner. Listening on from there in a play session for 30 s, or tapping 'Use this spot', confirms it.
    - A PATCH forward, or less than 120 s back, applies at once as today.
    - PATCH isFinished=true still marks the book finished immediately; isFinished=false unfinishes it without moving the place.
    - PATCH /api/me/progress/batch/update applies the same rules to each entry.
  - **Tests:** Go listening (rules): TestReportedOlderIsIgnored, TestReportedBigBackIsHeld, TestReportedHoldAdoptedByContinuousLiveSession, TestReportedForwardApplies, TestReportedWithinSkewIsNotOlder.; Go audioserver: TestPatchProgressRespectsGuards (older lastUpdate, far back, forward, isFinished=true, isFinished=false, and the batch route).
  - **Risk:** An app that saves a deliberate scrub-back only through PATCH now gets a hold instead of an instant save. The banner, 'Use this spot' and session adoption cover this. Check which route Lissen uses for a manual seek: its session sync or a PATCH. Touches the same files as AUD-01, AUD-02 and AUD-04; land them in order.
  - **Resolves:** audiobooks-2
<a id="aud-04"></a>
- [x] **AUD-04 · Place timeline, backend: record rejected, held and discarded places, make discard soft and restorable** — `P1` · `M` · Phase 8
  - **Problem:** Offline reports judged 'older' or 'unproven' return Dirty=false (rules.go:149-157), and apply returns before writing history (store.go:264-266). The only trace is listen_sessions.cur_pos, which no UI reads and Prune drops after 30 days (store.go:509-513). DeleteProgress hard-deletes the row even though its comment promises the place can be put back (store.go:244-248). handleMyAudio builds places only from AllProgress (httpapi/audioserver.go:279-291), so a discarded book has no restore path. Routine history is written at most every 5 min (store.go:299-306), so the place just before a jump or discard may be missing. Plane scenario: listen offline from 1h to 4h, then briefly open the book on a tablet, and the phone's later upload is silently rejected as older.
  - **Approach:** 1. New migration `listen_timeline` (next free number ≥0090):
       - `ALTER TABLE listen_history ADD COLUMN kind TEXT NOT NULL DEFAULT 'applied'` (applied|before|rejected|held|discarded|restored)
       - `ALTER TABLE listen_history ADD COLUMN dismissed INTEGER NOT NULL DEFAULT 0`
       - `ALTER TABLE listen_progress ADD COLUMN discarded_at INTEGER NOT NULL DEFAULT 0`
    2. store.go apply():
       (a) When `!d.Dirty` and Reason is older or unproven, insert a history row: kind 'rejected', position = the reported position, at = r.At, device = r.Device, reason = d.Reason.
       (b) When a hold first starts (cur.PendingPosition was nil and d.Progress.PendingPosition != nil), insert kind 'held' with the pending position.
       (c) Before any non-routine change (d.Changed and reason not forward/back), insert kind 'before' with cur.Position, cur.Device and at = cur.UpdatedAt. Non-routine covers rewind, manual, restore, accept, offline, set, forward-proven, and any report that flips Finished. Skip it when the newest history row for the item is already within 30 s of cur.Position.
       (d) Then the applied row is written as today.
    3. Replace Restore's `UPDATE … SET reason='restore' WHERE id=(SELECT MAX(id)…)` hack: give apply an optional reason override and pass 'restore'.
    4. recordHistory pruning per kind: keep the newest 50 applied/before rows and 30 others per (user, item).
    5. DeleteProgress becomes a soft delete: `UPDATE listen_progress SET discarded_at=now`, plus a kind 'discarded' row with the current position and device 'app'.
       - progressSelect callers Progress and AllProgress (used by ABS replies, userJSON, items-in-progress, personalized and handleMyAudio) filter `discarded_at = 0`.
       - The internal progress() used by apply and OpenSession also treats a discarded row as not found, so Decide(nil) starts fresh.
       - The writeProgress upsert sets `discarded_at = 0` in its DO UPDATE.
    6. New Store methods:
       - `Discarded(ctx, userID) ([]Progress, error)`: last 90 days, with DiscardedAt.
       - `Undiscard(ctx, userID, key) error`: clears discarded_at when > 0 and writes a 'restored' row; returns ErrNothingToRestore otherwise.
       - `Dismiss(ctx, userID, historyID) error`: sets dismissed=1 where id and user match.
       - `Offers(ctx, userID) (map[string]HistoryEntry, error)`: the newest undismissed 'rejected' row per item, more than 60 s past the saved place and under 14 days old.
       HistoryEntry gains Kind and Dismissed. Prune also hard-deletes rows with discarded_at older than 90 days.
    7. internal/httpapi/audioserver.go:
       - handleMyAudio: each place gains `offer` ({history_id, position, at, device} or null); add `removed` (Discarded resolved through AudioServer.Info).
       - New protected routes in httpapi/server.go: `POST /api/v1/me/audio/undiscard {item}` and `POST /api/v1/me/audio/dismiss {item, history_id}`.
       - handleMyAudioRestore: when the restored row is kind 'rejected', mark it dismissed after Restore.
       - Everything stays scoped to u.ID. No admin route reads listen_history.
    8. web/src/lib/api.ts: extend the AudioPlace, MyAudio and AudioHistoryEntry types and add undiscardAudio and dismissAudioOffer. This step is types only; the UI is [AUD-05](#aud-05).
  - **Files:** `internal/store/migrations/00NN_listen_timeline.sql`, `internal/listening/store.go`, `internal/listening/store_test.go`, `internal/audioserver/handlers_play.go`, `internal/audioserver/server_test.go`, `internal/httpapi/audioserver.go`, `internal/httpapi/server.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Plane scenario: after the tablet opens the book and the phone then uploads 4:02:11 offline, the place stays at the tablet's spot. GET /api/v1/me/audio shows an offer at 4:02:11 from the phone, and restoring that history id sets the place to 4:02:11 and dismisses the offer.
    - DELETE /api/me/progress/{id} from an app removes the place from the app's view: GET returns 404 and it's absent from /api/me, /api/me/progress and items-in-progress. It appears in `removed`, and POST undiscard brings back the exact position.
    - A new play after a discard starts at 0 and clears discarded_at.
    - Every rewind, restore, accept, offline apply, set or finish leaves the place from before the change in history, even within 5 min of the last routine row.
    - listen_log's schema is unchanged and has no book column. No admin endpoint returns history.
  - **Tests:** Go listening: TestRejectedUploadIsRecordedAndUsable (the plane scenario).; Go listening: TestDiscardIsSoftAndUndoable, TestNewReportAfterDiscardStartsFresh, TestPruneDropsOldDiscards, TestHistoryPrunedPerKind.; Go listening: TestNonRoutineChangeKeepsThePlaceBefore (a rewind within 5 min of the last history row still records a 'before' row).; Go audioserver: TestDeletedProgressHiddenFromApps.; Go httpapi: TestMyAudioOffersAndRemoved (offer present and above the place; undiscard and dismiss routes scoped to the caller).
  - **Risk:** There will be more history rows; per-kind pruning keeps them bounded. Every reader of listen_progress must honour discarded_at; today those are only progressSelect and writeProgress in store.go, but grep again before merging. If AUD-03 is in flight at the same time, land it first, since both touch store.go and apply().
  - **Resolves:** audiobooks-5
<a id="aud-05"></a>
- [x] **AUD-05 · Place timeline, UI: reasons in words, 'later spot' offers and Recently removed on the You page** — `P1` · `S` · Phase 8
  - **Problem:** Once AUD-04 records rejected, held, discarded and before rows, the person still can't see them. 'Earlier places' in PlaceRow (Audiobooks.tsx:344-391) lists bare clock times with no reasons, there's no offer for a rejected later spot, and no way to restore a discarded book. The overhaul's Listen tab will need the same widgets.
  - **Approach:** 1. Extract web/src/components/audiobooks/PlaceTimeline.tsx from PlaceRow's 'Earlier places' list. Each row shows the clock, when, device and a reason in words: played on {device} (applied), jumped back, jumped ahead, offline upload, set by an app, not used: older than your place, not used: jump not proven, held, place before a change, discarded in an app, restored. Each row has a 'Go back here' button that calls the existing restoreAudioPlace. Dismissed rejected rows are shown dimmed.
    2. web/src/components/audiobooks/OfferBanner.tsx renders 'Your {device} uploaded a later spot ({clock}) from listening offline — use it?' with 'Use it' (restore with the history id) and 'Dismiss' (dismissAudioOffer). It uses the existing banner style (var(--accent-soft)/accent-line for an offer, var(--avoid-soft) stays for the held-jump banner).
    3. PlaceRow renders OfferBanner when p.offer is set. The held-jump banner keeps the [AUD-02](#aud-02) restart variant. Replace the 'Earlier places' toggle content with PlaceTimeline.
    4. A new 'Recently removed' Card in YouView, shown only when data.removed is non-empty. Rows have the cover, title, '{clock} · removed {ago} in {device}' and a Restore button (undiscardAudio).
    5. Keep the current type scale (11–13px), Card component, ghost and primary button styles, and colour tokens. Check at 375px width: the banners wrap and the buttons stay tappable.
  - **Files:** `web/src/components/audiobooks/PlaceTimeline.tsx`, `web/src/components/audiobooks/OfferBanner.tsx`, `web/src/pages/Audiobooks.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A place with a pending offer shows the offer banner; 'Use it' moves the place and the banner goes away, and 'Dismiss' hides it for good.
    - Every timeline row shows a reason in words and can be restored.
    - A discarded book appears under Recently removed, and Restore puts it back in the places list at the same position.
    - Layout matches the existing page style and works at phone width with no horizontal scroll.
  - **Tests:** npm run build (type-check) passes.; Manual UI check on desktop and at 375px: offer banner, timeline reasons, Recently removed restore, the restart banner from AUD-02.
  - **Depends on:** [AUD-04](#aud-04)
  - **Risk:** Copy overload: keep the reason words short, and show only the 20 newest rows with a 'Show all' link.
  - **Resolves:** audiobooks-5
<a id="aud-06"></a>
- [x] **AUD-06 · Big forward jumps into the end of a book need proof before they finish it** — `P2` · `M` · Phase 8
  - **Problem:** In Decide's Live branch, any report with back <= 120 s is accepted, including forward jumps of any size (rules.go:160-167). setFinished then auto-finishes within 5 s of the end (rules.go:205-207), and speedAllowance is used only for held rewinds (rules.go:171). One bad report, such as a player sending the full duration on a stream error, marks the book finished and drops it from Continue Listening (handlers_library.go:264-268). AcceptPending always passes Finished=false (store.go:435-436), which would be wrong for a forward hold.
  - **Approach:** 1. rules.go: add `endZone = 600.0`. In the Live branch, compute `fwd := pos - cur.Position` and `bigForwardIntoEnd := dur > 0 && pos >= dur-endZone && fwd > rewindThreshold && fwd > r.Listened*speedAllowance+continuitySlack`. Accept immediately only when `back <= rewindThreshold && !bigForwardIntoEnd`.
    2. Pending continuation is direction-agnostic: a report continuous with the pending position, from the same session (or adopting a no-session hold per [AUD-03](#aud-03)), adds to PendingListened. It is proven when PendingListened >= rewindProof, or when it plays on to the end: `pos >= dur-finishedTail && pos > *cur.PendingPosition+0.5 && cur.PendingListened+r.Listened > 0`. The position must actually advance, so a player stuck re-reporting the full duration never proves itself. Proof leads to accept("rewind") for a backward hold and accept("forward-proven") for a forward one. A new forward hold uses Reason "held-forward".
    3. Offline: add `From float64` to Report, filled from OfflineSession.StartTime. If `dur > 0 && pos >= dur-endZone && (pos-r.From) > r.Listened*speedAllowance+continuitySlack`, return Decision{Reason:"unproven"}. [AUD-04](#aud-04) records it as rejected and offers it.
    4. Reported ([AUD-03](#aud-03)): the same end-zone forward condition holds the jump with no session instead of accepting it.
    5. Manual (explicit isFinished) is unchanged. In store.go AcceptPending, pass `Finished: nil` when the pending position is ahead of the saved one, so accepting a forward hold at the end auto-finishes. Keep `false` for backward holds.
    6. [AUD-04](#aud-04)'s 'before' row is written whenever an accepted report flips Finished.
    7. UI: the pending banner copy depends on direction. When pending_position > position: 'An app jumped ahead to {clock}, near the end. It's kept once you listen on from there — or use it now.' Add 'jumped ahead' to PlaceTimeline's reason words. In ServerView 'How places are kept', mention that a jump to the very end needs the same proof.
  - **Files:** `internal/listening/rules.go`, `internal/listening/rules_test.go`, `internal/listening/store.go`, `internal/listening/store_test.go`, `web/src/pages/Audiobooks.tsx`, `web/src/components/audiobooks/PlaceTimeline.tsx`
  - **Acceptance:**
    - A Live report jumping from 2:00:00 to the last 3 s of a 10 h book with 15 s listened leaves the place at 2:00:00 and not finished, and the banner appears.
    - Listening on from the jumped-to spot to the end (the position advancing) confirms it and marks the book finished.
    - A player re-sending the full duration in two consecutive reports stays held.
    - Normal listening, 30 s skips and chapter skips outside the last 10 minutes still save immediately.
    - An offline upload claiming the full duration after 60 s of listening from mid-book is not applied, and shows in the timeline as 'not used: jump not proven'.
    - 'Use this spot' on a forward hold at the end marks the book finished.
  - **Tests:** Go listening (rules): TestBigForwardJumpIntoEndIsHeld, TestListeningToTheEndConfirmsForwardJump, TestStuckFullDurationStaysHeld, TestNormalForwardStillImmediate, TestOfflineFinishNeedsListening, TestExplicitFinishStillApplies, TestReportedForwardIntoEndIsHeld.; Go listening (store): TestAcceptForwardHoldFinishes.
  - **Depends on:** [AUD-03](#aud-03), [AUD-04](#aud-04)
  - **Risk:** Someone who skips straight to an epilogue sees a hold until they listen for 30 s. The 10-minute end zone keeps this rare, and 'Use this spot' resolves it. Can be scheduled after M2 if the web player is more urgent.
  - **Resolves:** audiobooks-10

#### Milestone: M2: Listening inside Arrmada (web-player API)

_Arrmada has its own read and play API for audiobooks: shelves, catalogue, detail, play sessions, sync and close, Range streaming and bookmarks. It runs through the same listening guards, works through the tunnel for requesters, and logs no titles. APP can build the Listen tab and mini-player on top of it, which gives iPhone users a working path whatever happens with the apps._

<a id="aud-07"></a>
- [x] **AUD-07 · Arrmada listening API, read side: shelves, catalogue, item detail and covers for the web player** — `P1` · `M` · Phase 8
  - **Problem:** Listening is only possible through third-party apps on the separate audiobook port, and no iPhone app is verified. To build an in-app player (the overhaul's Listen tab, whose UI is in APP), Arrmada needs its own routes for browsing. Shelf logic lives only inside the ABS handler handlePersonalized (handlers_library.go:234-288). The books cover route `/api/v1/books/{id}/cover-image` isn't in externalAllowedPrefixes (httpapi/external.go:89-98), so covers would fail for requesters through the tunnel. The main API request log writes `r.URL.Path` at debug level (httpapi/middleware.go:25-41), so item keys would land in the log.
  - **Approach:** 1. New internal/audioserver/player.go (exported API for Arrmada's own player, kept separate from the ABS JSON):
       - `type Card struct{Key string; BookID, VersionID int64; Title, Author, Series, SeriesSeq, Cover string; Duration float64; Progress *listening.Progress}`. Cover is the relative `/api/v1/me/audio/items/{key}/cover?v=<updatedAt>`.
       - `func (s *Server) Shelves(ctx, userID) ([]Shelf, error)`, with `Shelf{ID, Label string; Items []Card}`. Refactor handlePersonalized's selection into `shelvesFor(items []Item, all []listening.Progress, prog map) []shelfDef{id,label,labelKey,items []Item}`. handlePersonalized maps it to itemMinified and Shelves maps it to Card; the ABS output stays identical.
       - `func (s *Server) Catalog(ctx, userID, q, sort string, page, limit int) ([]Card, int, error)`, reusing applyFilter, sortItems and the handleSearch matching.
       - `func (s *Server) Detail(ctx, userID, key) (ItemDetail, error)`: title, author, series, description, duration, chapters, tracks `[{ino,index,start_offset,duration,mime}]` (probe=true like itemExpanded), versions (other items with the same Book.ID), bookmarks and progress.
       - `func (s *Server) ServeCover(w, r, key)`, factored out of handleCover.
       Durations come from `probe.files(…, false)` today, and from the DB sums after [AUD-15](#aud-15).
    2. New internal/httpapi/audioplayer.go:
       - A helper `a.audioListener(w, r) (*auth.User, bool)` requires AudioServer != nil, the `audioserver_enabled` setting, and `AudioServer.Allowed(ctx,u)`. Otherwise it returns 403 'Audiobooks are switched off' or 'Your account isn't set up for audiobooks'.
       - Routes (a.protected): GET /api/v1/me/audio/shelves; GET /api/v1/me/audio/library?q=&sort=&page=; GET /api/v1/me/audio/items/{key}; GET /api/v1/me/audio/items/{key}/cover.
       - Register them in httpapi/server.go. They sit under /api/v1/me/, so they are already reachable from outside the LAN.
    3. Privacy in the main API log: in logRequests, log `r.Pattern` (set by ServeMux on the same request once next.ServeHTTP returns) instead of r.URL.Path for paths under /api/v1/me/audio/. If SEC's redaction task has switched the whole middleware to patterns, rely on that.
    4. web/src/lib/api.ts: typed functions audioShelves, audioLibrary, audioItem and the Card, ItemDetail types.
    5. ServerView copy: 'Switching the audiobook server off also turns off listening in Arrmada.'
  - **Files:** `internal/audioserver/player.go`, `internal/audioserver/handlers_library.go`, `internal/audioserver/media.go`, `internal/httpapi/audioplayer.go`, `internal/httpapi/server.go`, `internal/httpapi/middleware.go`, `internal/httpapi/audio_player_test.go`, `web/src/lib/api.ts`, `web/src/pages/Audiobooks.tsx`
  - **Acceptance:**
    - An allowed requester can fetch shelves, the library list with search and paging, item detail with chapters and tracks, and covers, from the LAN and through the tunnel.
    - A denied user, or the server switched off, gets 403 with the plain-language message.
    - The ABS /personalized reply is unchanged after the refactor (same shelf ids, labels and entity keys).
    - No item key appears in the main API's request log lines for these routes.
  - **Tests:** Go httpapi: new audio_player_test.go harness (api with AudioServer from audioserver.New on a temp DB with one book and fake mp3s, mirroring audioserver's newHarness): TestWebPlayerShelvesAndDetail, TestWebPlayerRespectsAllowListAndSwitch, TestWebPlayerLogHasNoItemKey (capture slog output).; Go audioserver: TestPersonalizedUnchangedAfterShelvesRefactor.
  - **Risk:** Gating on the same switch as the third-party port is intentional, so there is one on/off; the copy says so. Detail probes files on first open (seconds of ffprobe for a big book), the same as ABS's expanded item.
  - **Resolves:** audiobooks-7
<a id="aud-08"></a>
- [x] **AUD-08 · Arrmada listening API, play side: sessions, sync and close, Range streaming, bookmarks (same guards as the apps)** — `P1` · `M` · Phase 8
  - **Problem:** The web player needs to start sessions, stream audio with seeking, and save its place. Those must go through listening.Store, so the durable sessions, held jumps, newer-only rules, history and the privacy-preserving listen_log apply to it exactly as to Lissen. Today serveFile (media.go:34-60) and sync (handlers_play.go:88-117) are reachable only through the ABS port with an ABS token.
  - **Approach:** 1. internal/audioserver/player.go:
       - `func (s *Server) StartSession(ctx, userID int64, key, deviceID, deviceName string) (PlayStart, error)` calls `Listen().OpenSession(u, key, deviceID, "Web player · "+deviceName, "Arrmada web")` and returns `{session_id, start_time, duration, tracks, chapters, restart}`. restart comes from [AUD-02](#aud-02)'s Session.Restart. Track urls are relative: `/api/v1/me/audio/items/{key}/file/{ino}`.
       - `func (s *Server) SyncSession(ctx, userID, sid, pos *float64, listened, dur float64, closeIt bool)` reuses [AUD-01](#aud-01)'s syncSession.
       - `func (s *Server) ServeFile(w, r, key, ino string)`, refactored out of serveFile to take key and ino.
    2. internal/httpapi/audioplayer.go, all behind a.protected and audioListener:
       - POST /api/v1/me/audio/items/{key}/play {device_id, device_name}
       - POST /api/v1/me/audio/sessions/{sid}/sync and POST /api/v1/me/audio/sessions/{sid}/close with `{current_time?, time_listened, duration}`, using optNum pointer semantics (a missing current_time never moves the place) and returning `{position, held_position, finished}`. ErrSessionNotFound (wrong user or unknown id) returns 404.
       - GET /api/v1/me/audio/items/{key}/file/{ino}: Range via http.ServeFile, `Cache-Control: private, max-age=86400`. Same-origin `<audio src>` sends the session cookie, so no token is needed in the URL.
       - GET, POST and DELETE /api/v1/me/audio/items/{key}/bookmarks backed by Listen().Bookmarks, AddBookmark and DeleteBookmark.
    3. No NoteDevice call: the web player has no token family. Its sessions show in the admin Listening tab through listen_log (device 'Web player · Safari', client 'Arrmada web') with time and duration only.
    4. web/src/lib/api.ts: audioPlay, audioSync, audioClose, audioBookmarks and friends, plus an `audioFileUrl(key, ino)` helper that prefixes the app's base path.
    5. Leave a short README comment at the top of audioplayer.go describing the contract the APP Listen tab uses: sync every 15 s while playing; close on pause, page hide and ended; time_listened is wall-clock seconds while playing.
  - **Files:** `internal/audioserver/player.go`, `internal/audioserver/media.go`, `internal/audioserver/handlers_play.go`, `internal/httpapi/audioplayer.go`, `internal/httpapi/server.go`, `internal/httpapi/audio_player_test.go`, `web/src/lib/api.ts`
  - **Acceptance:**
    - An allowed requester can start a session, stream a file (a Range request returns 206 with Content-Range), sync and close it; the place updates.
    - A sync more than 120 s backward from the web player is held exactly as for an app, and an empty close leaves the place alone.
    - Using another user's session id returns 404, and a denied user or the server switched off gets 403.
    - The admin Listening tab shows 'Web player' sessions with time and duration but no titles.
    - The routes work from outside the LAN for requesters, through the existing /api/v1/me/ allowlist.
  - **Tests:** Go httpapi: TestWebPlayerPlaySyncClose (including a far-back sync that's held and an empty close), TestWebPlayerStreamsWithRange (206 and Content-Range), TestWebPlayerCannotTouchOthersSessions, TestWebPlayerBookmarks, TestWebPlayerSessionInListenLogWithoutTitle.
  - **Depends on:** [AUD-01](#aud-01), [AUD-07](#aud-07)
  - **Risk:** Streaming audio for remote requesters goes through the Cloudflare tunnel on the main port. That's bandwidth on the tunnel; mention it in the Server tab copy if the owner cares. A misbehaving web-player loop could inflate listened time; Sync already caps it by wall time.
  - **Resolves:** audiobooks-7

#### Milestone: M3: Know what apps expect; a verified iPhone app

_CI diffs every reply against recorded real Audiobookshelf replies. The cheap gaps are closed. An admin can trace an app's whole conversation for 24 h without exposing what anyone plays. ShelfPlayer and/or the official Audiobookshelf app are verified on an iPhone and encoded as conversation tests, and setup copy names only verified apps._

<a id="aud-09"></a>
- [x] **AUD-09 · Compatibility harness: record real Audiobookshelf replies and diff Arrmada's against them in CI** — `P2` · `M` · Phase 8
  - **Problem:** The contract test (server_test.go:150-290) only uses need() to check that a few keys exist; it never checks JSON types, nested shapes or id formats. Every new client is debugged one guess and one commit at a time, and nobody can see what's actually different from a real server (Plappa stalls right after /api/libraries).
  - **Approach:** 1. cmd/abs-capture/main.go, a dev-only Go main (the Dockerfile builds only ./cmd/arrmada; confirm this stays true).
       - It reads ABS_URL, ABS_USER and ABS_PASS from env, so nothing is hardcoded, committed or pasted in chat.
       - Owner recipe, in a comment and the -h output: `docker run --rm -p 13380:80 ghcr.io/advplyr/audiobookshelf:<ServerVersion>`. In it, create a throwaway root user and a book library holding ONE public-domain book with both LibriVox mp3s and the Project Gutenberg EPUB of the same title, so the ebookFile shape is captured for [AUD-18](#aud-18). Never point it at a real ABS.
       - It records, in order: GET /status, /ping; POST /login (with x-return-tokens: true, and once without to capture the cookie variant); POST /api/authorize; GET /api/me; /api/libraries (+?include=stats); /api/libraries/{id}?include=filterdata; /items?minified=1&limit=10&page=0; /personalized; /search?q=<word>; /authors; /series; /filterdata; /stats; /narrators; GET /api/items/{id}?expanded=1&include=progress; POST /api/items/{id}/play (with a deviceInfo); POST /api/session/{sid}/sync; POST /api/session/{sid}/close (empty body, to learn ABS's own reply); GET and PATCH /api/me/progress/{id}; GET /api/me/progress; /api/me/items-in-progress; /api/me/listening-stats; POST /api/me/item/{id}/bookmark; POST /api/session/local-all.
       - It scrubs token, accessToken and refreshToken values, any JWT-shaped string, Set-Cookie, usernames and emails, hostnames and IPs in any string, absolute filesystem paths (to /audiobooks/Book/…) and deviceInfo.ipAddress.
       - It writes `internal/audioserver/testdata/abs/<absVersion>/<step>.json` plus `steps.json` (method, path template, status).
    2. internal/audioserver/shape_test.go: `diffShape(path string, want, got any) []string` recurses.
       - Every key in want must exist in got; extra keys in got are fine.
       - JSON kinds must match. A null in want accepts anything; a null in got where want is non-null is reported.
       - Arrays are compared element by element against want[0] when got is non-empty.
       - `id` and `*Id` fields are flagged 'idshape' when want is UUID-shaped and got isn't.
    3. `TestRepliesMatchAudiobookshelf` drives the existing newHarness through the same steps (mapping ids to Arrmada's) and diffs each reply against the newest fixture version.
    4. Known gaps go in `testdata/abs/allowed_diffs.txt`, one `<step> <json.path> <reason>` per line with `[]` for array elements, so CI fails only on new drift. A unit test asserts every allowed line still matches a real diff, so stale allowances get removed.
    5. Owner review step: read the fixtures diff before committing to check that no secret slipped through.
  - **Files:** `cmd/abs-capture/main.go`, `internal/audioserver/testdata/abs/`, `internal/audioserver/testdata/abs/allowed_diffs.txt`, `internal/audioserver/shape_test.go`
  - **Acceptance:**
    - Committed fixtures contain no tokens, cookies, hostnames, IPs or real usernames, and record the ABS version they came from.
    - go test fails when a key or JSON type present in ABS's reply is missing or different in Arrmada's, unless allowed_diffs.txt lists that path with a reason.
    - An allowed_diffs line that no longer matches any diff fails the test (no stale allowances).
    - The capture includes an item with an ebookFile.
  - **Tests:** Go audioserver: TestRepliesMatchAudiobookshelf (fixture-driven).; Go audioserver: TestDiffShapeCatchesTypeAndIdDrift (synthetic objects: missing key, type change, null handling, UUID versus non-UUID id, array element shape).; Go audioserver: TestAllowedDiffsAreNotStale.; Go: a scrub unit test in cmd/abs-capture (JWT, token keys, IPs and hostnames replaced).
  - **Risk:** Fixtures age as Audiobookshelf changes; the version is recorded, the container is pinned to ServerVersion, and you re-capture when ServerVersion is bumped. The harness book has 0-duration files (no ffprobe in CI), so some numeric fields are 0, which is still the right kind.
  - **Resolves:** audiobooks-6
<a id="aud-10"></a>
- [x] **AUD-10 · Close the reply gaps the compatibility diff shows** — `P2` · `S` · Phase 8
  - **Problem:** Known gaps against real Audiobookshelf:
- mediaProgress has no userId (absjson.go:144-149).
- permissions lack createEreader and selectedTagsNotAccessible (absjson.go:179-180).
- serverSettings is a small subset (absjson.go:198-206).
AUD-09's first run will list more. Each is a chance for a strictly-typed client to fail silently.
  - **Approach:** 1. Run TestRepliesMatchAudiobookshelf and work through allowed_diffs.txt from the top. Check the /api/libraries and /api/authorize diffs first: Plappa stalls right after them. That isn't a target, but it's a free check.
    2. absjson.go:
       - mediaProgress gains `userId` (the user JSON id).
       - userJSON permissions gain createEreader:false and selectedTagsNotAccessible:false, plus any other permission keys in the fixture.
       - serverSettings() copies the full default key set and value types from the fixture (scanner*, storeCoverWithItem, storeMetadataWithItem, metadataFileFormat, rateLimit*, backup*, logger*, authLoginCustomMessage and so on) with harmless values.
       - Add whatever other missing keys and type mismatches the diff shows in the library, item, session, personalized, search and stats replies.
    3. Remove each fixed line from allowed_diffs.txt. What remains must be deliberate: id shapes (until [AUD-14](#aud-14)), podcasts, socket-only fields, with reasons.
    4. Keep TestLissenConversation green; Lissen's verified behaviour must not change.
  - **Files:** `internal/audioserver/absjson.go`, `internal/audioserver/handlers_library.go`, `internal/audioserver/handlers_compat.go`, `internal/audioserver/handlers_play.go`, `internal/audioserver/server.go`, `internal/audioserver/testdata/abs/allowed_diffs.txt`
  - **Acceptance:**
    - allowed_diffs.txt contains only deliberate differences, each with a reason.
    - TestLissenConversation and TestRepliesMatchAudiobookshelf pass.
    - Owner check: Lissen still signs in, browses, plays and syncs after deploy.
  - **Tests:** Go audioserver: TestRepliesMatchAudiobookshelf with the shrunken allowed_diffs.txt.; Go audioserver: TestMediaProgressHasUserID.
  - **Depends on:** [AUD-09](#aud-09)
  - **Risk:** A newly added field with a wrong value could change a client's behaviour (for example a serverSettings flag). Copy ABS's defaults exactly and keep Lissen's run-through as the gate.
  - **Resolves:** audiobooks-6
<a id="aud-11"></a>
- [x] **AUD-11 · Admin 'Trace app requests for 24 hours' switch that turns itself off** — `P2` · `S` · Phase 8
  - **Problem:** logRequest deliberately skips the steady play traffic: file, cover, image, sync, download, progress and GET session routes (server.go:582-599). So a client that fails silently during playback can't be seen. The only way to debug a new client is to ship a code change.
  - **Approach:** 1. New setting key `audioserver_trace_until` (unix ms). Server gets `traceUntil atomic.Int64`, loaded at New() and updated by a `SetTrace(until int64)` call from the admin handler, so there's no DB read per request.
    2. logRequest: while now < traceUntil, also log the normally skipped requests, tagged `trace=true`. They are still route patterns and query keys only, following SEC's redaction (no ids, no query values, no usernames).
    3. httpapi: `PUT /api/v1/audioserver {trace_hours: 24|0}` (admin) sets or clears it. GET /api/v1/audioserver returns `trace_until`.
    4. Audiobooks.tsx ServerView: a 'Trace app requests' card with a switch. While on, it shows 'Tracing until HH:MM — every request an app makes is logged (routes only, never which book)' and a 'Stop' button.
    5. When the time passes, tracing simply stops; the next GET shows it off.
  - **Files:** `internal/audioserver/server.go`, `internal/httpapi/audioserver.go`, `web/src/pages/Audiobooks.tsx`, `web/src/lib/api.ts`, `internal/audioserver/server_test.go`
  - **Acceptance:**
    - With tracing on, a play, sync, cover and file request each produce a log line containing the route pattern and no item id, query value or username.
    - After 24 h (or Stop), those requests are no longer logged.
    - The Server tab shows the end time while it's on.
  - **Tests:** Go audioserver: TestTraceSwitchExpires (fake clock: logged while active, not after).; Go audioserver: TestTraceLogsNoIdsOrQueries (captured slog output contains no 'b1', no search text).
  - **Depends on:** SEC (audiobook request-log redaction, finding audiobooks-1): tracing must reuse the route-pattern logging
  - **Risk:** A heavy listener produces many lines for 24 h, which the 50k ring absorbs. The privacy rule holds because traced lines carry routes, not ids.
  - **Resolves:** audiobooks-6
<a id="aud-12"></a>
- [ ] **AUD-12 · Verify open-source iOS clients (ShelfPlayer, official Audiobookshelf app) and fix what they need** — `P2` · `M` · Phase 8
  - **Problem:** Lissen (Android-only) is the only verified client, so iPhone family members have no verified app. ShelfPlayer and the official Audiobookshelf app are untested, not shown to be broken. Plappa is closed source, and the owner said not to depend on it. Setup copy names only Lissen (Audiobooks.tsx:242), and MyBooks says 'Listen in an app — audiobooks in Lissen' (MyBooks.tsx:90-93).
  - **Approach:** 1. Read ShelfPlayer's source (rasmuslos/ShelfPlayer, its Audiobookshelf client models and API layer) and the official app (advplyr/audiobookshelf-app: its server API calls, the iOS AudioPlayer and local-progress sync), the same way Lissen's models were checked. For each, list:
       - the routes it calls, including older ones such as /api/me/sync-local-progress if used
       - the fields and types it decodes as required
       - any id-shape assumptions
       - the auth flow (x-return-tokens, refresh, cookie)
       - whether it needs socket.io; the official app listens for progress events, so confirm it tolerates socket.io being absent
       - whether it sends PATCH for seeks (relevant to [AUD-03](#aud-03))
    2. Fix the gaps in absjson.go and the handlers. Encode each client's sign-in, browse, play, sync, close and offline-upload conversation as `TestShelfPlayerConversation` and `TestOfficialAppConversation`, modelled on TestLissenConversation, with field checks via diffShape where useful.
    3. The owner verifies on an iPhone (App Store or TestFlight) against both the home address and the tunnel address, with tracing on ([AUD-11](#aud-11)) if anything fails: sign in, browse, play with chapters, background, resume on another device, and an offline download plus upload.
    4. Record the verified app versions in the audiobook-server memory note. Update the setup copy in Audiobooks.tsx YouView and MyBooks.tsx to name only verified apps per platform, with the exact field labels each app uses (server type, address, username, password). If APP's Apps & devices registry (web/src/lib/audioApps.ts) exists, flip their status there instead.
    5. Write up any id-shape or socket.io blocker as evidence for [AUD-14](#aud-14) or a follow-up, decided with the owner. Plappa is not targeted.
  - **Files:** `internal/audioserver/absjson.go`, `internal/audioserver/handlers_library.go`, `internal/audioserver/handlers_play.go`, `internal/audioserver/handlers_compat.go`, `internal/audioserver/server_test.go`, `web/src/pages/Audiobooks.tsx`, `web/src/pages/MyBooks.tsx`, `web/src/lib/audioApps.ts`
  - **Acceptance:**
    - At least one iOS app signs in, lists the library, plays with chapters and syncs its position. The position is visible in Arrmada and resumes on another device.
    - Each verified client's conversation is encoded as a Go test that fails if the replies regress.
    - Setup copy names only verified apps per platform, with their exact field names.
  - **Tests:** Go audioserver: TestShelfPlayerConversation and TestOfficialAppConversation (requests and fields taken from the clients' source).; Manual: owner's iPhone run-through for each app (sign in, browse, play, background, resume elsewhere, offline upload).
  - **Depends on:** [AUD-09](#aud-09), [AUD-10](#aud-10), [AUD-11](#aud-11), SEC (audiobook request-log redaction)
  - **Risk:** A client may hard-require websockets or UUID ids. That becomes scope for AUD-14 or a socket.io stub, decided with the owner. The web player from M2 is the fallback iPhone path either way.
  - **Resolves:** audiobooks-6, audiobooks-7

#### Milestone: M4: UUID ids where a client needs them

_Every route accepts UUID-shaped ids. If the iOS verification shows a client needs UUIDs, and only with the owner's sign-off, devices that sign in afterwards can be given UUID ids while Lissen devices keep theirs and their downloads._

<a id="aud-13"></a>
- [x] **AUD-13 · Accept UUID-shaped ids on every incoming route (id codec, no output change)** — `P3` · `S` · Phase 8
  - **Problem:** The library id is the constant 'arrmada-audiobooks' (catalog.go:21). Items are 'b12'/'b12v3' (catalog.go:35-40), users are 'u'+id (absjson.go:176), and authors and series are 'au'/'se' plus a short hash (catalog.go:143-149). The code admits ABS clients expect UUIDs: session ids are already UUID-shaped (store.go:516-523). Switching output ids for everyone would orphan Lissen's downloads. The safe first step is to accept both shapes everywhere, so a later per-device switch is purely an output change.
  - **Approach:** 1. internal/audioserver/ids.go: an `idCodec` interface with item(key), media(key), user(id), library(), author(name) and series(name), plus parse functions.
       - `legacyCodec` reproduces today's ids exactly.
       - `uuidCodec` is reversible with no lookup table. Group 1 is a constant kind marker (item, media, user, library). The version id takes 40 bits across groups 2–4, with nibble 13 = '4' and the variant bits 10xx. The book or user id takes the 48 bits of group 5. The library gets a fixed UUID.
       - Author and series ids are sha1(lowercased name) formatted as a UUID (version nibble 5, variant set), resolved by recomputing, as today.
    2. Parsing accepts both shapes, and only these call sites change:
       - `parseItemKey` (catalog.go) accepts legacy or UUID item and media ids.
       - `checkLibrary` accepts 'arrmada-audiobooks' or the library UUID.
       - authorID and seriesID comparisons in handleAuthor, handleSeries and applyFilter (base64 filter values) match either form.
       - The offline-upload libraryItemId, the progress, bookmark and play routes, and batch/get all normalise to the internal key before touching listening.
    3. internal/listening keeps internal keys (b12v3). Nothing is stored in UUID form.
    4. Output is unchanged in this task.
  - **Files:** `internal/audioserver/ids.go`, `internal/audioserver/ids_test.go`, `internal/audioserver/catalog.go`, `internal/audioserver/handlers_library.go`, `internal/audioserver/handlers_play.go`, `internal/audioserver/handlers_compat.go`, `internal/audioserver/server_test.go`
  - **Acceptance:**
    - Every route that takes an item, library, author or series id answers identically for the legacy id and its UUID form.
    - An offline upload or PATCH using a UUID item id updates the same internal place as the legacy id.
    - Replies are byte-for-byte unchanged for existing clients (TestLissenConversation passes).
  - **Tests:** Go audioserver: TestUUIDCodecRoundTrip (large book and version ids, version 0, malformed and wrong-marker input rejected).; Go audioserver: TestRoutesAcceptBothIdShapes (play, sync, progress PATCH, local-all, bookmarks, author, series, filter).
  - **Depends on:** [AUD-09](#aud-09)
  - **Risk:** Low; this is input only. A sloppy parser could accept garbage as an id, so the strict marker and version checks are tested.
  - **Resolves:** audiobooks-6
<a id="aud-14"></a>
- [ ] **AUD-14 · Per-device id style: UUID ids for apps that sign in after the switch (owner sign-off)** — `P3` · `M` · Phase 8
  - **Problem:** Some ABS clients may require UUID-shaped ids (AUD-12 evidence, or Plappa's stall after /api/libraries). Changing ids for every device would orphan Lissen's downloads, which the owner's notes say must be asked about first.
  - **Approach:** 1. New migration `audio_id_style`: `ALTER TABLE audio_tokens ADD COLUMN id_style TEXT NOT NULL DEFAULT 'legacy'`.
    2. auth.go:
       - `Accounts.issue` stamps the style for new sign-ins from setting `audioserver_id_style_new` (default 'legacy').
       - Refresh issues tokens in the same family and copies the family's id_style, so a device never changes style.
       - Validate returns the style, and requireAuth puts the codec into the request context (`codecOf(ctx)`).
    3. Every JSON builder takes the codec from context: itemMinified, itemExpanded (including contentUrl paths), mediaProgress, bookmarkJSON, sessionJSON (including libraryId, libraryItemId, userId, bookId), userJSON, loginJSON (userDefaultLibraryId), libraryJSON and folders, handleAuthors, handleAuthor, handleSeriesList and handleSeries, personalized, search, filterdata, local-all results, and items-in-progress. Grep for `libraryID`, `it.Key`, `"u" + itoa`, `"m" + `, authorID( and seriesID( in absjson.go and the handlers to catch every one.
    4. Admin Server tab: 'IDs for newly signed-in apps: Classic / UUID', with the note 'Devices already signed in keep theirs; switching a phone means signing it out and in again, which re-downloads its books.' The default stays Classic until the owner decides.
    5. Shape test: run TestRepliesMatchAudiobookshelf a second time with a UUID-style token, with the idshape lines removed from allowed_diffs for that run.
  - **Files:** `internal/store/migrations/00NN_audio_id_style.sql`, `internal/audioserver/auth.go`, `internal/audioserver/server.go`, `internal/audioserver/absjson.go`, `internal/audioserver/handlers_library.go`, `internal/audioserver/handlers_play.go`, `internal/audioserver/handlers_compat.go`, `internal/audioserver/shape_test.go`, `internal/httpapi/audioserver.go`, `web/src/pages/Audiobooks.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A device signed in with the legacy style sees exactly today's ids, and Lissen's downloads keep working.
    - A device signed in with the UUID style sees UUID-shaped ids everywhere, and the shape test's id checks pass for it with no idshape allowances.
    - Requests using either style (play, sync, progress PATCH, local-all, bookmarks) reach the same internal place.
    - Changing the setting affects only new sign-ins; refreshes keep the family's style.
  - **Tests:** Go audioserver: TestUUIDDeviceConversation (full Lissen-style conversation with a UUID-style token, asserting no legacy id appears anywhere in any reply).; Go audioserver: TestLegacyDeviceUnchanged, TestRefreshKeepsIdStyle.; Go audioserver: TestRepliesMatchAudiobookshelf run for the UUID style.
  - **Depends on:** [AUD-13](#aud-13), [AUD-12](#aud-12) (evidence that a target client needs UUIDs), Owner sign-off before the default changes
  - **Risk:** Large surface: one builder that misses the codec mixes id styles and breaks a client's cache. TestUUIDDeviceConversation scans every reply for legacy-shaped ids to catch it. Don't flip the default without the owner's sign-off.
  - **Resolves:** audiobooks-6

#### Milestone: M5: Library polish: quiet disks, fresh covers, narrators, ebooks

_The warm-up and stats no longer walk the audiobook tree. Cover and title edits reach apps. Narrators show in apps and can be edited in Arrmada. The official app can open the EPUB of a book that also has an audiobook._

<a id="aud-15"></a>
- [ ] **AUD-15 · Warm-up, listings and library stats read probe state from the DB instead of walking every audiobook folder** — `P3` · `M` · Phase 13
  - **Problem:** audioserver-warm runs every 30 min while the server is on (cmd/arrmada/main.go:591-598). Warm calls probe.files(…, false) for every item (catalog.go:228-259). The file-list cache lasts 2 min (cache.go:16), so every run does an os.Stat, a WalkDir, a stat per file and a DB query per file (probe.go:66-120), even when nothing has changed. LibraryStats does the same on admin page loads and after toggles (httpapi/audioserver.go:70, 99, 118), as do ?include=stats, /stats and the itemMinified used by every list reply (handlers_library.go:24-36, handlers_compat.go:93-125, absjson.go:60-62). On Unraid this can keep array disks awake.
  - **Approach:** 1. probe.go: add `prober.knownUnder(ctx, root) (files int, seconds float64, bytes int64)`: `SELECT COUNT(*), COALESCE(SUM(duration),0), COALESCE(SUM(size),0) FROM audio_file_meta WHERE duration > 0 AND (path = ? OR (path >= ? AND path < ?))` with root, root+"/" and root+"0" ('/'+1). It uses the primary-key index and never touches the filesystem.
    2. Readiness: an item is ready when knownUnder.files > 0 and its book id isn't in the server's in-memory `dirty` set. Don't rely on Book.Audiobook.FileCount: imports record it inconsistently (automation/books.go:1110, 1256, 1550 pass 1).
    3. WatchImports subscribes to `book.imported` and a new `book.files_changed` event, and adds the payload's book id to dirty before invalidating. Publish book.files_changed (never book.imported, which would fire the requester 'ready' notification in requests/usernotify.go) from DeleteBookEdition, BookRename (automation/books.go:1178, 1229) and DeleteAudioVersionFile (books_versions.go:278). MergeAudiobook already publishes book.imported.
    4. Warm skips ready items with no filesystem access. It fully lists and probes dirty or unknown items (paced 200 ms as today) and clears their dirty flag.
    5. LibraryStats, libraryTotals (?include=stats), handleLibraryStats and itemMinified's numTracks, duration and size all use knownUnder and never walk. itemExpanded, play, file and download still list files.
    6. A new daily scheduled job, `audioserver-verify` (24 h, not at start, only while the server is on), does the full walk with probe=false. It deletes audio_file_meta rows for vanished files under item roots that still exist, and skips an item entirely when its root is missing (disk or share offline).
    7. cache.go: raise the file-list TTL to 30 min. serveFile: when a cached file path no longer exists, invalidate that item's entry and re-list once before returning 404.
    8. Test seam: prober gets `stat func(string) (os.FileInfo, error)` and `walk func(string) []library.BookFile` fields (defaulting to os.Stat and library.FindBookFiles), so tests can count filesystem calls.
  - **Files:** `internal/audioserver/probe.go`, `internal/audioserver/probe_test.go`, `internal/audioserver/catalog.go`, `internal/audioserver/cache.go`, `internal/audioserver/media.go`, `internal/audioserver/absjson.go`, `internal/audioserver/handlers_compat.go`, `internal/audioserver/handlers_library.go`, `internal/automation/books.go`, `internal/automation/books_versions.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - With the server on and nothing changed, a warm run makes zero stat or walk calls (counted through the test seam).
    - Loading the admin Audiobooks page, library lists and the apps' /stats never walks the audiobook tree.
    - A newly imported or re-imported book is probed and shows as ready within a minute.
    - A file deleted outside Arrmada is noticed by the daily verify, and a missing share doesn't wipe probe data.
    - Deleting or renaming an audiobook edition doesn't send a 'ready' notification.
  - **Tests:** Go audioserver: TestWarmSkipsKnownItemsWithoutDisk, TestWarmProbesDirtyItem, TestLibraryStatsFromDB, TestVerifyPrunesVanishedFiles, TestVerifyKeepsRowsWhenRootMissing, TestServeFileRelistsAfterVanishedPath.; Go automation: TestDeleteEditionPublishesFilesChanged (and not book.imported).
  - **Risk:** A stale readiness answer after an out-of-band file swap lasts until the daily verify or a 404 re-list. That's acceptable because playback re-lists on a missing file. Counts in list replies may include a just-deleted file for up to a day.
  - **Resolves:** audiobooks-8
<a id="aud-16"></a>
- [ ] **AUD-16 · Apps pick up cover and metadata edits: real updatedAt and cache-busted covers** — `P3` · `S` · Phase 13
  - **Problem:** Every item reports `"updatedAt": it.AddedAt` (absjson.go:69-70, 121-122), so apps that add ?ts=updatedAt to cover URLs never refresh a cover. Covers are served at a fixed /api/items/{id}/cover with max-age=86400 for uploaded covers and 604800 for remote ones (media.go:126-130, 173). Title edits reach apps on their next fetch, but cover changes can take up to a week, or forever if an app keys its own image cache on updatedAt.
  - **Approach:** 1. New migration `books_meta_updated`:
       - `ALTER TABLE books ADD COLUMN meta_updated_at INTEGER NOT NULL DEFAULT 0` (unix ms).
       - A trigger `AFTER UPDATE OF title, author, cover_url, description, year, subjects, series_name, series_position, audiobook_path ON books` that sets `meta_updated_at = CAST((julianday('now')-2440587.5)*86400000 AS INTEGER)` for NEW.id. Check the exact column names in the books schema. The column list excludes meta_updated_at, so there's no recursion, and it catches every path (OverrideMetadata, SetCover, Refresh, Rematch, applyUpgrade, SetSeries, MarkImported, ClearEdition, dedupe) without touching each call site.
       - A similar trigger on book_audio_versions (label, path) bumps the parent book.
    2. books.Repo and Service: read meta_updated_at into Book.MetaUpdatedAt and add `TouchMeta(ctx, id)`. Call it from the cover upload and delete handlers (httpapi/books.go handleUploadBookCover ~457 and the delete path), because those write a file, not the row.
    3. catalog.go: Item gains `UpdatedAt = max(AddedAt, Book.MetaUpdatedAt, version AddedAt)`. absjson itemMinified and itemExpanded report `"updatedAt": it.UpdatedAt`, and [AUD-07](#aud-07)'s Card cover url uses `?v=<UpdatedAt>`.
    4. media.go handleCover: when the request carries `ts=` or `v=`, send `Cache-Control: public, max-age=31536000, immutable`; otherwise keep today's headers. The remote image cache is keyed by URL hash, so a changed remote cover URL is fetched fresh.
  - **Files:** `internal/store/migrations/00NN_books_meta_updated.sql`, `internal/books/repo.go`, `internal/books/service.go`, `internal/httpapi/books.go`, `internal/audioserver/catalog.go`, `internal/audioserver/absjson.go`, `internal/audioserver/media.go`, `internal/audioserver/player.go`, `internal/audioserver/server_test.go`
  - **Acceptance:**
    - After editing a book's title, series or cover (URL or upload) in Arrmada, GET /api/items/{key} returns a newer updatedAt.
    - A cover request with ?ts= or ?v= gets a one-year immutable cache header, and without them the headers are unchanged.
    - Owner check: Lissen shows the new cover after its next library refresh.
  - **Tests:** Go audioserver: TestItemUpdatedAtFollowsEdits (OverrideMetadata, then SetSeries, then TouchMeta: the item's updatedAt increases each time).; Go audioserver: TestCoverCacheHeaders (with and without ts).; Go books: TestMetaUpdatedTriggerFiresOnlyOnMetadataColumns (updating last_search_at doesn't bump it).
  - **Risk:** A changed updatedAt may make some apps re-download item metadata, which is cheap; downloaded audio isn't affected. Triggers are invisible in Go code, so add a comment in repo.go pointing at the migration.
  - **Resolves:** audiobooks-11
<a id="aud-17"></a>
- [ ] **AUD-17 · Narrators: read them from file tags, show them to apps and in Arrmada, and let them be edited** — `P3` · `M` · Phase 13
  - **Problem:** narratorName is always "" and narrators always [] (absjson.go:33, 54). /narrators returns nothing (handlers_compat.go:79-84), and filterdata and search return empty narrator lists (handlers_library.go:95, 481). parseProbe keeps only the title tag (probe.go:297), even though the narrator is often in the composer or album_artist tag. Listeners can't browse by narrator or tell editions apart, and Audiobookshelf shows narrators.
  - **Approach:** 1. New migration `audio_narrator`:
       - `audio_file_meta ADD narrator TEXT NOT NULL DEFAULT ''` and `ADD meta_version INTEGER NOT NULL DEFAULT 1`
       - `books ADD audiobook_narrator TEXT NOT NULL DEFAULT ''`
       - `book_audio_versions ADD narrator TEXT NOT NULL DEFAULT ''`
       Add both narrator columns to [AUD-16](#aud-16)'s metadata trigger if it has landed.
    2. probe.go parseProbe: take the narrator from the format tags in the order narrator, composer, then album_artist (only when it differs from artist and the book author, compared case-insensitively), then performer. Store it with meta_version=2. cached() still serves duration and chapters from v1 rows. Warm re-probes rows with meta_version < 2 once, paced as today: with [AUD-15](#aud-15), knownUnder also reports a stale count and treats such items as needing a probe.
    3. Item narrator: the manual field (version.narrator, or books.audiobook_narrator for the standard item) when set, otherwise the first file's probed narrator.
       - absjson: metadataMinified `narratorName` and metadataExpanded `narrators []string` (split on ', ' and ' & ').
       - handleNarrators lists distinct narrators with numBooks.
       - filterdata.narrators, search.narrators and applyFilter 'narrators.<base64>' include them.
    4. Books API and UI: extend the book metadata override endpoint (PUT /api/v1/books/{id}/metadata) with an optional narrator, and the audio-version update (PUT /api/v1/books/{id}/audio-versions/{vid}) likewise. BookDetail's audiobook edition card and the AudioVersions rows get an editable 'Narrator' field in the existing input style. [AUD-07](#aud-07)'s ItemDetail includes narrator, so the Listen tab can show it.
  - **Files:** `internal/store/migrations/00NN_audio_narrator.sql`, `internal/audioserver/probe.go`, `internal/audioserver/probe_test.go`, `internal/audioserver/testdata/ffprobe_m4b.json`, `internal/audioserver/absjson.go`, `internal/audioserver/catalog.go`, `internal/audioserver/handlers_compat.go`, `internal/audioserver/handlers_library.go`, `internal/audioserver/player.go`, `internal/books/repo.go`, `internal/books/service.go`, `internal/books/versions.go`, `internal/httpapi/books.go`, `web/src/pages/BookDetail.tsx`
  - **Acceptance:**
    - A file tagged with a narrator or composer shows that narrator in apps (item detail and lists) and in /narrators.
    - An album_artist equal to the author is not taken as the narrator.
    - A narrator typed on the Books page wins over the tag and shows in apps on their next fetch.
    - Two versions of a book show their own narrators.
  - **Tests:** Go audioserver: TestParseProbeNarratorTags (fixtures with a narrator tag, a composer tag, and album_artist equal to the author).; Go audioserver: TestNarratorInItemJSONAndNarratorsRoute, TestManualNarratorWins, TestNarratorFilter.; UI check: edit the narrator on BookDetail and on a version row, on desktop and at phone width.
  - **Depends on:** [AUD-15](#aud-15) (helpful for the one-time re-probe without walking; not strictly required)
  - **Risk:** The one-time re-probe reads every file's header once, which spins up disks once. It's paced and runs only while the server is on. BookDetail is also being regrouped by BOOK, so keep the field small and coordinate if both are in flight.
  - **Resolves:** audiobooks-11
<a id="aud-18"></a>
- [ ] **AUD-18 · Expose the ebook on the Audiobookshelf-compatible server for items that have one** — `P3` · `S` · Phase 13
  - **Problem:** The ABS-compatible server always sends `media.ebookFile: nil` and `ebookFormat: nil` (internal/audioserver/absjson.go:75, 127), with `audiobooksOnly: true` (absjson.go:215). A client that can read ebooks, such as the official Audiobookshelf app, can't open the EPUB of a book the family is also listening to. Ebooks otherwise reach devices only as a browser download (httpapi/mybooks.go:136-171).
  - **Approach:** 1. For items whose book has an ebook edition (Book.Ebook.Path set and the file present via BOOK's shared ebook-file helper, today's httpapi `ebookFile()` moved into internal/books):
       - fill `media.ebookFormat` (minified and expanded) and `media.ebookFile` in the exact shape recorded by [AUD-09](#aud-09)'s fixture (ino, metadata{filename, ext, path, relPath, size, mtimeMs, ctimeMs, birthtimeMs}, ebookFormat, addedAt, updatedAt)
       - add the ebook to `libraryFiles` with fileType 'ebook'
       The ino is inoFor('ebook/'+filename) so it never collides with audio inos. Apply it to the standard item and to versions of the same book.
    2. Serve the ebook behind the same token auth on `GET /api/items/{id}/ebook` and `GET /api/items/{id}/ebook/{fileid}` (ABS's routes). Also make `GET /api/items/{id}/file/{ino}[/download]` serve the ebook ino. Content types come from the shared ebookContentType: application/epub+zip, application/pdf and so on.
    3. Keep `audiobooksOnly: true` and don't add ebook-only items to the audio library, because Lissen and Plappa would list them as unplayable. An ebook-only library is a later, opt-in decision.
    4. Ebook reading progress (`ebookLocation`/`ebookProgress` in PATCH) is still ignored. patchProgress already treats a PATCH with no position fields as a no-op, which is verified by a test. Syncing it is out of scope.
    5. Logging follows SEC's route-pattern rule, so the ebook route never logs ids.
  - **Files:** `internal/audioserver/absjson.go`, `internal/audioserver/catalog.go`, `internal/audioserver/media.go`, `internal/audioserver/server.go`, `internal/audioserver/server_test.go`, `internal/audioserver/testdata/abs/allowed_diffs.txt`, `internal/books/ebookfile.go`
  - **Acceptance:**
    - In the official Audiobookshelf app, a book with both editions offers 'Read' and opens the EPUB (owner check on device).
    - Lissen behaves exactly as before (manual check), and TestLissenConversation passes.
    - An unauthenticated ebook request returns 401, and an item without an ebook returns 404 on the ebook route.
    - A PATCH carrying only ebookProgress leaves the audio place untouched.
  - **Tests:** Go audioserver: TestEbookFileMatchesAudiobookshelf (diffShape of media.ebookFile and libraryFiles[] against the recorded fixture; the ebookFile allowed_diffs line removed).; Go audioserver: TestEbookRouteServesWithToken (application/epub+zip with a valid token, 401 without, 404 when absent).; Go audioserver: TestEbookProgressPatchIsNoop.
  - **Depends on:** [AUD-09](#aud-09) (recorded ebookFile shape), BOOK (shared ebook-file helper moved to internal/books, draft books.t12), SEC (audiobook request-log redaction)
  - **Risk:** A client that parses ebookFile strictly could crash on a shape mismatch, so ship only with the fixture diff test. If the official app hides 'Read' while audiobooksOnly is true, take that to the owner rather than flipping the flag, because flipping it may change how Lissen lists the library.
  - **Resolves:** books-7

#### Risks

- Rules-engine changes stack up: AUD-01, 02, 03, 04 and 06 all touch internal/listening/rules.go and store.go. Land them in order, keep Decide pure, and add a rules_test case per new branch. Run `go test -race` in Docker before each push, because the store's mutex and read-decide-write path is exactly what race tests guard.
- Client behaviour is still partly unknown: how often apps send an empty close, whether Lissen seeks through its session sync or a PATCH, and whether iOS apps need socket.io or UUIDs. AUD-11 (trace) and AUD-12 (source reading plus device checks) exist to answer these before AUD-14 is committed to.
- Privacy regressions are the easiest mistake. New routes (the web player, ebook) and new logging (trace) must log route patterns only. The timeline and offers are scoped to the signed-in user, and no admin route may read listen_history, listen_progress or titles. listen_log must never gain a book column.
- Migration-number collisions with other epics working in parallel. Assign numbers at implementation time and rebase before pushing.
- Fixture hygiene: the capture tool must scrub tokens, cookies, hosts and usernames, the owner reviews the diff before committing, and it is never pointed at a real Audiobookshelf.
- UUID ids (AUD-14) could orphan downloads or mix id styles in a client cache if one JSON builder is missed. The default stays legacy, the switch is per device, a test scans every reply for legacy ids, and the owner must sign off.
- Holding more reports (PATCH holds, forward holds) means more banners. The copy must stay calm and 'Use this spot' must always work; otherwise people will learn to tap through and defeat the guards.
- Web-player streaming for remote requesters runs over the Cloudflare tunnel on the main port, which costs bandwidth there.

#### Out of scope

- The Listen tab UI, persistent mini-player, Media Session lock-screen controls, PWA install and the Apps & devices checklist (APP epic; AUD provides the API and the verified-app list).
- Redacting the existing audiobook request log itself (SEC epic, finding audiobooks-1).
- Making the audiobook merge safe: output duration check and recycling sources (BOOK epic, finding audiobooks-9).
- OPDS catalogue, Send-to-Kindle and the MyBooks shelf redesign (the rest of books-7; BOOK epic).
- Targeting Plappa specifically (closed source; the owner said not to depend on it). It may benefit from AUD-10 and AUD-14 for free.
- A socket.io server for ABS clients, unless AUD-12 proves a target iOS app can't work without it, in which case the owner decides.
- Syncing ebook reading progress (ebookLocation/ebookProgress) and an ebook-only library on the ABS server.
- Podcasts, collections and playlists on the ABS server (they stay empty, correctly shaped replies).
- Converting already signed-in Lissen devices to UUID ids.

