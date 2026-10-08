# Finding → task traceability

_Every audit finding (2026-10-08) and UI walkthrough note, and the roadmap task(s) that resolve it. Back to the [roadmap](../../ROADMAP.md)._

| Finding | Severity | Title | Task(s) |
|---|---|---|---|
| audiobooks-1 | medium | The request log shows what people listen to, and managers can read it in the Logs page | [SEC-04](SEC.md#sec-04) |
| audiobooks-2 | medium | PATCH /api/me/progress skips every sync guard: it counts as "Manual" and always wins | [AUD-03](AUD.md#aud-03) |
| audiobooks-3 | medium | Closing a session with no body (or syncing without currentTime) counts as a jump to 0:00 | [AUD-01](AUD.md#aud-01) |
| audiobooks-4 | medium | Finished books restart at their last seconds, so the "Listen Again" shelf doesn't work | [AUD-02](AUD.md#aud-02) |
| audiobooks-5 | medium | Rejected uploads and discarded places leave no trace, so "you can always put it back" isn't true | [AUD-04](AUD.md#aud-04), [AUD-05](AUD.md#aud-05) |
| audiobooks-6 | medium | Compatibility checks only test that keys exist; Plappa fails silently and the ids aren't UUID-shaped | [AUD-09](AUD.md#aud-09), [AUD-10](AUD.md#aud-10), [AUD-11](AUD.md#aud-11), [AUD-12](AUD.md#aud-12), [AUD-13](AUD.md#aud-13), [AUD-14](AUD.md#aud-14) |
| audiobooks-7 | high | /audiobooks is a settings page, not somewhere to listen, and it offers iPhone users nothing | [APP-09](APP.md#app-09), [APP-10](APP.md#app-10), [APP-11](APP.md#app-11), [APP-12](APP.md#app-12), [AUD-07](AUD.md#aud-07), [AUD-08](AUD.md#aud-08), [AUD-12](AUD.md#aud-12) |
| audiobooks-8 | low | The 30-minute warm-up re-walks and stats every audiobook file, which on Unraid can keep array disks from spinning down | [AUD-15](AUD.md#aud-15) |
| audiobooks-9 | medium | Audiobook merge deletes the source files without checking the output, and pulls files out from under listeners | [BOOK-26](BOOK.md#book-26), [SAFE-06](SAFE.md#safe-06) |
| audiobooks-10 | low | Forward jumps are trusted without limit: one bad report can mark a book finished | [AUD-06](AUD.md#aud-06) |
| audiobooks-11 | low | Apps never see cover or metadata edits, and narrators are never shown | [AUD-16](AUD.md#aud-16), [AUD-17](AUD.md#aud-17) |
| backend-1 | medium | Any signed-in account can make the server walk and list any directory on the host | [SEC-01](SEC.md#sec-01), [SEC-12](SEC.md#sec-12) |
| backend-2 | medium | API permissions don't match the product: about 80 staff routes are open to requester/readonly users on the LAN | [SEC-02](SEC.md#sec-02), [SEC-10](SEC.md#sec-10) |
| backend-3 | low | Requester-level accounts can make the server send notification requests to internal hosts (SSRF) | [SEC-13](SEC.md#sec-13) |
| backend-4 | medium | Realtime websocket sends every event, including who is watching what on Plex, to every signed-in user | [FE-07](FE.md#fe-07), [FE-26](FE.md#fe-26), [FE-27](FE.md#fe-27), [SEC-03](SEC.md#sec-03) |
| backend-5 | high | No database backups and no snapshot before the 89 automatic migrations | [BE-01](BE.md#be-01), [SAFE-01](SAFE.md#safe-01), [SAFE-11](SAFE.md#safe-11), [SAFE-12](SAFE.md#safe-12), [SAFE-13](SAFE.md#safe-13), [SAFE-14](SAFE.md#safe-14) |
| backend-6 | medium | Movie import completion travels over a bus that drops messages, so a finished import can stay 'Wanted' forever | [BE-03](BE.md#be-03), [BE-06](BE.md#be-06) |
| backend-7 | medium | Login throttling trusts a spoofable X-Forwarded-For header and lets anyone lock the admin out | [SEC-11](SEC.md#sec-11) |
| backend-8 | medium | The recycle bin quietly turns into permanent delete, and its 50 GB default cap purges a single 4K remux within an hour | [SAFE-07](SAFE.md#safe-07), [SAFE-08](SAFE.md#safe-08), [SAFE-16](SAFE.md#safe-16), [SAFE-17](SAFE.md#safe-17) |
| backend-9 | medium | Background work is untracked goroutines: one panic restarts the app, and 'Search now' reports nothing | [ACQ-15](ACQ.md#acq-15), [ACQ-19](ACQ.md#acq-19), [BE-02](BE.md#be-02), [BE-07](BE.md#be-07), [BE-08](BE.md#be-08), [BE-09](BE.md#be-09), [BE-10](BE.md#be-10), [OBS-03](OBS.md#obs-03), [OBS-06](OBS.md#obs-06), [OBS-20](OBS.md#obs-20) |
| backend-10 | low | SQLite: deferred read-then-write transactions hit SQLITE_BUSY, and database errors fail open in safety checks | [BE-04](BE.md#be-04), [BE-05](BE.md#be-05) |
| backend-11 | medium | Startup book de-duplication deletes audiobook versions and orphans everyone's listening progress | [BOOK-01](BOOK.md#book-01), [BOOK-10](BOOK.md#book-10), [BOOK-11](BOOK.md#book-11), [BOOK-12](BOOK.md#book-12) |
| backend-12 | low | When the download client is down, the app shows 'nothing downloading' and keeps grabbing | [ACQ-22](ACQ.md#acq-22), [ACQ-23](ACQ.md#acq-23) |
| backend-13 | medium | Session expiry or revocation leaves the app broken until a manual reload; sessions hard-expire every 30 days | [FE-02](FE.md#fe-02), [SEC-14](SEC.md#sec-14) |
| backend-14 | medium | No Plex library refresh after import, upgrade, rename, delete or convert | [PLEX-04](PLEX.md#plex-04), [PLEX-05](PLEX.md#plex-05), [REQ-15](REQ.md#req-15) |
| backend-15 | medium | Download identity rests on fuzzy title matching in three separately maintained normalizers | [ACQ-21](ACQ.md#acq-21), [ACQ-24](ACQ.md#acq-24), [ACQ-25](ACQ.md#acq-25) |
| backend-16 | low | Critical file paths (import replacement, delete, upgrade) and route authorization have almost no tests | [BE-03](BE.md#be-03), [BE-11](BE.md#be-11), [BE-12](BE.md#be-12), [SAFE-07](SAFE.md#safe-07), [SEC-02](SEC.md#sec-02) |
| backend-17 | low | ARRMADA_BASE_URL is only half implemented, so setting it gives a blank app | [CFG-08](CFG.md#cfg-08) |
| books-1 | high | Requests are linked by catalogue key, so any re-key cuts them off permanently: no 'ready' notification, never 'Available', stuck in 'Searching' | [BOOK-03](BOOK.md#book-03), [BOOK-09](BOOK.md#book-09) |
| books-2 | high | Duplicate key drops everything after ':' / ' (' / ' - ', so same-author books sharing a prefix count as one: can't add, shown 'In library', merged automatically | [BOOK-01](BOOK.md#book-01), [BOOK-02](BOOK.md#book-02), [BOOK-11](BOOK.md#book-11), [BOOK-12](BOOK.md#book-12) |
| books-3 | high | A wanted book gets two automatic searches and is then dropped; MAM has no RSS path; the UI still says 'Arrmada is searching' | [BOOK-04](BOOK.md#book-04), [BOOK-07](BOOK.md#book-07), [BOOK-08](BOOK.md#book-08), [BOOK-18](BOOK.md#book-18) |
| books-4 | medium | The book RSS matcher is still the substring logic the search path was fixed to avoid ('It' matches 'The Institute', 'Dune' matches 'Dune Messiah') | [BOOK-05](BOOK.md#book-05) |
| books-5 | medium | Release-to-book matching drops non-ASCII characters and splits apostrophes, so accent or apostrophe differences mean a book never matches | [BOOK-06](BOOK.md#book-06), [BOOK-15](BOOK.md#book-15) |
| books-6 | medium | Requesters can't choose ebook or audiobook, and once one format exists they can't request the other | [BOOK-13](BOOK.md#book-13), [BOOK-14](BOOK.md#book-14) |
| books-7 | medium | Ebooks only reach devices as a browser download: no OPDS, no Send-to-Kindle, and the Audiobookshelf-compatible server is audio-only | [APP-19](APP.md#app-19), [AUD-18](AUD.md#aud-18), [BOOK-22](BOOK.md#book-22), [BOOK-23](BOOK.md#book-23), [BOOK-24](BOOK.md#book-24) |
| books-8 | medium | No author monitoring or new-release detection; authors aren't entities and the author page guesses which author is meant by name | [BOOK-19](BOOK.md#book-19), [BOOK-20](BOOK.md#book-20), [BOOK-21](BOOK.md#book-21) |
| books-9 | medium | No scheduled metadata refresh: books from 'Add author' or 'Add missing in series' stay without descriptions, genres or series | [BOOK-17](BOOK.md#book-17) |
| books-10 | medium | Requester accounts can walk any server directory through manual-import listing, and can trigger all-indexer searches | [SEC-01](SEC.md#sec-01), [SEC-02](SEC.md#sec-02) |
| books-11 | low | Book page links every book to Open Library, including Hardcover and Google Books keys; copy still says Open Library | [COPY-11](COPY.md#copy-11), [COPY-15](COPY.md#copy-15) |
| books-12 | medium | Admin Books screens are a wall of buttons: up to 9 header controls that don't wrap, 9 toolbar actions on the detail page, hover-only card actions | [BOOK-25](BOOK.md#book-25) |
| books-13 | medium | No language preference: MAM language codes are ignored when picking an automatic grab | [BOOK-16](BOOK.md#book-16) |
| books-14 | low | Books Discover skips the always-on adult filter used on every other Discover surface | [SEC-05](SEC.md#sec-05) |
| convert-1 | high | Transient failures retry in a hot loop with no backoff (scratch full, library disk full) | [CONV-01](CONV.md#conv-01), [CONV-03](CONV.md#conv-03) |
| convert-2 | high | The 'recycle bin' safety net deletes 4K originals within the hour, and a conversion can't be undone | [CONV-02](CONV.md#conv-02), [CONV-06](CONV.md#conv-06), [CONV-08](CONV.md#conv-08), [CONV-09](CONV.md#conv-09), [CONV-10](CONV.md#conv-10) |
| convert-3 | medium | Quality upgrades can undo conversions: the shrunken file looks like a bitrate downgrade | [CONV-20](CONV.md#conv-20) |
| convert-4 | medium | Dolby Vision is removed without the UI ever saying so, and the headline copy overclaims | [CONV-05](CONV.md#conv-05), [CONV-12](CONV.md#conv-12), [CONV-15](CONV.md#conv-15), [CONV-29](CONV.md#conv-29) |
| convert-5 | medium | No per-file plan before converting and no record of what was done after | [CONV-06](CONV.md#conv-06), [CONV-11](CONV.md#conv-11), [CONV-12](CONV.md#conv-12), [CONV-13](CONV.md#conv-13) |
| convert-6 | medium | 'Convert now' waits behind a frozen auto job despite promising it starts right away | [CONV-14](CONV.md#conv-14) |
| convert-7 | medium | Multi-night CPU encodes lose all progress on any restart, and there is no time forecast | [CFG-07](CFG.md#cfg-07), [CONV-22](CONV.md#conv-22), [CONV-27](CONV.md#conv-27), [CONV-28](CONV.md#conv-28) |
| convert-8 | medium | Quality gate is weaker than 'looks the same': mean SSIM 0.97 over 60 seconds, no full-file integrity check | [CONV-05](CONV.md#conv-05), [CONV-17](CONV.md#conv-17), [CONV-19](CONV.md#conv-19) |
| convert-9 | medium | A forced-only .srt sidecar causes the full PGS track to be dropped, and forced image subs are dropped too | [CONV-04](CONV.md#conv-04) |
| convert-10 | medium | Non-default movie versions are invisible to Convert (and to the ideal-file check) | [CONV-24](CONV.md#conv-24) |
| convert-11 | medium | Plex isn't told about swapped files, and Convert is silent outside its own page | [CONV-21](CONV.md#conv-21), [OBS-18](OBS.md#obs-18), [PLEX-04](PLEX.md#plex-04), [PLEX-05](PLEX.md#plex-05) |
| convert-12 | medium | Device compatibility is guesswork even though Insights already knows what each player direct-plays | [CONV-05](CONV.md#conv-05), [CONV-25](CONV.md#conv-25), [CONV-26](CONV.md#conv-26) |
| convert-13 | low | Heavy side steps and the final swap bypass the 'gentle and careful' guarantees | [CONV-07](CONV.md#conv-07), [CONV-18](CONV.md#conv-18) |
| convert-14 | low | Black-bar crop is on by default for ~2% savings, and can permanently cut picture from brief open-matte shots | [CONV-05](CONV.md#conv-05), [CONV-16](CONV.md#conv-16) |
| convert-15 | low | Smaller polish gaps: hidden settings, misleading indicators, stale docs | [CONV-05](CONV.md#conv-05), [CONV-09](CONV.md#conv-09), [CONV-23](CONV.md#conv-23) |
| discover-1 | high | No request management page, and admins are never told a request is waiting | [REQ-03](REQ.md#req-03), [REQ-05](REQ.md#req-05), [REQ-07](REQ.md#req-07) |
| discover-2 | high | Series requests grab every season; Plex sign-ins auto-approve by default; no quotas; request is one hover click | [REQ-09](REQ.md#req-09), [REQ-10](REQ.md#req-10), [REQ-11](REQ.md#req-11), [REQ-12](REQ.md#req-12), [REQ-13](REQ.md#req-13), [REQ-14](REQ.md#req-14) |
| discover-3 | low | Subscribers ('You're on the list') never see the request they joined | [REQ-03](REQ.md#req-03), [REQ-06](REQ.md#req-06) |
| discover-4 | medium | Nothing in Discover has a URL: Back exits the app, titles can't be shared, and notifications open a search | [APP-06](APP.md#app-06), [APP-07](APP.md#app-07), [APP-08](APP.md#app-08) |
| discover-5 | medium | Requesters reach 'In library' and stop: no Watch on Plex, no Recently Added | [PLEX-10](PLEX.md#plex-10), [PLEX-11](PLEX.md#plex-11), [REQ-16](REQ.md#req-16), [REQ-18](REQ.md#req-18) |
| discover-6 | medium | Shallow discovery: no 'See all', one-page genre and search results, people dropped, cast not clickable, collections flattened | [REQ-19](REQ.md#req-19), [REQ-20](REQ.md#req-20), [REQ-21](REQ.md#req-21) |
| discover-7 | high | Requester PWA shell breaks on phones; the bell only exists on Discover; invisible approve buttons can be tapped | [APP-01](APP.md#app-01), [APP-02](APP.md#app-02), [APP-05](APP.md#app-05) |
| discover-8 | medium | Notification spam on auto-approve and Overseerr import; unthrottled import searches; 'Pending' shown for auto-approved requests | [REQ-01](REQ.md#req-01) |
| discover-9 | low | Every open Discover tab runs a heavy requests rebuild every 8 seconds | [REQ-03](REQ.md#req-03), [REQ-06](REQ.md#req-06), [REQ-17](REQ.md#req-17) |
| discover-10 | low | Decisions without context: no decline reason, no requester note, no approve options in the UI | [REQ-08](REQ.md#req-08) |
| discover-11 | low | Calendar is a bare desktop month grid: unreadable on phones, info hidden in tooltips, dead-end items | [APP-16](APP.md#app-16), [APP-17](APP.md#app-17), [APP-18](APP.md#app-18) |
| discover-12 | low | Request card says 'Partly ready' forever while the notification says 'ready' | [REQ-02](REQ.md#req-02) |
| discover-13 | low | Notification settings: two 'Push notifications' sections, and Apprise URLs asked of family members | [APP-13](APP.md#app-13), [APP-15](APP.md#app-15) |
| discover-14 | low | Detail sheet makes uncached TMDB and OMDb calls on every open | [INT-13](INT.md#int-13) |
| frontend-1 | medium | Two design tokens used 75 times are never defined: every 'Wanted'/'Pending' chip has no fill and modals have no shadow | [FE-01](FE.md#fe-01) |
| frontend-2 | medium | No design system: every page re-implements modals, toasts, buttons, pills, tabs, formatters | [FE-08](FE.md#fe-08), [FE-09](FE.md#fe-09), [FE-10](FE.md#fe-10), [FE-11](FE.md#fe-11), [FE-12](FE.md#fe-12), [FE-13](FE.md#fe-13), [FE-14](FE.md#fe-14), [FE-15](FE.md#fe-15), [FE-23](FE.md#fe-23), [FE-30](FE.md#fe-30), [FE-31](FE.md#fe-31), [FE-32](FE.md#fe-32) |
| frontend-3 | medium | Scroll position leaks between pages and is never restored; library filters reset on every visit | [FE-21](FE.md#fe-21), [FE-24](FE.md#fe-24) |
| frontend-4 | high | Invisible-but-tappable buttons on touch: one tap can Request, Approve or Decline without the user seeing a button | [APP-01](APP.md#app-01), [REQ-04](REQ.md#req-04), [REQ-06](REQ.md#req-06) |
| frontend-5 | high | The requester PWA (what family installs) has no real mobile layout | [APP-02](APP.md#app-02), [APP-03](APP.md#app-03), [APP-05](APP.md#app-05), [APP-06](APP.md#app-06), [APP-16](APP.md#app-16), [FE-10](FE.md#fe-10) |
| frontend-6 | medium | Nothing is addressable: tabs, sub-views and title details live in component state, not the URL | [APP-07](APP.md#app-07), [APP-08](APP.md#app-08), [FE-23](FE.md#fe-23), [FE-25](FE.md#fe-25) |
| frontend-7 | medium | One 915 KB JS bundle, served uncompressed to everyone, including requesters on phones | [FE-04](FE.md#fe-04), [FE-06](FE.md#fe-06) |
| frontend-8 | medium | ARRMADA_BASE_URL (reverse-proxy sub-path) produces a blank page; the frontend hard-codes root paths | [FE-29](FE.md#fe-29) |
| frontend-9 | medium | Misleading loading, empty and error states; no error boundary; expired sessions aren't handled | [FE-02](FE.md#fe-02), [FE-03](FE.md#fe-03), [FE-13](FE.md#fe-13), [FE-21](FE.md#fe-21), [FE-22](FE.md#fe-22) |
| frontend-10 | high | Navigation groups don't reflect the product; integrations are scattered across six places; no admin 'needs attention' view | [ACQ-27](ACQ.md#acq-27), [CFG-13](CFG.md#cfg-13), [CFG-15](CFG.md#cfg-15), [CFG-16](CFG.md#cfg-16), [CFG-17](CFG.md#cfg-17), [COPY-03](COPY.md#copy-03), [COPY-13](COPY.md#copy-13), [FE-16](FE.md#fe-16), [FE-17](FE.md#fe-17), [INT-04](INT.md#int-04), [INT-05](INT.md#int-05), [INT-15](INT.md#int-15), [INT-16](INT.md#int-16), [INT-17](INT.md#int-17), [OBS-04](OBS.md#obs-04), [OBS-07](OBS.md#obs-07), [OBS-08](OBS.md#obs-08), [REQ-05](REQ.md#req-05) |
| frontend-11 | medium | Accessibility: the most-used text colour fails contrast; most form labels and dialogs aren't wired up | [FE-01](FE.md#fe-01), [FE-08](FE.md#fe-08), [FE-09](FE.md#fe-09), [FE-11](FE.md#fe-11), [FE-12](FE.md#fe-12), [FE-13](FE.md#fe-13), [FE-14](FE.md#fe-14), [FE-15](FE.md#fe-15), [FE-30](FE.md#fe-30) |
| frontend-12 | medium | Leaving the Quality editor mid-edit discards a half-built profile; Settings has no unsaved-changes guard | [CFG-14](CFG.md#cfg-14), [FE-22](FE.md#fe-22), [FE-25](FE.md#fe-25) |
| frontend-13 | low | Stale and contradictory copy in visible places (404 page, TMDB banner, 'Activity', Music 'roadmap') | [COPY-02](COPY.md#copy-02), [COPY-03](COPY.md#copy-03) |
| frontend-14 | low | The app polls on ~25 timers while the realtime websocket goes mostly unused | [BE-13](BE.md#be-13), [FE-07](FE.md#fe-07), [FE-26](FE.md#fe-26), [FE-27](FE.md#fe-27) |
| frontend-15 | low | Theme toggle isn't remembered and requesters can't switch themes | [FE-05](FE.md#fe-05) |
| insights-1 | medium | History 'Watched' column still uses wall-clock time, ignoring the watched_ms fix | [PLEX-01](PLEX.md#plex-01) |
| insights-2 | medium | Tautulli import double-counts every play that Arrmada also recorded live | [PLEX-02](PLEX.md#plex-02) |
| insights-3 | medium | Monitoring is off by default, sign-in doesn't turn it on, and the UI says everything is connected | [PLEX-07](PLEX.md#plex-07), [PLEX-08](PLEX.md#plex-08) |
| insights-4 | medium | Every restart splits streams in progress into two plays and sends 'Now playing' again | [PLEX-03](PLEX.md#plex-03) |
| insights-5 | high | Arrmada never asks Plex to scan after an import, so 'ready to watch' can arrive before Plex has the title | [PLEX-04](PLEX.md#plex-04), [PLEX-05](PLEX.md#plex-05), [REQ-15](REQ.md#req-15) |
| insights-6 | high | Admin alerts cover only four events, with no 'new request pending' and no books, audiobooks, music or failures | [OBS-11](OBS.md#obs-11), [OBS-12](OBS.md#obs-12), [OBS-14](OBS.md#obs-14) |
| insights-7 | medium | Notification setup is raw Apprise URLs with no presets, templates, delivery log or spam control, and it lives inside Insights | [OBS-10](OBS.md#obs-10), [OBS-13](OBS.md#obs-13), [OBS-16](OBS.md#obs-16), [OBS-17](OBS.md#obs-17), [OBS-19](OBS.md#obs-19), [SEC-08](SEC.md#sec-08) |
| insights-8 | medium | Sign in with Plex opens its popup after an await, with no blocked-popup handling and no redirect fallback | [PLEX-06](PLEX.md#plex-06) |
| insights-9 | medium | HW transcode badge and buffer-cause diagnosis use 'HW requested', not 'HW in use' | [PLEX-09](PLEX.md#plex-09) |
| insights-10 | medium | Insights copies Tautulli's tables without its navigation: no user pages, avatars, posters or drill-downs | [PLEX-18](PLEX.md#plex-18), [PLEX-20](PLEX.md#plex-20), [PLEX-21](PLEX.md#plex-21), [PLEX-22](PLEX.md#plex-22), [PLEX-23](PLEX.md#plex-23), [PLEX-24](PLEX.md#plex-24) |
| insights-11 | low | Finished tabs say 'Coming soon', and history from the database is hidden whenever Plex isn't reachable | [COPY-08](COPY.md#copy-08), [PLEX-19](PLEX.md#plex-19) |
| insights-12 | medium | Insights layout overflows sideways on a phone | [PLEX-18](PLEX.md#plex-18) |
| insights-13 | medium | Tautulli import runs blind, and Plex settings are split across three places | [PLEX-16](PLEX.md#plex-16), [PLEX-17](PLEX.md#plex-17) |
| insights-14 | medium | Local accounts can't link Plex, and the owner signing in with Plex gets a second, requester account | [PLEX-14](PLEX.md#plex-14), [PLEX-15](PLEX.md#plex-15) |
| insights-15 | low | Automatic server discovery can pick a server you don't own, and the URL isn't tested | [PLEX-07](PLEX.md#plex-07) |
| integrations-1 | high | Indexer failures never reach the UI and look like 'no results'; the automatic sweep even counts an outage as a miss | [ACQ-02](ACQ.md#acq-02), [ACQ-14](ACQ.md#acq-14), [INT-01](INT.md#int-01), [INT-02](INT.md#int-02), [INT-14](INT.md#int-14), [INT-15](INT.md#int-15), [OBS-15](OBS.md#obs-15) |
| integrations-2 | medium | Prowlarr re-sync wipes your scoping and re-enables disabled indexers, and never removes stale ones or runs on its own | [INT-07](INT.md#int-07), [INT-19](INT.md#int-19) |
| integrations-3 | medium | Download clients can't be edited or disabled, the first one always wins, and one dead client silently turns off stall fail-over | [INT-05](INT.md#int-05), [INT-11](INT.md#int-11), [INT-14](INT.md#int-14), [OBS-05](OBS.md#obs-05) |
| integrations-4 | low | Newznab/usenet is advertised but nothing can download it, and Prowlarr sync imports usenet indexers that waste every search | [INT-07](INT.md#int-07), [INT-12](INT.md#int-12) |
| integrations-5 | medium | Torznab 'Test' says Connected for any HTTP 200, including a web page from the wrong URL | [INT-09](INT.md#int-09) |
| integrations-6 | medium | Download client 'Category' field silently breaks movie imports | [INT-10](INT.md#int-10) |
| integrations-7 | medium | Any signed-in account can run tracker searches and read download links that carry the Prowlarr API key | [SEC-02](SEC.md#sec-02), [SEC-07](SEC.md#sec-07) |
| integrations-8 | medium | Native TorrentLeech: a stale session is never dropped on Cloudflare errors, failed logins aren't backed off, the RSS key is ignored, and there's no RSS | [INT-08](INT.md#int-08), [INT-20](INT.md#int-20) |
| integrations-9 | low | All Prowlarr indexers share one 1-request-per-second queue, and searches you start yourself wait behind background sweeps | [INT-01](INT.md#int-01), [INT-21](INT.md#int-21) |
| integrations-10 | medium | No ID-based searches, no categories, and capabilities are never read: searches match on title only | [INT-09](INT.md#int-09), [INT-22](INT.md#int-22), [INT-23](INT.md#int-23) |
| integrations-11 | low | Doesn't work with an existing qBittorrent mounted at different paths: no path mapping, and Test doesn't check paths | [INT-25](INT.md#int-25) |
| integrations-12 | medium | Indexers and Download clients pages are bare forms with traps: name doesn't follow kind, no test-before-save, unconfirmed deletes, inverted scope pills | [INT-04](INT.md#int-04), [INT-09](INT.md#int-09), [INT-15](INT.md#int-15), [INT-16](INT.md#int-16), [INT-17](INT.md#int-17), [INT-18](INT.md#int-18) |
| integrations-13 | low | FlareSolverr is an invisible dependency: environment-only, no status, no test, and error messages point to a setting that doesn't exist | [INT-03](INT.md#int-03), [INT-14](INT.md#int-14), [OBS-15](OBS.md#obs-15) |
| integrations-14 | low | Only the Hardcover key is testable (TMDB, the required one, isn't), and OMDb ratings are uncached and fail silently | [INT-06](INT.md#int-06), [INT-13](INT.md#int-13) |
| integrations-15 | low | The 'Open Prowlarr' link hard-codes port 9696, but the installer may put the bundled Prowlarr on 9697 or 9698 | [INT-19](INT.md#int-19) |
| movies-1 | high | Background sweeps run ffprobe on every movie file in the library every 5 minutes, and detail pages run it twice per load | [MOV-01](MOV.md#mov-01), [MOV-02](MOV.md#mov-02) |
| movies-2 | medium | Versions: the Edition field is never used, imports ignore which track a grab was for, and same-resolution tracks overwrite each other | [MOV-12](MOV.md#mov-12), [MOV-13](MOV.md#mov-13), [MOV-14](MOV.md#mov-14) |
| movies-3 | medium | Requester and read-only accounts can run indexer searches (and see download links) and list video files anywhere on the server | [SEC-01](SEC.md#sec-01), [SEC-02](SEC.md#sec-02), [SEC-07](SEC.md#sec-07) |
| movies-4 | medium | Bulk 'Set quality profile' starts one indexer search per selected movie at the same time, and failures are silent | [MOV-07](MOV.md#mov-07), [MOV-19](MOV.md#mov-19) |
| movies-5 | medium | No Wanted or Cutoff-unmet view, and searches report nothing back | [MOV-04](MOV.md#mov-04), [MOV-07](MOV.md#mov-07), [MOV-08](MOV.md#mov-08) |
| movies-6 | medium | Untrue or stale copy: upgrade promise on unmonitored films, env-var TMDB banner, 'Activity', recycle bin | [COPY-02](COPY.md#copy-02), [COPY-03](COPY.md#copy-03), [COPY-06](COPY.md#copy-06), [MOV-03](MOV.md#mov-03), [MOV-05](MOV.md#mov-05), [SAFE-02](SAFE.md#safe-02), [SAFE-09](SAFE.md#safe-09) |
| movies-7 | medium | No way to delete a movie on touch devices, and version files delete with a single click | [MOV-14](MOV.md#mov-14), [MOV-18](MOV.md#mov-18), [SAFE-09](SAFE.md#safe-09) |
| movies-8 | medium | Deleting a movie mid-download leaves the download running, and its file later appears in the library untracked | [SAFE-10](SAFE.md#safe-10) |
| movies-9 | medium | Plex is never told about imports, upgrades, renames or deletes, and the movie page has no Plex link or watch status | [MOV-15](MOV.md#mov-15), [PLEX-04](PLEX.md#plex-04), [PLEX-05](PLEX.md#plex-05), [PLEX-10](PLEX.md#plex-10), [PLEX-11](PLEX.md#plex-11), [PLEX-12](PLEX.md#plex-12), [REQ-15](REQ.md#req-15) |
| movies-10 | low | Interactive search lists usenet releases that can't be downloaded and can mark one 'Recommended' | [MOV-11](MOV.md#mov-11) |
| movies-11 | medium | Searching and matching use only the TMDB primary title as text, with no IMDb-ID search and no alternate titles | [INT-22](INT.md#int-22), [INT-24](INT.md#int-24), [MOV-24](MOV.md#mov-24) |
| movies-12 | medium | The library grid refetches the whole library every 4 seconds and still misses downloads it didn't start | [MOV-09](MOV.md#mov-09), [MOV-10](MOV.md#mov-10) |
| movies-13 | medium | Importing an existing library: movies already added are skipped instead of having their files attached, and every import is unmonitored with no profile | [MOV-06](MOV.md#mov-06), [MOV-20](MOV.md#mov-20) |
| movies-14 | medium | Library management is thin next to Radarr: rename runs without a preview, no organize step, limited bulk actions, a bare add flow and basic sorting | [MOV-16](MOV.md#mov-16), [MOV-17](MOV.md#mov-17), [MOV-18](MOV.md#mov-18), [MOV-19](MOV.md#mov-19), [MOV-22](MOV.md#mov-22), [MOV-23](MOV.md#mov-23) |
| music-1 | high | Missing-album sweep has no search backoff; it searches every indexer for every incomplete album every 30 minutes, forever | [MUS-02](MUS.md#mus-02) |
| music-2 | high | Release matching only compares plain a-z/0-9 text: accented, apostrophe and '&' names never match | [MUS-04](MUS.md#mus-04) |
| music-3 | medium | Quality detection is plain substring matching: 'Mixtape', 'Escape', 'Palace' and 'Wave' are read as lossless | [MUS-05](MUS.md#mus-05), [MUS-11](MUS.md#mus-11) |
| music-4 | medium | Track-number parser eats song titles that start with a number ('99 Problems', '7 Rings', '22') | [MUS-06](MUS.md#mus-06) |
| music-5 | medium | Partly imported albums seed forever, and the next grab can downgrade or duplicate tracks already on disk | [MUS-08](MUS.md#mus-08), [MUS-11](MUS.md#mus-11), [MUS-19](MUS.md#mus-19) |
| music-6 | medium | Discography grabs store the artist ID in the album-ID column, so stall, blocklist and seeding logic act on an unrelated album | [MUS-07](MUS.md#mus-07) |
| music-7 | medium | Upgrades are advertised in three places but no music upgrade code exists | [COPY-04](COPY.md#copy-04), [MUS-16](MUS.md#mus-16) |
| music-8 | high | Monitored artists never pick up new releases; Add artist monitors the whole catalogue and offers no profile or monitor options | [MUS-13](MUS.md#mus-13), [MUS-14](MUS.md#mus-14) |
| music-9 | high | No per-album Search or Interactive search, and the reasons an album isn't downloading only appear in the server log | [MUS-09](MUS.md#mus-09), [MUS-17](MUS.md#mus-17), [MUS-18](MUS.md#mus-18) |
| music-10 | medium | Completeness stats count only albums whose track listing has been fetched, so artists show 'Complete' when most albums are missing | [MUS-10](MUS.md#mus-10) |
| music-11 | medium | Review page: 'Import into a different movie…' on a held music download sends a movie ID as the album ID | [ACQ-01](ACQ.md#acq-01) |
| music-12 | medium | In the nav by default while labelled 'planned', and the off switch only hides the nav entry | [MUS-01](MUS.md#mus-01), [MUS-12](MUS.md#mus-12) |
| music-13 | medium | Bare text-only library with no artwork, bio or sorting, far from the Plex feel the rest of the app aims for | [MUS-22](MUS.md#mus-22), [MUS-23](MUS.md#mus-23) |
| music-14 | medium | Library scan reports nothing back, misreads multi-disc folders, and deleted files are never noticed | [MUS-15](MUS.md#mus-15), [MUS-20](MUS.md#mus-20) |
| music-15 | low | Album searches aren't restricted to audio categories and ignore size, so concert videos and fakes pass the gate | [MUS-21](MUS.md#mus-21) |
| ops-1 | high | "Import into a different…" lists MOVIES for book and music reviews, then imports into whichever book or album happens to share that numeric id | [ACQ-01](ACQ.md#acq-01) |
| ops-2 | medium | "Resume all" or a per-torrent Resume silently defeats the disk guard, and the disk fills anyway | [ACQ-03](ACQ.md#acq-03) |
| ops-3 | high | Stuck downloads are invisible: dead torrents read "downloading", stall fail-over is off by default, and a dead TV torrent freezes the show's searches | [ACQ-04](ACQ.md#acq-04), [ACQ-05](ACQ.md#acq-05), [ACQ-06](ACQ.md#acq-06), [ACQ-07](ACQ.md#acq-07), [ACQ-08](ACQ.md#acq-08), [ACQ-18](ACQ.md#acq-18) |
| ops-4 | high | Nothing tells the owner that something needs them: no Review badge, no admin alert, no "needs you" on the Dashboard, and stale warnings | [ACQ-12](ACQ.md#acq-12), [ACQ-26](ACQ.md#acq-26), [ACQ-30](ACQ.md#acq-30), [OBS-02](OBS.md#obs-02), [OBS-07](OBS.md#obs-07), [OBS-08](OBS.md#obs-08), [OBS-09](OBS.md#obs-09), [OBS-11](OBS.md#obs-11), [OBS-12](OBS.md#obs-12) |
| ops-5 | medium | Finished-but-not-imported downloads have no state, and Delete wipes the data with one click | [ACQ-09](ACQ.md#acq-09), [ACQ-28](ACQ.md#acq-28), [ACQ-30](ACQ.md#acq-30), [SAFE-04](SAFE.md#safe-04) |
| ops-6 | medium | Series downloads resolved through Review (and dismissed reviews) are never seed-cleaned, and requesters see "Importing" forever | [ACQ-09](ACQ.md#acq-09) |
| ops-7 | medium | Block on a Book or Music download deletes it but blocklists nothing, so the next sweep grabs the same release again | [ACQ-10](ACQ.md#acq-10) |
| ops-8 | medium | Review gives the same four actions for very different problems, several of them dead ends, with little context | [ACQ-12](ACQ.md#acq-12), [ACQ-13](ACQ.md#acq-13) |
| ops-9 | medium | History is a raw imports-table dump: no dates, no types, no links, and it includes things that were never imported | [ACQ-05](ACQ.md#acq-05), [ACQ-09](ACQ.md#acq-09), [ACQ-27](ACQ.md#acq-27) |
| ops-10 | low | Music torrents are labelled "Movie", and Searching/Upcoming leave out books and music | [ACQ-17](ACQ.md#acq-17), [ACQ-18](ACQ.md#acq-18) |
| ops-11 | medium | Downloads is a raw torrent list with no poster, no title link and no requester. The admin view knows less than the requester's progress view | [ACQ-25](ACQ.md#acq-25), [ACQ-26](ACQ.md#acq-26), [ACQ-28](ACQ.md#acq-28), [ACQ-29](ACQ.md#acq-29), [ACQ-30](ACQ.md#acq-30), [ACQ-31](ACQ.md#acq-31) |
| ops-12 | low | No Blocklist page: global, book and music blocklist entries can't be seen or undone | [ACQ-11](ACQ.md#acq-11) |
| ops-13 | medium | Requesters can read the full transfer list, file paths and import history, and the websocket broadcasts everyone's Plex activity to any logged-in user | [SEC-02](SEC.md#sec-02), [SEC-03](SEC.md#sec-03) |
| ops-14 | low | Polling-heavy pages: a large fan-out endpoint every 3 seconds, History every 5, and a websocket opened only to print "connected" | [ACQ-27](ACQ.md#acq-27), [ACQ-32](ACQ.md#acq-32) |
| ops-15 | low | The Searching tab pulses "Searching…" with no last or next search and no Search now button, though the backoff data exists | [ACQ-18](ACQ.md#acq-18) |
| product-1 | high | Plex is only read from: no library scan after import, no 'is it in Plex yet' check, no 'Watch on Plex' link | [PLEX-04](PLEX.md#plex-04), [PLEX-05](PLEX.md#plex-05), [PLEX-10](PLEX.md#plex-10), [PLEX-11](PLEX.md#plex-11), [REQ-15](REQ.md#req-15), [REQ-16](REQ.md#req-16) |
| product-2 | high | Owner request management is a hover-only poster strip: pending sorted last, no profile choice, no decline reason, unbounded | [REQ-03](REQ.md#req-03), [REQ-04](REQ.md#req-04), [REQ-05](REQ.md#req-05), [REQ-06](REQ.md#req-06), [REQ-08](REQ.md#req-08) |
| product-3 | high | A series request downloads every season, and 'ready' waits until no episode is missing | [REQ-11](REQ.md#req-11), [REQ-12](REQ.md#req-12), [REQ-13](REQ.md#req-13) |
| product-4 | high | Nothing tells the owner something needs them: no alert for new requests, held reviews or failures; no badges | [FE-28](FE.md#fe-28), [OBS-07](OBS.md#obs-07), [OBS-08](OBS.md#obs-08), [OBS-11](OBS.md#obs-11), [OBS-12](OBS.md#obs-12), [OBS-14](OBS.md#obs-14) |
| product-5 | medium | The health panel can't see the common failures: indexers, Plex token, TMDB key, background tasks | [INT-01](INT.md#int-01), [INT-02](INT.md#int-02), [INT-14](INT.md#int-14), [OBS-03](OBS.md#obs-03), [OBS-04](OBS.md#obs-04), [OBS-05](OBS.md#obs-05), [OBS-06](OBS.md#obs-06), [OBS-15](OBS.md#obs-15) |
| product-6 | high | No database backups at all, even though one SQLite file now holds 8 apps' state and every update can change its schema | [SAFE-01](SAFE.md#safe-01), [SAFE-11](SAFE.md#safe-11), [SAFE-12](SAFE.md#safe-12), [SAFE-13](SAFE.md#safe-13), [SAFE-15](SAFE.md#safe-15) |
| product-7 | high | The requester phone app is rough: header won't fit at phone width, ignores the iPhone notch, invisible tappable controls, bell only on Discover | [APP-01](APP.md#app-01), [APP-02](APP.md#app-02), [APP-03](APP.md#app-03), [APP-05](APP.md#app-05), [APP-13](APP.md#app-13), [APP-14](APP.md#app-14), [REQ-06](REQ.md#req-06) |
| product-8 | medium | Navigation and setup are scattered: Plex is connected inside Insights, admin alerts live in Insights, and Activity is split across three pages | [ACQ-26](ACQ.md#acq-26), [CFG-15](CFG.md#cfg-15), [CFG-16](CFG.md#cfg-16), [CFG-18](CFG.md#cfg-18), [FE-16](FE.md#fe-16), [FE-28](FE.md#fe-28), [OBS-10](OBS.md#obs-10), [OBS-11](OBS.md#obs-11), [OBS-16](OBS.md#obs-16) |
| product-9 | medium | The setup wizard stops before indexers and Plex; next steps are plain text, not links | [CFG-21](CFG.md#cfg-21), [CFG-22](CFG.md#cfg-22), [CFG-23](CFG.md#cfg-23), [CFG-24](CFG.md#cfg-24), [OBS-04](OBS.md#obs-04) |
| product-10 | medium | 'Searching…' pulses forever, even though the backend knows when it last searched and how many tries came up empty | [ACQ-15](ACQ.md#acq-15), [ACQ-16](ACQ.md#acq-16), [ACQ-18](ACQ.md#acq-18), [ACQ-19](ACQ.md#acq-19), [ACQ-20](ACQ.md#acq-20) |
| product-11 | medium | The ebook journey ends at 'Download EPUB': no OPDS feed, Send-to-Kindle or reader, and no format choice when requesting | [BOOK-13](BOOK.md#book-13), [BOOK-14](BOOK.md#book-14), [BOOK-22](BOOK.md#book-22), [BOOK-23](BOOK.md#book-23), [BOOK-24](BOOK.md#book-24) |
| product-12 | low | History is the barest page in the app: imports only, no dates, no links, no filters | [ACQ-26](ACQ.md#acq-26), [ACQ-27](ACQ.md#acq-27) |
| product-13 | medium | Stale or dishonest UI copy across the app | [COPY-02](COPY.md#copy-02), [COPY-03](COPY.md#copy-03), [COPY-08](COPY.md#copy-08), [COPY-09](COPY.md#copy-09), [COPY-10](COPY.md#copy-10) |
| product-14 | medium | No way for requesters to report a problem (bad audio, out-of-sync subtitles, wrong file) | [REQ-22](REQ.md#req-22), [REQ-23](REQ.md#req-23) |
| product-15 | low | Calendar is the weakest module compared with Sonarr: month grid only, unusable on phones, no agenda, no iCal feed | [APP-16](APP.md#app-16), [APP-17](APP.md#app-17), [APP-18](APP.md#app-18) |
| quality-1 | high | Series grabs, RSS and upgrades ignore the bitrate window (ceiling and floor) | [QUAL-05](QUAL.md#qual-05), [QUAL-06](QUAL.md#qual-06), [QUAL-16](QUAL.md#qual-16) |
| quality-2 | high | Deleting a profile does not move titles to the default; acquisition silently switches to a hidden permissive profile | [QUAL-01](QUAL.md#qual-01), [QUAL-02](QUAL.md#qual-02) |
| quality-3 | high | Templates' 'WEB-DL+' minimum source rejects scene 'WEB' releases and almost all anime fansub releases | [QUAL-04](QUAL.md#qual-04) |
| quality-4 | high | AV1 conversions are read back as H.264, which brings back the convert → re-download loop | [QUAL-03](QUAL.md#qual-03) |
| quality-5 | high | Silent mass re-download when a profile is edited; per-title changes ask first, per-profile edits don't | [QUAL-10](QUAL.md#qual-10), [QUAL-11](QUAL.md#qual-11), [QUAL-12](QUAL.md#qual-12), [QUAL-13](QUAL.md#qual-13) |
| quality-6 | low | Prefer can beat the goal resolution, while Avoid and any negative score drop a release below every resolution | [QUAL-18](QUAL.md#qual-18), [QUAL-21](QUAL.md#qual-21), [QUAL-22](QUAL.md#qual-22), [QUAL-23](QUAL.md#qual-23) |
| quality-7 | medium | No defences against the TRaSH 'Unwanted' set: full-disc (BR-DISK), foreign-language, upscaled, 3D and Dolby Vision without HDR fallback | [QUAL-19](QUAL.md#qual-19), [QUAL-20](QUAL.md#qual-20) |
| quality-8 | medium | Custom formats are too weak for TRaSH-style use and easily end up silently never matching | [QUAL-17](QUAL.md#qual-17), [QUAL-19](QUAL.md#qual-19), [QUAL-24](QUAL.md#qual-24), [QUAL-25](QUAL.md#qual-25), [QUAL-26](QUAL.md#qual-26), [QUAL-27](QUAL.md#qual-27) |
| quality-9 | medium | Seeders barely affect ranking, and stall fail-over is off by default, so dead torrents can win and sit forever | [ACQ-05](ACQ.md#acq-05), [ACQ-06](ACQ.md#acq-06), [QUAL-07](QUAL.md#qual-07) |
| quality-10 | medium | Two sources of truth: upgrade-stop judges the release name, the library fit judges the probed file | [QUAL-09](QUAL.md#qual-09) |
| quality-11 | medium | The 'why' explanations are templated rather than taken from what decided the ranking, and some copy is wrong | [QUAL-14](QUAL.md#qual-14), [QUAL-15](QUAL.md#qual-15) |
| quality-12 | low | Parser gaps: untagged SD releases are rejected, lossless is incomplete, pre-release check scans the title | [QUAL-08](QUAL.md#qual-08) |
| series-1 | high | Season Grab, episode Grab and Replace can pull a complete-series pack and then overwrite existing files with the quality gate skipped | [SER-01](SER.md#ser-01), [SER-02](SER.md#ser-02) |
| series-2 | high | Specials cannot be searched, and their Grab buttons become a whole-show grab | [SER-03](SER.md#ser-03), [SER-21](SER.md#ser-21) |
| series-3 | medium | Refresh never updates the show itself: status, title, poster and overview stay frozen at add time | [SER-06](SER.md#ser-06) |
| series-4 | high | Numbering rebuilds trust counted absolute numbers and can move and rename files onto the wrong episodes | [SER-04](SER.md#ser-04), [SER-05](SER.md#ser-05), [SER-07](SER.md#ser-07), [SER-20](SER.md#ser-20) |
| series-5 | medium | The profile's bitrate window and ceiling never apply to TV grabs, and ties go to the biggest file | [QUAL-05](QUAL.md#qual-05), [QUAL-06](QUAL.md#qual-06) |
| series-6 | high | Monitoring model: no add presets, the series toggle wipes per-season choices, and new seasons inherit the series flag | [REQ-10](REQ.md#req-10), [REQ-13](REQ.md#req-13), [SER-08](SER.md#ser-08), [SER-09](SER.md#ser-09) |
| series-7 | medium | Release matching ignores year and country: remakes cross-grab, and 'Show.US' releases never match | [SER-11](SER.md#ser-11), [SER-12](SER.md#ser-12) |
| series-8 | medium | Anime 'romaji' matching uses TMDB original_name, which is Japanese script, and that title is never searched for | [SER-12](SER.md#ser-12), [SER-13](SER.md#ser-13) |
| series-9 | medium | Search results only reach the logs; the UI fakes 'Requested' with localStorage | [ACQ-08](ACQ.md#acq-08), [ACQ-15](ACQ.md#acq-15), [ACQ-19](ACQ.md#acq-19), [SER-19](SER.md#ser-19), [SER-20](SER.md#ser-20) |
| series-10 | medium | Progress, 'Missing' and 'Partial' count unmonitored seasons | [SER-10](SER.md#ser-10) |
| series-11 | medium | Deleting a series deletes files by default, permanently, and leaves subtitles behind | [SAFE-05](SAFE.md#safe-05) |
| series-12 | medium | The detail page is an admin console, not a Plex-like page, and fetched metadata goes unused | [SER-04](SER.md#ser-04), [SER-14](SER.md#ser-14), [SER-15](SER.md#ser-15), [SER-16](SER.md#ser-16), [SER-17](SER.md#ser-17), [SER-20](SER.md#ser-20) |
| series-13 | low | The detail endpoint parses the whole download queue once per missing episode, every 3 seconds; History never refreshes | [SER-18](SER.md#ser-18) |
| series-14 | low | The series upgrade sweep only sees about 100 recent results and only single-episode releases | [SER-22](SER.md#ser-22) |
| series-15 | low | The 'Metadata not configured' banner tells users to set an env var and restart | [COPY-02](COPY.md#copy-02) |
| subtitles-1 | high | With OpenSubtitles configured, the AI fallback never runs: no-match and quota-exhausted files get nothing, forever | [SUB-01](SUB.md#sub-01), [SUB-12](SUB.md#sub-12), [SUB-22](SUB.md#sub-22) |
| subtitles-2 | high | Forced (foreign-parts-only) subtitles are treated as full subtitles everywhere | [SUB-03](SUB.md#sub-03), [SUB-07](SUB.md#sub-07), [SUB-09](SUB.md#sub-09), [SUB-33](SUB.md#sub-33), [SUB-34](SUB.md#sub-34) |
| subtitles-3 | medium | After a movie upgrade, the old release's sidecar counts as coverage for the new file, which Plex can't match | [SUB-04](SUB.md#sub-04), [SUB-05](SUB.md#sub-05), [SUB-10](SUB.md#sub-10) |
| subtitles-4 | high | No sync quality control: non-hash downloads are taken blindly and the 'Health' column is a permanent stub | [SUB-11](SUB.md#sub-11), [SUB-17](SUB.md#sub-17), [SUB-18](SUB.md#sub-18), [SUB-19](SUB.md#sub-19), [SUB-22](SUB.md#sub-22) |
| subtitles-5 | medium | Redo can't fix a bad download, and redoes every language | [SUB-11](SUB.md#sub-11), [SUB-20](SUB.md#sub-20), [SUB-28](SUB.md#sub-28) |
| subtitles-6 | medium | Whisper translate path forces the wrong source language and fails on untagged English audio | [SUB-06](SUB.md#sub-06), [SUB-21](SUB.md#sub-21) |
| subtitles-7 | medium | Single in-memory first-in-first-out queue: new imports wait behind days of AI backlog, and the queue is lost on restart | [SUB-02](SUB.md#sub-02), [SUB-23](SUB.md#sub-23), [SUB-25](SUB.md#sub-25) |
| subtitles-8 | medium | Failures are shown as 'skipped — nothing produced'; reasons live only in a 500-line in-memory log | [SUB-11](SUB.md#sub-11), [SUB-14](SUB.md#sub-14), [SUB-24](SUB.md#sub-24) |
| subtitles-9 | medium | UI and backend copy is stale or misleading ('soon' AI, stripping, Health) | [COPY-05](COPY.md#copy-05), [SUB-13](SUB.md#sub-13), [SUB-32](SUB.md#sub-32), [SUB-34](SUB.md#sub-34) |
| subtitles-10 | medium | Language support is a hard-coded 16-language table, split across three tables that disagree | [SUB-03](SUB.md#sub-03), [SUB-08](SUB.md#sub-08), [SUB-09](SUB.md#sub-09), [SUB-31](SUB.md#sub-31), [SUB-33](SUB.md#sub-33), [SUB-35](SUB.md#sub-35) |
| subtitles-11 | medium | Provider status is fake-green: no credential test, wrong passwords fail silently, 429 pauses downloads for 24h | [SUB-13](SUB.md#sub-13) |
| subtitles-12 | medium | No interactive search, no per-sidecar management, no subtitles on detail pages, no movie search box | [SUB-26](SUB.md#sub-26), [SUB-27](SUB.md#sub-27), [SUB-28](SUB.md#sub-28), [SUB-29](SUB.md#sub-29) |
| subtitles-13 | low | Legacy endpoints bypass the queue and let any signed-in user (including requesters) trigger full-library walks | [SUB-16](SUB.md#sub-16) |
| subtitles-14 | low | Nothing tells Plex a new subtitle exists | [SUB-30](SUB.md#sub-30) |
| subtitles-15 | low | Changing kept languages leaves Overview and Library coverage stale for up to 6 hours | [SUB-15](SUB.md#sub-15) |
| system-1 | high | 'Save settings' always fails: the whole GET payload, including read-only fields, goes to a decoder that rejects unknown fields | [CFG-01](CFG.md#cfg-01), [CFG-14](CFG.md#cfg-14) |
| system-2 | high | No backups, no restore, no rollback, and the whole household's history is in one SQLite file | [CFG-05](CFG.md#cfg-05), [CFG-06](CFG.md#cfg-06), [SAFE-01](SAFE.md#safe-01), [SAFE-11](SAFE.md#safe-11), [SAFE-12](SAFE.md#safe-12), [SAFE-13](SAFE.md#safe-13), [SAFE-15](SAFE.md#safe-15), [SAFE-18](SAFE.md#safe-18) |
| system-3 | high | The folder legacy ARRMADA_LIBRARY_DIR points at (the managed Docker volume) still holds the recycle bin and is what two checks measure | [CFG-19](CFG.md#cfg-19), [CFG-20](CFG.md#cfg-20), [OBS-01](OBS.md#obs-01), [SAFE-16](SAFE.md#safe-16), [SAFE-17](SAFE.md#safe-17) |
| system-4 | medium | Changing library or download folders in Settings needs a restart that the page never mentions or offers | [CFG-03](CFG.md#cfg-03), [CFG-19](CFG.md#cfg-19), [CFG-20](CFG.md#cfg-20) |
| system-5 | high | One unconfirmed click deletes a user and wipes their audiobook places and history, and there is no 'disable' option | [CFG-10](CFG.md#cfg-10), [CFG-18](CFG.md#cfg-18), [SAFE-03](SAFE.md#safe-03) |
| system-6 | medium | Account lifecycle gaps: no password change, no admin recovery, case-sensitive emails, hard 30-day expiry, and API roles that don't match the UI | [CFG-09](CFG.md#cfg-09), [CFG-10](CFG.md#cfg-10), [CFG-11](CFG.md#cfg-11), [CFG-12](CFG.md#cfg-12), [CFG-18](CFG.md#cfg-18), [FE-02](FE.md#fe-02), [REQ-09](REQ.md#req-09), [REQ-14](REQ.md#req-14), [SEC-09](SEC.md#sec-09), [SEC-14](SEC.md#sec-14), [SEC-15](SEC.md#sec-15) |
| system-7 | medium | Updates compile main HEAD on the server with no releases, no image build in CI and no way back | [CFG-05](CFG.md#cfg-05), [CFG-26](CFG.md#cfg-26), [CFG-27](CFG.md#cfg-27), [CFG-28](CFG.md#cfg-28), [CFG-29](CFG.md#cfg-29) |
| system-8 | medium | Observability amounts to a log viewer: scheduled tasks are invisible and the health banner loads once and checks the wrong things | [CFG-26](CFG.md#cfg-26), [OBS-01](OBS.md#obs-01), [OBS-03](OBS.md#obs-03), [OBS-04](OBS.md#obs-04), [OBS-05](OBS.md#obs-05), [OBS-06](OBS.md#obs-06), [OBS-12](OBS.md#obs-12), [OBS-15](OBS.md#obs-15) |
| system-9 | medium | Settings are spread across about eight pages that save in five different ways | [CFG-13](CFG.md#cfg-13), [CFG-14](CFG.md#cfg-14), [CFG-15](CFG.md#cfg-15), [CFG-17](CFG.md#cfg-17), [CFG-18](CFG.md#cfg-18), [INT-15](INT.md#int-15), [INT-16](INT.md#int-16) |
| system-10 | medium | The setup wizard and Library folders validate nothing: no key test, no folder or hardlink check, no guard against /data | [CFG-04](CFG.md#cfg-04), [CFG-22](CFG.md#cfg-22), [CFG-23](CFG.md#cfg-23), [CFG-25](CFG.md#cfg-25), [INT-06](INT.md#int-06), [SEC-06](SEC.md#sec-06) |
| system-11 | medium | Sign in with Plex opens its popup after an await and has no fallback if the popup is blocked; the owner's Plex account becomes a second, requester-only account | [APP-04](APP.md#app-04), [PLEX-06](PLEX.md#plex-06), [PLEX-14](PLEX.md#plex-14), [PLEX-15](PLEX.md#plex-15) |
| system-12 | low | Logs: the debug level can't be switched on, the copy understates retention, persisted history is out of reach, and the page polls everything every 3 s | [CFG-30](CFG.md#cfg-30), [CFG-31](CFG.md#cfg-31) |
| system-13 | low | Clicking Save on an empty API-key field silently clears the saved key | [CFG-02](CFG.md#cfg-02) |
| system-14 | low | Naming has no Plex-native tokens: no {tmdb-…} folder ids, no Plex Pass {edition-…}, no presets, no bulk rename | [MOV-12](MOV.md#mov-12), [MOV-17](MOV.md#mov-17), [PLEX-13](PLEX.md#plex-13) |
| system-15 | low | System copy and options promise things that aren't true | [CFG-08](CFG.md#cfg-08), [CFG-26](CFG.md#cfg-26), [COPY-03](COPY.md#copy-03), [COPY-09](COPY.md#copy-09), [COPY-10](COPY.md#copy-10), [FE-05](FE.md#fe-05), [FE-29](FE.md#fe-29) |
| walk-1 | low | TMDB setup banners disagree (env var vs Settings → API keys); requesters see admin instructions | [COPY-02](COPY.md#copy-02) |
| walk-2 | low | Subtitles/Insights show "soon"/"coming soon" for shipped features | [COPY-05](COPY.md#copy-05), [COPY-08](COPY.md#copy-08) |
| walk-3 | low | Breadcrumbs disagree with sidebar groups | [FE-17](FE.md#fe-17) |
| walk-4 | low | Users tab says requesters see only Discover | [COPY-09](COPY.md#copy-09) |
| walk-5 | low | Library tab repeats its intro; disk guard tells you to edit .env | [CFG-03](CFG.md#cfg-03), [COPY-09](COPY.md#copy-09) |
| walk-6 | low | Downloads shows "free 0 GB" with no download client | [ACQ-23](ACQ.md#acq-23) |
| walk-7 | low | Missing-file movie shows a DOWNLOADED badge | [MOV-03](MOV.md#mov-03), [MOV-21](MOV.md#mov-21) |
| walk-8 | low | Inconsistent library toolbars; Movies toolbar/table break at 375px | [FE-19](FE.md#fe-19), [FE-20](FE.md#fe-20) |
| walk-9 | low | Inconsistent page content widths | [FE-18](FE.md#fe-18) |
| walk-10 | low | One 914 kB bundle, no route splitting | [FE-04](FE.md#fe-04), [FE-06](FE.md#fe-06) |

