# BOOK — Books

_Part of the [Arrmada roadmap](../../ROADMAP.md). 26 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make Books trustworthy and complete. Each book is one library row with a stable id. Catalogue keys are only aliases of that row. Nothing is merged or dropped unless a person says so. A wanted book keeps being searched until it arrives, and the app says where it is. Requesters choose to read, listen or both. Followed authors bring their new books in automatically. Ebooks reach e-readers through OPDS and reach Kindles by email.

**Why.** Books scored 5/10 in the 2026-10-08 audit. The import gates are good, but the module treats catalogue keys and truncated titles as identity, so it quietly loses things people care about.

- **Startup deletes books (books-2, backend-11).** On every boot, MergeDuplicates folds rows whose titles match only up to the first ':' / ' (' / ' - '. 'Mistborn: The Final Empire' and 'Mistborn: Secret History' become one book. The deleted row takes its extra narrations with it (cascade), leaves files untracked on disk, and orphans every family member's listening place and bookmarks ('b<id>' item keys). The same truncated key also makes Discover show sibling books as 'In library', blocks adding them (409), and files one book's download onto another.
- **Requests lose track of their book (books-1).** Book requests join on requests.ol_key, but the library row's key changes: the automatic Hardcover upgrade rewrites it, Hardcover swaps to canonical ids, and Approve can land on an existing row under another key. Every request made before the upgrade shows 'Searching' forever, never sends 'ready', and loses its 'Yours' tag on My shelf.
- **Wanted books stop being searched (books-3).** After about two searches the book is dropped for good. MyAnonaMouse, the owner's main book tracker, has no RSS path, and the UI keeps saying 'Arrmada is searching'.
- **Matching is loose or brittle (books-4, books-5, books-13).** The RSS matcher is a substring check, so 'It' matches 'The Institute' and 'Dune' matches 'Dune Messiah'. Accents and apostrophes break matches ('Pokémon', 'Ender's Game'). Hand-picked grabs still go to Review. The language of a release is never checked.
- **Requesters can't pick a format (books-6, product-11).** They can't choose ebook or audiobook. Once one format exists they can't ask for the other, and the 'ready' message always says 'ready to read'.
- **Authors and metadata (books-8, books-9).** Authors are plain strings, can't be followed, and the author page guesses the person by name. Books added from an author's catalogue never get descriptions or genres.
- **Ebooks stop at a browser download (books-7, product-11).** There is no OPDS and no Send-to-Kindle.
- **The admin Books screens don't fit a phone (books-12).** They are a wall of buttons, and the card actions only appear on hover.

**Depends on:** ACQ — needs the shared rule 'don't record a search miss when every indexer errored' (audit quick win #3), so books and movies behave the same ([BOOK-04](#book-04)). If it hasn't landed, implement locally and switch later; REQ — [BOOK-03](#book-03), [BOOK-08](#book-08), [BOOK-09](#book-09) and [BOOK-13](#book-13) edit internal/requests/* (book_id, formats, tracking notes). Sequence them with REQ's request-model and Discover requests-row work to avoid conflicting edits; SEC — the tag-based books adult-content filter must be applied by [BOOK-22](#book-22)'s OPDS feeds once SEC adds it. [BOOK-22](#book-22) and [BOOK-24](#book-24) follow SEC's request-log redaction pattern (route patterns, no ids or query values); AUD — [BOOK-10](#book-10) changes audioserver item lookup and the progress write paths in handlers_play.go and handlers_library.go, so coordinate with AUD's sync fixes there. [BOOK-22](#book-22) relabels the audiobook password card. ebookFile on the Audiobookshelf-compatible server stays with AUD; SAFE — audiobook merge safety (duration check, recycle instead of hard delete) must land before [BOOK-26](#book-26) edits internal/audiobook/merge.go; OBS — the admin alert catalogue delivers the 'book.author_release' bus event from [BOOK-21](#book-21); FE — the component kit's Menu and Modal are used by [BOOK-12](#book-12) and [BOOK-25](#book-25). If it hasn't landed, a local accessible OverflowMenu is the fallback; BE — the store.WithTx transaction helper is optional for [BOOK-11](#book-11). BeginTx works without it; APP — the MyBooks detail sheet and Me page are where [BOOK-24](#book-24)'s Send to Kindle button and Kindle address finally live. [BOOK-24](#book-24) ships on cards and a first-time sheet first; MUS — the music matcher should adopt [BOOK-06](#book-06)'s accent and apostrophe fold rules (audit quick win #13)

#### Design

## Target architecture

### 1. Book identity
- **The id is the identity.** `books.id` is the book. `books.ol_key` stays as the row's current primary catalogue key.
- **Aliases** ([BOOK-09](#book-09)): `book_keys(key TEXT PK, book_id → books ON DELETE CASCADE, source, added_at)` stores every key the row has ever had. It is seeded from `books.ol_key` and from the keys on linked requests. It is written by Create, by Add when Hardcover swaps to a canonical key, by the Add-hits-duplicate path, by Rematch, by the Hardcover upgrade, by the disk scan, and by Merge. Anything that resolves a catalogue key to a book (Discover cards, request dedupe, series gaps, Recommended, `findByKey`) goes through `BookIDForKey`.
- **Title identity** ([BOOK-02](#book-02)): `IdentityOf(title, author) → {Full, Sub, Author, NoAuthor}`.
  - Trailing `(…)` / `[…]` notes are stripped.
  - A subtitle after `:` / ` - ` / ` — ` is dropped only when it is an edition note (Deluxe Edition, Unabridged, A Novel…) or a series note (Book One of the Dune Chronicles, The Expanse #1).
  - Otherwise `Full` = main + subtitle, and `Sub` = the subtitle alone.
  - Two books are the same when the author keys match (and both are blank or both are set), **and** either `Full == Full` or one book's `Sub` equals the other's `Full`.
  - A subtitle is never compared with a subtitle, so franchise subtitles such as 'X: Star Wars' can't collapse different books. A main title is never compared on its own, so 'Thrawn' ≠ 'Thrawn: Alliances'.
  - Every caller uses one `IdentityIndex` (byFull and bySub maps), which prefers the row that has files.
- **One normaliser** ([BOOK-06](#book-06)): `internal/books/normalize.go` folds accents (`parser.FoldAccents`), curly apostrophes and `&`. It produces joined and split variants for apostrophes. It feeds the release matcher, the file matcher, `NormKey`, `titlePartKey` and `authorKey`.
- **Requests link by id** ([BOOK-03](#book-03)): `requests.book_id INTEGER REFERENCES books(id) ON DELETE SET NULL`.
  - It is set on Approve, including the ErrExists case.
  - Existing requests are backfilled: by exact key in SQL, then by a unique `SameBook` match in Go at boot.
  - `requests.ol_key` remains the request's own key and its inbox dedupe ref (`requestRef = 'book:'+ol_key`), so no duplicate notifications go out.

### 2. Nothing merges by itself
- **No automatic merges** ([BOOK-01](#book-01)): no boot merge and no merge-all endpoint. When the Hardcover upgrade finds a key already used by another row, it flags the pair ('possible_duplicate' event, later a `book_dupe_reviews` row) instead of folding.
- **Possible duplicates review** ([BOOK-12](#book-12), manager only):
  - Groups come from computed `SameBook` pairs, a weaker 'same title, overlapping author' tier, and upgrade flags, minus pairs marked ignored.
  - A suggested keeper is offered.
  - A past-merges list (`book_events` with event='merged') lets the owner re-add books lost to earlier boots.
  - The review shows no listening data.
- **Lossless merge** ([BOOK-11](#book-11)): `Service.Merge(keep, drop)` runs in one transaction.
  - Editions the keeper lacks move over.
  - If both rows have an audiobook, the dropped row's main audiobook becomes a `book_audio_versions` row labelled 'Merged copy'. The dropped row's other versions are re-parented.
  - If both rows have an ebook, the extra ebook file is left on disk and reported. Files are never deleted.
  - Listening rows are re-keyed through `listening.RekeyItemsTx` ([BOOK-10](#book-10)). The newer `updated_at` wins. Bookmarks use INSERT OR IGNORE. Sessions and history move. `listen_item_redirects(old_key → new_key)` has no user column; through it the audio server still accepts late or offline reports for the old item id.
  - Requests (`book_id`), aliases, `book_events`, grabs and blocklist (`media_type='book' AND movie_id`) and `import_reviews.expected_id` are re-pointed.
  - Afterwards `book.imported` is published for the keeper so the audiobook catalog cache refreshes.

### 3. Search lifecycle
- **Ladder** (`internal/books/ladder.go`, [BOOK-04](#book-04)): `SearchWait(misses)`: 0 → now, 1 → 24h, 2 → 72h, 3–10 → 7d, 11+ → 30d. It never gives up.
  - Each 30-minute sweep runs at most 40 book searches, oldest due first.
  - No miss is recorded when every indexer errored (a shared rule with ACQ).
- **Release day** ([BOOK-18](#book-18)): `books.release_date` (YYYY-MM-DD, from Hardcover).
  - No sweep searches run before the release date.
  - On release day misses reset, and the book is searched daily for 14 days.
  - Request tracking reads 'Out 12 Mar'.
- **RSS** ([BOOK-05](#book-05), [BOOK-07](#book-07)): every 15 minutes, now including MyAnonaMouse (`Recent` = an empty-text search sorted `dateDesc`, ≤100 results, one call per cycle).
  - Releases are filtered by the same word-boundary matcher snapshot the search path uses.
  - Only editions the profile wants are grabbed.
  - RSS grabs write the 'grabbed' timeline event and learn the series.
- **Language** ([BOOK-16](#book-16)): the `books_languages` setting (default `en`). MAM `lang_code` is normalised to ISO 639-1. Automatic grabs reject other languages, and interactive search shows them with the reason.
- **Visible state** ([BOOK-08](#book-08)):
  - `GET /books/{id}` returns `last_search_at`, `search_misses` and `next_search_at`.
  - Request `Tracking` gains `next_check_at` and the note 'Not found yet'.
  - BookDetail, My shelf and the Discover requests row show 'Not found yet — next check Tue 14 Oct' instead of 'Arrmada is searching'.
- **Manual grabs** ([BOOK-15](#book-15)): `grabs.manual=1` for interactive and uploaded-torrent grabs, so the import goes into the book they were picked for without the name gate.

### 4. Read / Listen / Both ([BOOK-13](#book-13), [BOOK-14](#book-14))
- `requests.formats TEXT NOT NULL DEFAULT ''`: `''` means a legacy request (ready when any file exists, one 'ready to read'); otherwise `ebook`, `audiobook` or `both`.
- `quality.Service.BookProfileFor(ebook, audio)` (moved from automation) maps formats to the three fixed book presets (migration 0032). There is no per-book override column: the profile already is the format choice.
- A second requester's formats are unioned into the request. Approve, or a request for a book that is already in the library, widens the book's profile and searches the missing edition. A profile is never narrowed.
- A request is ready only when every requested format is on disk. Each format gets its own notification: '“X” is ready to read.' or 'The audiobook of “X” is ready to listen.', with refs `book:<key>:ebook` / `book:<key>:audiobook` for new requests only.
- Discover cards carry `has_ebook`, `has_audiobook`, `want_ebook` and `want_audiobook`, show per-format badges, and offer 'Request audiobook' when only the ebook exists. The request modal has a Read / Listen / Both control.

### 5. Authors ([BOOK-19](#book-19) – [BOOK-21](#book-21))
- **Data model:**
  - `authors(id, name, catalogue_key UNIQUE WHERE != '', image_url, bio, monitored, monitor_new, quality_profile, added_at, checked_at, works_checked_at)`.
  - `books.author_id → authors ON DELETE SET NULL`.
  - `author_works_seen(author_id, work_key, first_seen)`.
- **Backfill:** a Go backfill groups `books.author` by the normalised `authorKey`. It never merges two names that have different catalogue keys. `book_authors` is kept for one release.
- **API:** `GET /api/v1/book-authors`, `GET /api/v1/book-authors/{id}`, `PUT /api/v1/book-authors/{id}`.
  - These deliberately do not live under `/api/v1/books/authors/{id}`. Go's ServeMux panics at startup on patterns that overlap without either being more specific, and that path would overlap `GET /api/v1/books/{id}/series`, `/history`, `/covers` and the others.
- **UI:** author pages live at `/books/authors/:id` and use the stored catalogue key. An ambiguous author gets a 'Which author is this?' picker instead of a silent guess.
- **New releases:** a `check-author-releases` job runs every 6h. It checks at most a few authors per run, each about once a week, and is gated on Hardcover budget usage below 70%. The first check only seeds `author_works_seen`. Later checks add new works as monitored books and publish `book.author_release` for admin alerts.

### 6. Catalogue freshness ([BOOK-17](#book-17))
- `books.meta_checked_at` plus a `refresh-books` job (6h, 25 books per run, Hardcover budget gate) fill in descriptions, genres, series, release dates and authors.
- 'Find series' is hidden when Hardcover is the source.

### 7. Ebooks to devices ([BOOK-22](#book-22) – [BOOK-24](#book-24))
- **OPDS 1.2** (`internal/opds`, mounted at `/opds` before the SPA fallback; non-/api paths already pass `externalGate`):
  - Feeds: navigation, New, Mine (via book_id), Authors, Series, Search with OpenSearch, file download, cover.
  - Auth: HTTP Basic with the username and the per-user audiobook-server password (`audioserver.Accounts.Authenticate`, keeping the bcrypt timing guard). Failures go through the login limiter.
  - Logs show route patterns only. Feeds respect the SEC books adult filter once it exists.
- **Outgoing email** (`internal/mail`, net/smtp with STARTTLS or implicit TLS):
  - Host, port, security, username and from address are stored in settings. The password is stored in the apikeys store: write-only, entered in the UI.
  - Settings has a 'Send test email' action.
- **Send to Kindle:**
  - `users.kindle_email`, read and written through `GET/PUT /api/v1/me/kindle`.
  - `POST /api/v1/me/books/{id}/send-to-kindle` returns 202. It accepts EPUB or PDF up to 50 MB and allows 20 sends per user per day. The result goes to the user's inbox.

### 8. Admin UI ([BOOK-25](#book-25))
- **Books header:** '+ Add book' (primary), '+ Add author', and 'Search missing (n)' up front. A '⋯ Library tools' menu holds Scan library, Re-match to Hardcover, Possible duplicates (n), Find series (Open Library setups only) and Select.
- **View and sort:** one View control, and a Sort dropdown whose choice is kept in the URL.
- **Detail toolbar:** grouped as Monitor | Search ▾ | Add files ▾ | Fix ▾ | Delete.
- **Touch:** card actions get a '⋯' button on touch devices.
- **Style:** the existing dark warm palette, terracotta accent and type scale are kept.

### Migration plan
Numbers are assigned at commit time: the next free number after 0089 and anything other epics have landed. One migration per task, in this order:
1. `request_book_id`: column, index and exact backfill. [BOOK-03](#book-03).
2. `book_keys`: table, index, and a seed from `books.ol_key` and linked requests. [BOOK-09](#book-09).
3. `listen_item_redirects`. [BOOK-10](#book-10).
4. `book_dupe_reviews` (`a_id < b_id`, reason, status open|ignored). [BOOK-12](#book-12).
5. `request_formats`. [BOOK-13](#book-13).
6. `book_meta_checked`. [BOOK-17](#book-17).
7. `book_release_date`. [BOOK-18](#book-18).
8. `authors` and `books.author_id`. [BOOK-19](#book-19).
9. `author_works_seen`. [BOOK-21](#book-21).
10. `user_kindle`. [BOOK-24](#book-24).

Go backfills (`BackfillBookIDs`, `BackfillAuthors`) are idempotent and run once in the boot goroutine. They log counts only.

### Privacy invariants
- No log line, admin view or table added by this epic pairs a user with a book.
- Merges log counts only.
- The duplicate review shows no listening data.
- `listen_item_redirects` has no user column.
- OPDS and Kindle sends log route patterns and outcomes, never book ids or titles.

#### Milestone: M1 — Stop losing books and requests

_Booting never deletes a book again. Prefix-sibling books ('Thrawn' / 'Thrawn: Alliances') can be added and requested. Every book request, including those made before the Hardcover upgrade, finds its book, shows Available and sends 'ready'. Wanted books stay on a search ladder instead of being dropped after two tries._

<a id="book-01"></a>
- [x] **BOOK-01 · Turn off every automatic book merge (boot, the merge-all endpoint, the Hardcover upgrade fold)** — `P0` · `S` · Phase 0
  - **Problem:** Three paths delete book rows with no one asking.
- On every start, the boot goroutine calls booksSvc.MergeDuplicates (cmd/arrmada/main.go:184-193), keyed on the truncating DedupeKey. Same-author prefix siblings ('Mistborn: The Final Empire' / 'Mistborn: Secret History', every 'Star Wars: X') get folded.
- POST /api/v1/books/dedupe (server.go:376) merges every group blind.
- The Hardcover upgrade, which MaybeStartUpgrade also starts at boot, folds two rows that land on one Hardcover key (upgrade.go:268-281).

All three use foldInto (dedupe.go:207-241), which copies an edition only when the keeper lacks it and then runs repo.Delete(dup.ID). The damage:
- book_audio_versions cascade away (0082).
- The duplicate's audiobook record is lost when both rows have one, and its file stays untracked on disk.
- listen_progress, listen_history, listen_sessions and listen_bookmarks rows keyed 'b<id>' / 'b<id>v<vid>' are orphaned, so family members lose their place.

Only a 'merged' timeline event and a log line record any of this.
  - **Approach:** 1. cmd/arrmada/main.go: delete the MergeDuplicates block from the boot goroutine, keep booksSvc.MaybeStartUpgrade, and rewrite the comment.
    2. internal/books/upgrade.go upgradeOne: when findByKey(d.Key) returns another row (other.ID != b.ID):
    - Do not fold. Leave b on its current key.
    - AddEvent on both rows: event 'possible_duplicate', detail 'Hardcover lists this as the same book as “<other title>” (book <id>). Not merged; review it under Possible duplicates.'
    - Log at Info with ids only.
    - Return outcome 'flagged' with that reason. Add Flagged to UpgradeStatus next to Unmatched, and drop the Merged counter, updating the Books page upgrade banner text that reads it.
    3. Delete MergeDuplicates and foldInto, which have no callers left. Remove the POST /api/v1/books/dedupe route (server.go:376), handleMergeBookDuplicates (httpapi/books.go ~100-110) and api.mergeBookDuplicates (web/src/lib/api.ts:1404). Grep first to confirm there is no UI caller. [BOOK-12](#book-12) adds the reviewed replacement.
    4. merge_test.go: delete the tests that pin auto-merge behaviour. Keep the editionCount helper if anything still uses it.
    5. Commit message note: rows deleted by earlier boots are not restored. [BOOK-12](#book-12) lists past 'merged' events so the owner can re-add them.
  - **Files:** `cmd/arrmada/main.go`, `internal/books/dedupe.go`, `internal/books/upgrade.go`, `internal/books/merge_test.go`, `internal/books/upgrade_test.go`, `internal/httpapi/books.go`, `internal/httpapi/server.go`, `web/src/lib/api.ts`, `web/src/pages/Books.tsx`
  - **Acceptance:**
    - Restarting the server with 'Mistborn: The Final Empire' and 'Mistborn: Secret History' (same author) in the library leaves both rows, and no 'merged duplicate' log line appears
    - A Hardcover re-match that lands on a key another row already holds leaves both rows, their audio versions and their listening rows untouched, and writes a 'possible_duplicate' event on both timelines
    - POST /api/v1/books/dedupe no longer exists (404/405), and grep finds no foldInto or MergeDuplicates
    - The upgrade banner reports flagged rows instead of 'merged'
  - **Tests:** Go: upgrade_test with a fake BookProvider returning a Hardcover key already used by another row: both rows remain, the outcome is 'flagged', and both rows get the event; Go: upgrade_test where the duplicate row has an audio version and a listen_progress row: both are still present after runUpgrade; Race suite in Docker before pushing
  - **Risk:** Real duplicates (one novel under an Open Library key and a Hardcover key) stay as two rows until BOOK-11/BOOK-12 ship. The cost is cosmetic: two rows in the library. A flagged row is re-queried on the next boot's upgrade run like any other unmatched row, which costs 1-2 Hardcover requests each.
  - **Resolves:** books-2, backend-11
<a id="book-02"></a>
- [x] **BOOK-02 · Full-title book identity: IdentityOf / SameBook / IdentityIndex replace the truncating titleKey everywhere** — `P0` · `M` · Phase 1
  - **Problem:** titleKey (internal/books/dedupe.go:30-39) cuts every title at the first ':', ' (', ' [', ' - ' or ' — '. Same-author books that share a prefix ('Thrawn' / 'Thrawn: Alliances', 'Mistborn: The Final Empire' / 'Mistborn: Secret History', every 'Star Wars: X' by one author) therefore get one DedupeKey. The effects:
- Add returns ErrExists, a 409 'already in your library' (httpapi/books.go:148-151).
- AddWorks skips them (service.go:205-208).
- enrichBookCards marks them in_library and has_file through the 'd:' key (httpapi/books.go:563-590), which hides Request.
- Recommended filters them out (discover.go:60,90), and series gaps read as owned (catalogue.go:83-98).
- Request approval lands on the sibling, and the disk scan files the second book's files onto the first.

matchUpgrade's fallback titleKeys/keysOverlap also matches subtitle against subtitle, so 'Heir to the Empire: Star Wars' overlaps 'Dark Force Rising: Star Wars'.
  - **Approach:** 1. internal/books/dedupe.go: add type Identity struct { Full, Sub, Author string; NoAuthor bool } and IdentityOf(title, author string) Identity.
    - Lowercase and trim. Repeatedly strip a trailing '(…)' or '[…]' note: '(Dune Chronicles, #1)', '(Star Wars)', '[Illustrated]'.
    - Split at the first ':' / ' - ' / ' — ' into main and sub.
    - Drop sub (Full = titlePartKey(main), Sub = '') when it matches either regex:
      - editionNoteRe = \b(edition|anniversary|illustrated|deluxe|unabridged|abridged|annotated|collector'?s|special|revised|expanded|a novel|a thriller|a mystery|a memoir|a novella)\b
      - seriesNoteRe: '(book|volume|vol\.?|part) (\d+|one…ten)( of\b|$)', '#\s*\d+$' or ', book \d+$'
    - Otherwise Full = titlePartKey(main+' '+sub) and Sub = titlePartKey(sub). Keep Sub only when it has a letter and is at least 4 characters (never a bare volume number).
    - ' - The Graphic Novel' and 'Omnibus' stay distinct products.
    - Author = authorKey(author), and NoAuthor = trimmed author == ''.
    2. func (a Identity) SameTitle(b Identity) bool returns a.Full == b.Full || (a.Sub != '' && a.Sub == b.Full) || (b.Sub != '' && b.Sub == a.Full).
    - Subtitle is never compared with subtitle, which blocks franchise collisions such as 'X: Star Wars'.
    - A main title alone is never compared, so 'Thrawn' never matches 'Thrawn: Alliances'.
    - 'The Final Empire' still matches 'Mistborn: The Final Empire'.
    3. SameBook(aTitle, aAuthor, bTitle, bAuthor) bool = SameTitle, equal Author, and equal NoAuthor (the existing rule that both are blank or both are set). Add type IdentityIndex with NewIdentityIndex([]Book), Add(Book) and Find(title, author) (Book, bool). It holds byFull and bySub maps keyed 'key|author', probes byFull[full], then byFull[sub], then bySub[full], and prefers a row with files, then the lowest id. Keep DedupeKey() = Full+'|'+Author for logs and tests. Delete titleKey's truncation, titleKeys and keysOverlap.
    4. Switch every caller to IdentityIndex or SameBook:
    - findDuplicate (dedupe.go:111-126).
    - The AddWorks have-map (service.go:195-221). Add each created row to the index so a catalogue that repeats a novel still dedupes.
    - The catalogue.go:80-98 series owned check.
    - The discover.go:57-91 Recommended owned filter.
    - httpapi/books.go enrichBookCards: replace the 'd:'+DedupeKey map with one IdentityIndex built per request.
    5. upgrade.go matchUpgrade: pass 1 uses SameBook. Pass 2 uses IdentityOf(b).SameTitle(IdentityOf(r)) plus authorsOverlap. The authorless fallback keeps its current rule.
    6. Rewrite the dedupe_test pins: 'Dune: Deluxe Edition', 'Dune (Dune Chronicles, #1)' and 'Dune: Book One of the Dune Chronicles' all equal 'Dune'.
    Ship with or after [BOOK-01](#book-01), so the new key never feeds an automatic merge.
  - **Files:** `internal/books/dedupe.go`, `internal/books/dedupe_test.go`, `internal/books/service.go`, `internal/books/catalogue.go`, `internal/books/discover.go`, `internal/books/discover_test.go`, `internal/books/upgrade.go`, `internal/books/upgrade_test.go`, `internal/httpapi/books.go`
  - **Acceptance:**
    - POST /api/v1/books for 'Thrawn: Alliances' returns 201 when 'Thrawn' by the same author is already in the library
    - On Books Discover, 'Thrawn: Alliances' shows a Request button when only 'Thrawn' is owned
    - 'Dune: Deluxe Edition', 'Dune (Dune Chronicles, #1)' and 'Dune: Book One of the Dune Chronicles' still resolve to the existing 'Dune' row, and 'The Final Empire' still matches 'Mistborn: The Final Empire'
    - 'Heir to the Empire: Star Wars' and 'Dark Force Rising: Star Wars' by Timothy Zahn are two books
    - Add author for Timothy Zahn adds every Thrawn title not already present instead of skipping them as duplicates
    - A series panel with 'Mistborn: The Final Empire' owned lists 'Mistborn: Secret History' as missing
  - **Tests:** Go: dedupe_test TestSameBook table. Distinct pairs: Thrawn / Thrawn: Alliances; Mistborn: The Final Empire / Mistborn: Secret History; Dune: House Atreides / Dune: House Harkonnen; Halo: The Fall of Reach / Halo: First Strike; Heir to the Empire: Star Wars / Dark Force Rising: Star Wars; 'Series: Book 1' / 'Other: Book 1'. Same pairs: Dune / Dune: Deluxe Edition; Mistborn: The Final Empire / The Final Empire; Star Wars: Thrawn: Alliances / Thrawn: Alliances; White Sand Vol. 1 / White Sand #1. Plus the author blank/set rule; Go: IdentityIndex.Find prefers the row with files; Go: books service test where Add of a prefix sibling succeeds and AddWorks adds both siblings; Go: upgrade_test where matchUpgrade does not pick 'Thrawn: Alliances' for library 'Thrawn'; Go: httpapi enrichBookCards test where a prefix sibling is not in_library; Go: catalogue series-gap test where siblings are not marked owned
  - **Depends on:** [BOOK-01](#book-01)
  - **Risk:** Comparing full titles lets some catalogue variants through as two rows when the subtitle is neither an edition note nor a series note. BOOK-12's review catches those. Discover may offer Request for a book the owner has under a variant title. Rows folded by earlier boots are not restored (BOOK-12 lists them).
  - **Resolves:** books-2
<a id="book-03"></a>
- [x] **BOOK-03 · Link book requests to the library by book_id, and backfill existing requests** — `P0` · `M` · Phase 1
  - **Problem:** A book request stores the Discover card's key in requests.ol_key. Availability, the ready notifier, the ready sweep, request tracking and My shelf all join on that string (requests/service.go:286-300, usernotify.go:143-172 and 278-296, httpapi/mybooks.go:82-128). The library row's key keeps changing:
- The automatic Hardcover upgrade rewrites books.ol_key (upgrade.go:409, run at boot).
- Hardcover getBookLive swaps to the canonical id (hardcover.go:491-495).
- Approve hits ErrExists and gets a row under a different key, then does nothing more (requests/service.go:205-219).

Nothing ever updates requests.ol_key. Every pre-upgrade book request, fulfilled ones included, shows 'Searching' forever and never gets 'ready'. On My shelf it reappears under 'Your requests' next to its own book, without the 'Yours' tag.
  - **Approach:** 1. New migration NNNN_request_book_id.sql (next free number):
    - ALTER TABLE requests ADD COLUMN book_id INTEGER REFERENCES books(id) ON DELETE SET NULL. SQLite allows this because the default is NULL, and foreign_keys is ON (store.go:35).
    - CREATE INDEX idx_requests_book ON requests(book_id).
    - Exact backfill: UPDATE requests SET book_id = (SELECT id FROM books WHERE books.ol_key = requests.ol_key) WHERE media_type='book' AND book_id IS NULL.
    2. requests.Service.BackfillBookIDs(ctx), called once in the boot goroutine after migrations:
    - For book requests still NULL, build a books.IdentityIndex over the library ([BOOK-02](#book-02)) and set book_id only when exactly one row is SameBook.
    - Log 'backfilled N book requests (M ambiguous)' with counts only.
    - Once [BOOK-09](#book-09) lands, also AddKey(req.OLKey → book_id).
    3. Request gains BookID int64 `json:"book_id,omitempty"`. Add it to cols, scan and Create, and add repo.SetBookID(id, bookID) and repo.ListByBookID(bookID).
    4. Approve case 'book': after books.Add, on success or on ErrExists with b.ID > 0, call SetBookID. On ErrExists, call coord.SearchBookNow when the existing row lacks an edition its profile wants (books.WantedEditions). This is skipped today.
    5. enrichAvailability: build bookByID alongside bookHave. Use it when BookID > 0 and fall back to the ol_key map. Set libID from it so Track's activeGrabs finds the grabs.
    6. RunNotifier bookCh: ListByBookID(b.ID), falling back to GetByBook(b.OLKey). SweepReadyRequests resolves by book_id first.
    7. httpapi/mybooks.go: key mine and have by book id, so a fulfilled request is hidden from 'Your requests' and its book is tagged Mine.
    8. enrichBookCards: for requests with BookID, map that book's ol_key and identity to the request status, so the card under the new key shows Requested.
    9. Keep requestRef = 'book:'+req.OLKey. The request's own key never changes, so inbox dedupe holds and no duplicate 'ready' goes out.
  - **Files:** `internal/store/migrations/NNNN_request_book_id.sql`, `internal/requests/repo.go`, `internal/requests/service.go`, `internal/requests/usernotify.go`, `internal/requests/progress.go`, `internal/requests/service_test.go`, `internal/requests/usernotify_test.go`, `internal/httpapi/mybooks.go`, `internal/httpapi/mybooks_test.go`, `internal/httpapi/books.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - A request made under an Open Library key still shows Available after its book is re-matched to an hc: key (POST /books/{id}/rematch). On My shelf it sits on the shelf with 'Yours', not under 'Your requests'
    - Importing a file for a re-matched requested book sends exactly one 'ready' inbox notification to the requester and to each subscriber
    - Approving a request for a book already in the library under another key sets book_id. The request becomes Available when that book has a file, and a search starts when it lacks a wanted edition
    - On the owner's database the boot log shows 'backfilled N book requests', and long-fulfilled pre-upgrade requests show Available on Discover's requests row
    - Deleting a book sets its requests' book_id to NULL without errors
  - **Tests:** Go: requests service_test where Approve with books.ErrExists stores BookID and triggers SearchBookNow on the existing row (fake coordinator); Go: enrichAvailability resolves Available after books.Rematch changes ol_key; Go: usernotify_test where 'book.imported' for a re-matched book notifies once, and the sweep doesn't re-notify; Go: BackfillBookIDs links a unique title+author match and skips an ambiguous one; Go: httpapi mybooks test where a fulfilled request is not listed under requests and its book is Mine
  - **Depends on:** [BOOK-02](#book-02), REQ — coordinate with REQ tasks editing internal/requests/* in the same window
  - **Risk:** Backfill false positives when two library rows share a title and author: only unique matches are linked. Pending requests with no book_id yet still resolve by key until BOOK-09 adds aliases. Deleting a book reverts its request to Searching, which is acceptable.
  - **Resolves:** books-1
<a id="book-04"></a>
- [x] **BOOK-04 · Never give up on a wanted book: a slow search ladder with a per-sweep cap** — `P0` · `S` · Phase 1
  - **Problem:** bookSearchAttempts = 2, and once misses reach 2, bookSearchWait returns giveUp (automation/books.go:113-134). After the add-time search and one search a day later, the 30-minute sweep skips the book forever. Misses only reset on a grab. Anything uploaded a week after the request never arrives unless the admin presses 'Search missing'.
  - **Approach:** 1. New internal/books/ladder.go:
    - SearchWait(misses int) time.Duration: 0 for 0 misses, 24h for 1, 72h for 2, 7d for 3-10, 30d for 11 and up.
    - NextSearchAt(lastAt string, misses int) time.Time.
    - It never gives up. Keep it in books so [BOOK-08](#book-08) and the requests package can compute next_search_at.
    2. books.Repo.SearchStates(ctx) map[int64]SearchState{LastAt string; Misses int}: one query instead of a SearchState call per book.
    3. automation/books.go SearchBooksMissing:
    - Delete bookSearchAttempts and the giveUp branch.
    - Collect the due monitored books that aren't downloading, sort them by last_search_at (never-searched first), and search at most 40 per sweep (const bookSweepCap = 40). Log 'books: N due, searched 40, rest next sweep' at Debug.
    - Log once at Info when a book moves to monthly checks (misses 10 → 11).
    - Books that previously gave up become due again. The cap and oldest-first order spread that backlog over the first sweeps.
    4. No miss when every indexer errored:
    - Change searchBookOnce to return (grabbed int, searched bool, err). Have grabBookEdition report searched = at least one indexer answered without error (searchBook already returns res.Errors), separately from grabbed.
    - When !searched, don't call RecordSearchMiss. Log Warn 'book search: every indexer failed — not counted as a miss'.
    - Use ACQ's shared helper if it has landed (audit quick win #3: coordinator.go:550-557, books.go:77), so books and movies share one rule.
    5. Inject the clock (a Coordinator.now func defaulting to time.Now) so the sweep can be tested.
  - **Files:** `internal/books/ladder.go`, `internal/books/ladder_test.go`, `internal/books/repo.go`, `internal/automation/books.go`, `internal/automation/booksearchwait_test.go`
  - **Acceptance:**
    - A monitored, missing book with 2 misses is searched again 3 days after its last search and weekly after that. It is never permanently skipped
    - After 11 misses it is searched every 30 days, and the transition is logged once
    - A sweep with 200 due books runs at most 40 searches, oldest first, and leaves the rest for the next run
    - A sweep where every indexer errored does not increase search_misses
  - **Tests:** Go: ladder_test table for SearchWait and NextSearchAt at 0, 1, 2, 3, 10, 11 and 50 misses; Go: rewrite booksearchwait_test: a book with misses=5 is skipped at 6 days and searched at 7 days (injected clock); the per-sweep cap is respected; never-searched books go first; Go: a fake indexer service where every indexer errors: no RecordSearchMiss
  - **Depends on:** ACQ — shared rule 'don't record a search miss when every indexer errored' (audit quick win #3); implement locally if ACQ hasn't landed and switch later
  - **Risk:** More automatic searches against private trackers (MAM rate limits). The 40-per-sweep cap and the monthly tail bound the load. The first sweeps after deploy work through every previously abandoned book, so watch the MAM throttle logs that day.
  - **Resolves:** books-3

#### Milestone: M2 — Wanted books arrive, and the app says where they are

_RSS no longer grabs 'The Institute' for 'It'. Accented and apostrophe titles match. MyAnonaMouse uploads are picked up within one 15-minute RSS cycle. BookDetail, My shelf and the requests row say 'Not found yet — next check <date>' instead of 'Arrmada is searching'._

<a id="book-05"></a>
- [ ] **BOOK-05 · Book RSS uses the word-boundary matcher the search path uses, and only grabs wanted editions** — `P1` · `S` · Phase 4
  - **Problem:** RSSSyncBooks (automation/books.go:1807-1880) filters with releasesForBook (books.go:1884-1896): strings.Contains over normTitle, which keeps only letters and digits, plus a substring check on the author.
- 'It' matches 'The Institute' and any title containing 'Edition'. That grab fails the import gate and lands in Review.
- 'Dune' matches 'Dune Messiah' and 'Children of Dune'. If the sequel isn't in the library, word-boundary MatchByRelease resolves it to 'Dune' on import and the sequel is imported as Dune.

RSS also grabs any missing edition whether or not the profile wants it, skips the 'grabbed' timeline event and learnBookSeries, and bookDownloading runs MatchByRelease, a full books-table scan, for every queue item and every book. The comment at books.go:180-186 claiming RSS already gated titles is stale.
  - **Approach:** 1. RSSSyncBooks builds match := c.books.Matcher(ctx) once per cycle.
    2. Refactor releasesForThisBook (books.go:228) into releasesForBookWith(match, b, rels), which the search path also calls. Delete releasesForBook.
    3. bookDownloading takes the same match snapshot instead of calling MatchByRelease per item, in both SearchBooksMissing and RSSSyncBooks.
    4. Per book, compute wantE, wantA := books.WantedEditions(sp.FormatScores) and skip editions the profile doesn't want. That becomes the effective-editions helper if [BOOK-13](#book-13) introduces one.
    5. On an RSS grab, call c.learnBookSeries(ctx, b, *best) and c.books.AddEvent 'grabbed' with the same detail text grabBookEdition writes ('Grabbed the ebook edition from <indexer>: <release>').
    6. Correct the stale comment at books.go:180-186.
  - **Files:** `internal/automation/books.go`, `internal/automation/books_rss_test.go`
  - **Acceptance:**
    - An RSS item 'Stephen King - The Institute (2019) EPUB' is not grabbed for the monitored book 'It'
    - 'Frank Herbert - Dune Messiah EPUB' is not grabbed for 'Dune'
    - 'Stephen King - It (1986) EPUB' is still grabbed for 'It'
    - An RSS grab shows on the book's timeline as 'Grabbed the ebook edition from …', and an Ebook-only book never gets an audiobook grabbed by RSS
  - **Tests:** Go: new books_rss_test.go with a fake Recent indexer and a fake download client, covering It/Institute, Dune/Dune Messiah, the positive cases (copied from books/match_test.go) and the unwanted-edition case, asserting which releases were grabbed; Go: bookDownloading reads the library once per cycle (counting repo stub)
  - **Risk:** Low. A release for a sequel that isn't in the library can still pass as the base title, which is the same known limit as the search path. The import identity gate remains the backstop.
  - **Resolves:** books-4
<a id="book-06"></a>
- [ ] **BOOK-06 · One shared title normaliser: fold accents, apostrophes and '&' before matching** — `P1` · `S` · Phase 4
  - **Problem:** wordKey and NormKey keep only ASCII a-z0-9 (books/service.go:478-503), so:
- 'Pokémon' becomes 'pok mon'.
- 'Ender's Game' becomes 'ender s game'.
- The test pins 'L'Étranger' as 'l tranger' (match_test.go:71).

The search filter (releasesForThisBook), import routing (Matcher) and the file matcher (filematch.go:40-48) therefore throw away releases named 'Pokemon' or 'Enders.Game', and log 'no release matched this title'. Accented and apostrophe titles look unavailable. automation's normTitle already folds accents (store.go:494-502), but the books package never calls parser.FoldAccents.
  - **Approach:** 1. New internal/books/normalize.go with foldVariants(s string) []string:
    - Apply parser.FoldAccents. internal/parser has no internal deps, so the import is safe.
    - Lowercase.
    - Map ’ ‘ ʼ ` to an ASCII apostrophe, and '&' to ' and '.
    - Produce a joined variant with the apostrophe dropped ("ender's" becomes "enders", "l'étranger" becomes "letranger") and a split variant with the apostrophe as a space.
    - Each variant then goes through the existing wordKey reduction.
    2. matchRelease and containsWords: precompute the release's variants once and match when any title variant appears as whole words in any release variant. Do the same in filematch.matchFileName.
    3. NormKey, titlePartKey and authorKey (dedupe.go) use the folded text, so 'García Márquez' equals 'Garcia Marquez' and [BOOK-02](#book-02)'s identity folds accents too.
    4. Update the match_test.go:71-73 pins.
    5. Leave music alone (MUS owns music/match.go:150) but note in the commit that music should adopt foldVariants.
  - **Files:** `internal/books/normalize.go`, `internal/books/normalize_test.go`, `internal/books/service.go`, `internal/books/filematch.go`, `internal/books/dedupe.go`, `internal/books/match_test.go`, `internal/books/filematch_test.go`, `internal/books/dedupe_test.go`
  - **Acceptance:**
    - The library book 'Pokémon Adventures, Vol. 1' matches the release 'Pokemon Adventures Vol 1 EPUB' in search, import and file scan
    - 'Ender's Game' matches 'Orson.Scott.Card-Enders.Game.epub' and 'Ender s Game [M4B]'
    - 'Pride & Prejudice' matches 'Pride and Prejudice', and the reverse
    - Books by 'Gabriel García Márquez' match releases credited to 'Gabriel Garcia Marquez', and the two spellings give one identity
  - **Tests:** Go: match_test new table rows for accents, apostrophes both ways, curly apostrophes and '&'; Go: filematch_test equivalents; Go: dedupe_test IdentityOf('Pokémon …') equals IdentityOf('Pokemon …'); Go: automation releasesForBookWith test with an accented library title; Go: the existing It/Institute and Dune/Dune Messiah negative cases still pass
  - **Depends on:** [BOOK-02](#book-02), MUS — music matcher should adopt the same fold rules (audit quick win #13)
  - **Risk:** Matching becomes slightly looser, so rerun the word-boundary negative cases. Nothing persisted depends on NormKey output, since keys are computed at read time.
  - **Resolves:** books-5
<a id="book-07"></a>
- [ ] **BOOK-07 · MyAnonaMouse recent-uploads poll, so books have real RSS** — `P1` · `S` · Phase 4
  - **Problem:** RSSSyncBooks reads indexers.Recent(ctx, 100), but fetchRecent skips any searcher that isn't a Recenter (indexer/service.go:300-303). Only Torznab and 1337x implement Recent, so MAMSearcher, the owner's main book tracker, is never polled. A book uploaded to MAM between ladder checks waits days or weeks for its next scheduled search.
  - **Approach:** 1. internal/indexer/myanonamouse.go: factor the POST, parse and isMAMEmpty handling out of Search (lines 163-224) into m.query(ctx, idx, body mamSearchBody) ([]Release, error). The rotated mam_id still goes through persistRotatedSession, and do() keeps the throttle.
    2. Add (m *MAMSearcher) Recent(ctx, idx, limit) ([]Release, error) to satisfy indexer.Recenter. The body is mamTor{Text: "", SrchIn as in Search, SearchType: "all", SearchIn: "torrents", MainCat: idx.Categories or mamBookMainCats, SortType: "dateDesc", StartNumber: "0", PerPage: min(limit, 100)}, and results are mapped with releaseFrom.
    3. indexer.Service already caches Recent for the series RSS pass (service.go:34), so MAM costs one request per 15-minute cycle.
    4. RSSSyncBooks logs 'rss: N recent releases (M from MyAnonaMouse)' at Debug.
    5. The first deploy: check the debug line and the first response with the owner's session before relying on it.
  - **Files:** `internal/indexer/myanonamouse.go`, `internal/indexer/myanonamouse_test.go`, `internal/indexer/service.go`, `internal/automation/books.go`
  - **Acceptance:**
    - With a MAM indexer enabled, every rss-sync-books run fetches MAM's newest book uploads (visible in the debug log)
    - A new MAM upload for a monitored, wanted book is grabbed within one RSS cycle (about 15 minutes) without anyone pressing Search
    - Indexers without a feed are still skipped without errors, and a MAM 'no results' reply is an empty list, not an error
  - **Tests:** Go: myanonamouse_test with an httptest server asserting the POST body (empty text, sortType 'dateDesc', the book main_cat values, perpage <= 100) and that items map to releases with Format, Author and Language; Go: indexer service test where fetchRecent includes a MAM indexer
  - **Depends on:** [BOOK-05](#book-05)
  - **Risk:** MAM's behaviour with an empty search text and dateDesc is unverified live. Respect MAM's API etiquette: one call per cycle and the existing mamRequestDelay.
  - **Resolves:** books-3
<a id="book-08"></a>
- [ ] **BOOK-08 · Show the real search state: API fields, request tracking, BookDetail and My shelf copy** — `P1` · `M` · Phase 4
  - **Problem:** books.cols has no search state (repo.go:133-136). The UI doesn't know whether a book was ever found:
- BookDetail always says 'Wanted — no file yet. Arrmada is searching' (BookDetail.tsx:191-197).
- My shelf labels every approved request 'Searching' (MyBooks.tsx:187-190).
- Request progress falls through to StageSearching (progress.go:142-147).

A book that hasn't been found in a month looks identical to one that was just added.
  - **Approach:** 1. books repo: add last_search_at and search_misses to cols and scan, giving Book.LastSearchAt `json:"last_search_at,omitempty"` and Book.SearchMisses `json:"search_misses"`. Service.List and Service.Get fill the computed Book.NextSearchAt `json:"next_search_at,omitempty"` with books.NextSearchAt ([BOOK-04](#book-04)), left empty when the book has every wanted edition or isn't monitored.
    2. requests: Tracking gains NextCheckAt string `json:"next_check_at,omitempty"` (RFC3339). enrichAvailability carries the book's misses and next search time. In track()'s default branch for books with misses > 0, the Note is 'Not found yet' and NextCheckAt is set. The UI formats the date in the viewer's locale; the server never formats dates in copy.
    3. httpapi/mybooks.go: MyRequest gains stage, note and next_check_at, computed with Requests.Track over the caller's requests and the download queue. Read the queue once and tolerate a queue error by omitting stage.
    4. UI copy:
    - BookDetail.tsx edition row: misses 0 reads 'Wanted — searching'. Misses > 0 reads 'Not found yet — last checked {relative}, next check {Tue 14 Oct}', with the existing 'Search now' button beside it.
    - Books.tsx statusOf: 'Wanted' gets a title tooltip with the next check.
    - MyBooks.tsx RequestCard renders the stage and note ('Not found yet · next check Tue') instead of the hard-coded 'Searching'.
    - The Discover requests row already renders Tracking.Note and gains the date.
    5. web/src/lib/api.ts types.
  - **Files:** `internal/books/repo.go`, `internal/books/service.go`, `internal/requests/progress.go`, `internal/requests/service.go`, `internal/requests/progress_test.go`, `internal/httpapi/mybooks.go`, `internal/httpapi/mybooks_test.go`, `web/src/pages/BookDetail.tsx`, `web/src/pages/Books.tsx`, `web/src/pages/MyBooks.tsx`, `web/src/pages/Discover.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - GET /api/v1/books/{id} returns last_search_at, search_misses and next_search_at
    - BookDetail for a book with misses shows 'Not found yet — next check <date>', and 'Arrmada is searching' no longer appears for it
    - The requester's My shelf card and the Discover requests row show 'Not found yet' with the next check date instead of 'Searching'
    - A book with all wanted editions has no next_search_at
  - **Tests:** Go: repo round-trip of the search state in List and Get; Go: progress_test where a book request with misses gets the 'Not found yet' note and next_check_at; Go: mybooks_test where MyRequest carries stage and note; UI: BookDetail and My shelf copy for misses 0 vs 3, checked at 375px
  - **Depends on:** [BOOK-04](#book-04), [BOOK-03](#book-03), REQ — the Discover requests row is REQ's; only the note/date rendering is touched here
  - **Risk:** Low. Copy must stay in the existing type scale and palette. Server-side the change is additive JSON fields.
  - **Resolves:** books-3

#### Milestone: M3 — Duplicates are reviewed, and merges lose nothing

_Every key a book has ever had resolves to it, so old Discover cards and second requests attach to the right book. A manager reviews possible duplicates side by side and merges with one click. Editions, extra narrations, listening places, bookmarks, requests and history all move to the kept book, and no file is deleted._

<a id="book-09"></a>
- [ ] **BOOK-09 · book_keys alias table: every catalogue key a book has ever had** — `P1` · `M` · Phase 6
  - **Problem:** Catalogue keys stand in for identity, and Re-match, the Hardcover upgrade, canonical-id swaps and Add-hits-duplicate each replace or ignore a key. As a result:
- A Discover card still carrying an old key doesn't show In library.
- A second request for the same book from a card with a different key creates a second request.
- The series and Recommended owned checks miss books whose key changed.
  - **Approach:** 1. New migration NNNN_book_keys.sql:
    - CREATE TABLE book_keys (key TEXT PRIMARY KEY, book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE, source TEXT NOT NULL DEFAULT '', added_at TEXT NOT NULL DEFAULT (datetime('now'))), plus CREATE INDEX idx_book_keys_book ON book_keys(book_id).
    - Seed: INSERT OR IGNORE … SELECT ol_key, id, CASE WHEN ol_key LIKE 'hc:%' THEN 'hardcover' WHEN ol_key LIKE 'gb:%' THEN 'google' ELSE 'openlibrary' END FROM books.
    - INSERT OR IGNORE … SELECT ol_key, book_id, 'request' FROM requests WHERE media_type='book' AND book_id IS NOT NULL, which recovers pre-upgrade Open Library keys.
    - [BOOK-03](#book-03)'s Go backfill also calls AddKey for every request it links.
    2. books.Repo: AddKey(ctx, key, bookID, source), BookIDForKey(ctx, key) (int64, bool), KeysFor(ctx, bookID) and AllKeys(ctx) map[string]int64. Write paths:
    - Repo.Create (in the same statement batch).
    - Service.Add: alias the incoming olKey when GetBook returned a different canonical d.Key. On the ErrExists path, alias the incoming key to the existing row.
    - Service.Rematch and applyUpgrade: the old key stays and the new key is added. A Rematch onto a key aliased to another book returns ErrExists (409).
    - The disk-scan ErrExists path (automation/books.go:1403-1423).
    - Merge ([BOOK-11](#book-11)).
    3. Readers:
    - findByKey uses BookIDForKey.
    - enrichBookCards loads AllKeys once per request.
    - catalogue.go series-gap owned and the discover.go Recommended filter check aliases before identity.
    - requests: Create for books resolves in.OLKey to a book_id via BookIDForKey before inserting. lookupExisting then finds a request with that book_id or whose ol_key is any alias of it, and the caller is attached as a subscriber instead of a second request being created. This needs a pre-insert check, because the unique idx_requests_ol only catches identical keys.
    - GET /books/{id} returns aliases [{key, source}] for the detail page's 'Catalogue' line.
    4. Conflicts at seed time (two historic rows claiming one key): INSERT OR IGNORE, and log the count for [BOOK-12](#book-12)'s review.
  - **Files:** `internal/store/migrations/NNNN_book_keys.sql`, `internal/books/repo.go`, `internal/books/keys_test.go`, `internal/books/service.go`, `internal/books/upgrade.go`, `internal/books/dedupe.go`, `internal/books/catalogue.go`, `internal/books/discover.go`, `internal/automation/books.go`, `internal/requests/service.go`, `internal/requests/repo.go`, `internal/requests/service_test.go`, `internal/httpapi/books.go`, `web/src/pages/BookDetail.tsx`
  - **Acceptance:**
    - After the Hardcover upgrade, a Discover card that still carries a book's old Open Library key shows 'In library'
    - Requesting the same book from an OL-key card and an HC-key card gives one request, with the second user subscribed (POST returns subscribed=true)
    - GET /api/v1/books/{id} lists the previous key in aliases after a Change match
    - Re-matching a book onto a key that is another book's alias returns 409
  - **Tests:** Go: repo tests for AddKey, BookIDForKey, KeysFor and AllKeys, including a cascade on book delete; Go: Rematch keeps the old alias; Add with a canonical swap stores both keys; Go: requests.Create with an alias key attaches to the existing request; Go: enrichBookCards alias hit marks in_library
  - **Depends on:** [BOOK-03](#book-03)
  - **Risk:** Historic duplicate rows may claim the same key. INSERT OR IGNORE keeps the first, and the review in BOOK-12 resolves the rest. Keys are namespaced (hc:, gb:, OL…W), so cross-catalogue collisions are not expected.
  - **Resolves:** books-1
<a id="book-10"></a>
- [ ] **BOOK-10 · listening: re-key item ids inside a transaction, with redirects for late reports from listening apps** — `P1` · `M` · Phase 13
  - **Problem:** Listening rows are keyed by item_key 'b<bookID>' or 'b<bookID>v<versionID>' (audioserver/catalog.go:35-40) in listen_progress (PK user_id,item_key), listen_history, listen_sessions and listen_bookmarks (0084). Nothing can move them: the only UPDATE on listen_progress changes the hidden flag (listening/store.go:240). Any merge therefore orphans everyone's place and bookmarks. Listening apps (Lissen) cache item ids, and offline uploads and progress PATCHes for the old id would 404 after a merge.
  - **Approach:** 1. New internal/listening/rekey.go: (s *Store) RekeyItemsTx(ctx, tx *sql.Tx, moves map[string]string) (RekeyCounts, error). For each old → new pair:
    - listen_progress: per (user_id, old) row, if a (user_id, new) row exists, keep the row with the larger updated_at by copying all of the winner's columns, pending_* included, into the new row. Otherwise UPDATE item_key. Then delete the old row.
    - listen_history: UPDATE item_key, then apply the existing per-(user,item) trim (store.go:314 query) to the new key.
    - listen_sessions: UPDATE item_key, so open sessions keep syncing.
    - listen_bookmarks: INSERT OR IGNORE … SELECT with the new key, then DELETE the old rows.
    - RekeyCounts holds row counts only.
    2. New migration NNNN_listen_item_redirects.sql: CREATE TABLE listen_item_redirects (old_key TEXT PRIMARY KEY, new_key TEXT NOT NULL, created_at INTEGER NOT NULL). It has no user column.
    - RekeyItemsTx inserts redirects and collapses chains: UPDATE … SET new_key=? WHERE new_key=old.
    - Store.ResolveItem(ctx, key) string follows the table.
    3. audioserver: add Server.resolveKey(ctx, key), which tries the key and, when the book or version isn't found, follows ResolveItem once. Call it at the top of item() (catalog.go:108) and in handleGetProgress, handlePatchProgress, handleBatchProgress, sync and the offline-session upload path (handlers_play.go), and in handleBatchGet (handlers_library.go). Pass the resolved key to every listening.Store write.
    4. Privacy: log 're-keyed N progress / M bookmark rows' only, never user and item pairs.
    5. Coordinate with AUD, which has its own fixes queued in handlers_play.go (empty-body close, finished books restart at 0), to avoid conflicting edits.
  - **Files:** `internal/listening/rekey.go`, `internal/listening/rekey_test.go`, `internal/listening/store.go`, `internal/store/migrations/NNNN_listen_item_redirects.sql`, `internal/audioserver/catalog.go`, `internal/audioserver/handlers_play.go`, `internal/audioserver/handlers_library.go`, `internal/audioserver/server_test.go`
  - **Acceptance:**
    - After RekeyItemsTx('b5' → 'b9'), a user's position, finished flag and bookmarks on b5 appear on b9
    - When both keys have progress for one user, the newer updated_at wins and pending_* guard fields come with it
    - A progress PATCH or offline-session upload for 'b5' after the move lands on 'b9'
    - Nothing in the log names a user together with an item
  - **Tests:** Go: rekey_test seeding progress, history, sessions and bookmarks on 'b5' and 'b5v2', moving them to 'b9' and 'b9v2', and asserting the moved rows, conflict resolution and trimmed history; Go: rekey_test with a failing statement inside the tx rolls everything back; Go: audioserver server_test where a sync for a redirected key writes progress under the new key
  - **Depends on:** AUD — coordinate edits to internal/audioserver/handlers_play.go with AUD's sync fixes
  - **Risk:** It touches the sync guard's pending_* columns, so the conflict copy must carry them intact or a guarded backwards jump could be applied. Test with Lissen after deploy: a re-keyed item shows up under the kept book's id on the next library refresh.
  - **Resolves:** backend-11
<a id="book-11"></a>
- [ ] **BOOK-11 · Lossless Service.Merge: one transaction that moves editions, versions, listening places, requests, aliases and history, and never deletes a file** — `P1` · `M` · Phase 13
  - **Problem:** With automatic merging gone (BOOK-01), the library still needs a way to fold real duplicates, such as one novel under an Open Library key and a Hardcover key. The old foldInto:
- dropped the duplicate's audio versions through the cascade;
- orphaned listening rows;
- dropped the duplicate's audiobook record when both rows had one;
- didn't re-point requests, grabs, blocklist, reviews or timeline events;
- ran outside a transaction.
  - **Approach:** New internal/books/merge.go: (s *Service) Merge(ctx, keepID, dropID int64) (MergeResult, error). It runs in one db.BeginTx, or BE's store.WithTx if that has landed, and every step below happens inside it:
    1. Load both rows with their versions. keepID == dropID returns an error.
    2. Ebook edition:
    - If keep lacks one, move drop's ebook_* columns over.
    - Otherwise leave drop's file on disk and add its path to MergeResult.LeftFiles.
    3. Audiobook edition:
    - If keep lacks one, move drop's audiobook_* columns over. The move is 'b<drop>' → 'b<keep>'.
    - Otherwise INSERT drop's main audiobook into book_audio_versions with book_id=keep, label 'Merged copy', terms '', monitored 0 (so the sweep never searches for it) and path/format/size/files. The move is 'b<drop>' → 'b<keep>v<newVid>'.
    4. UPDATE book_audio_versions SET book_id=keep WHERE book_id=drop. Each version's move is 'b<drop>v<vid>' → 'b<keep>v<vid>'.
    5. Listening: s.items.RekeyItemsTx(ctx, tx, moves) ([BOOK-10](#book-10)). Wire it through an interface field ItemRekeyer set in cmd/arrmada/main.go, so books doesn't depend on listening in tests.
    6. Re-point the rows that reference drop:
    - UPDATE requests SET book_id=keep WHERE book_id=drop ([BOOK-03](#book-03)).
    - UPDATE book_keys SET book_id=keep WHERE book_id=drop, plus AddKey(drop.OLKey) ([BOOK-09](#book-09)).
    - UPDATE book_events SET book_id=keep WHERE book_id=drop.
    - UPDATE grabs SET movie_id=keep WHERE media_type='book' AND movie_id=drop, and the same for blocklist.
    - UPDATE import_reviews SET expected_id=keep WHERE media_type='book' AND expected_id=drop.
    7. Carry over as foldInto did:
    - monitored (OR);
    - series name, position and key when keep lacks them;
    - description, cover and subjects when keep lacks them.
    Keep's quality profile stays. When drop wanted an edition keep doesn't, add a note to the event.
    8. DELETE drop. AddEvent(keep, 'merged', 'Merged “<title>” (<key>): moved ebook/audiobook/N versions; left on disk: <paths>').
    
    After commit, the caller publishes 'book.imported' {id: keep, edition: 'merge'} so the audioserver cache (cache.go:110) refreshes. Merge never touches files. Log counts only.
  - **Files:** `internal/books/merge.go`, `internal/books/merge_test.go`, `internal/books/repo.go`, `internal/books/versions.go`, `internal/books/service.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - Merging two rows where both have audiobooks leaves the dropped row's audiobook as a 'Merged copy' audio version on the kept row, with no untracked file
    - A listener's place and bookmarks in the dropped book (main edition or extra version) appear on the kept book in Lissen after a merge
    - A request linked to the dropped book shows the kept book's availability
    - A failure mid-merge leaves both books, all versions and all listening rows exactly as they were
    - Merge never deletes a file, and a leftover ebook path is reported in the result and the 'merged' event
  - **Tests:** Go: merge_test covering moved editions, the main audiobook turned into a version, re-parented versions, listen_progress re-keyed (newer wins on conflict), requests.book_id re-pointed, aliases moved, book_events, grabs and blocklist re-pointed, the drop row gone, and the left ebook path reported; Go: merge_test with an injected failure (rekeyer returns an error): full rollback; Go: merge_test keepID == dropID returns an error
  - **Depends on:** [BOOK-10](#book-10), [BOOK-03](#book-03), [BOOK-09](#book-09), BE — store.WithTx helper (optional)
  - **Risk:** The transaction must cover every table, or a crash mid-merge leaves split state. Keep all statements on the tx. Two very large libraries could hold the write lock for a moment, which is acceptable for a manual action.
  - **Resolves:** books-2, backend-11
<a id="book-12"></a>
- [ ] **BOOK-12 · Possible duplicates review: preview, merge or dismiss, plus the list of earlier automatic merges** — `P1` · `M` · Phase 13
  - **Problem:** Book dedupe used to be an unconditional boot step, and the only manual action merged every detected group blind, with no preview and no way to say 'these are different books'. Books the old boot merge deleted are recorded only as 'merged' timeline events on the keeper, so the owner can't see what was lost.
  - **Approach:** 1. New migration NNNN_book_dupe_reviews.sql: CREATE TABLE book_dupe_reviews (a_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE, b_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE, reason TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'open' /* open | ignored */, created_at TEXT NOT NULL DEFAULT (datetime('now')), PRIMARY KEY (a_id, b_id), CHECK (a_id < b_id)).
    2. books.Service.PossibleDuplicates(ctx) ([]DupGroup, error). Pairs come from:
    - SameBook ([BOOK-02](#book-02)), reason 'Same title and author'.
    - A weaker tier: SameTitle with authorsOverlap but different author keys, reason 'Same title, similar author'.
    - Open flagged rows, reason e.g. 'Hardcover lists both as the same book'.
    Pairs with status 'ignored' are removed, and the rest are grouped transitively. Each group carries suggested_keep_id (most editions, then most audio versions, then the oldest id) and per-book details: cover, title, author, catalogue source and key, ebook and audiobook paths/format/size, audio version count, monitored and added_at. Listening data is never included.
    3. upgrade.go's flag path from [BOOK-01](#book-01) now also calls s.flagDuplicate(a, b, reason), an INSERT OR IGNORE with status open.
    4. Routes, all requireRole(RoleManager):
    - GET /api/v1/books/duplicates returns {groups, past_merges}. past_merges = book_events WHERE event='merged' ORDER BY id DESC LIMIT 200, as {book_id, keeper_title, detail, created_at}.
    - POST /api/v1/books/duplicates/merge {keep_id, drop_ids[]}. The server recomputes groups and returns 400 unless every drop is in keep's group, then calls Merge ([BOOK-11](#book-11)) for each drop and publishes book.imported.
    - POST /api/v1/books/duplicates/ignore {ids[]} marks every pair in the set as ignored.
    These don't conflict with the existing /books/{id}/… patterns.
    5. UI:
    - New web/src/components/DuplicatesModal.tsx, opened from a 'Possible duplicates (n)' chip in the Books header. [BOOK-25](#book-25) moves it into the Library tools menu.
    - Each group shows side-by-side cards with a 'Keep this one' choice (preselected suggestion), 'Merge' and 'Not the same'.
    - A static note: 'Listening places, bookmarks and requests move to the kept book. No files are deleted.'
    - After a merge, list any left-over file paths.
    - A collapsed 'Earlier automatic merges' section lists past_merges, each with a 'Find on Discover' link (books discover search for the title) so the owner can re-add a lost book.
    - Use the existing modal and card styles, or the FE kit's Modal if it has landed.
  - **Files:** `internal/store/migrations/NNNN_book_dupe_reviews.sql`, `internal/books/dupes.go`, `internal/books/dupes_test.go`, `internal/books/upgrade.go`, `internal/httpapi/books.go`, `internal/httpapi/server.go`, `web/src/components/DuplicatesModal.tsx`, `web/src/pages/Books.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - GET /api/v1/books/duplicates lists an OL-key row and an HC-key row of the same novel, and does not list 'Thrawn' with 'Thrawn: Alliances'
    - Nothing merges without a click. Merging from the modal keeps listening places and turns a second audiobook into a 'Merged copy' version
    - 'Not the same' hides the pair permanently, across reloads and restarts
    - The modal lists earlier automatic merges with titles and dates and shows no listening data
    - Merge with a drop id outside keep's group returns 400
  - **Tests:** Go: PossibleDuplicates excludes ignored pairs and prefix siblings, includes flagged pairs, groups transitively and orders the suggested keeper correctly; Go: the merge endpoint rejects ids not in one group (manager role required; requester 403); Go: the upgrade flag path writes a book_dupe_reviews row; UI: open the modal, click Not the same, reload, and the pair is gone; merge flow at 375px and 1440px
  - **Depends on:** [BOOK-11](#book-11), [BOOK-02](#book-02), FE — Modal/component kit if available
  - **Risk:** A manager could still merge two different books by mistake. Merge is lossless and reports leftovers, and a mistaken merge can be undone by hand with Add plus Manual import. That limitation is stated in the modal.
  - **Resolves:** books-2, backend-11

#### Milestone: M4 — Read, listen, or both

_Requesters choose Read, Listen or Both. A book that has only an ebook offers 'Request audiobook'. Existing books are widened rather than refused. 'Ready' messages say what actually arrived, once per requested format._

<a id="book-13"></a>
- [ ] **BOOK-13 · Read / Listen / Both on book requests: stored formats, widening existing books, per-format readiness and 'ready' wording** — `P1` · `M` · Phase 6
  - **Problem:** Book requests can't say which format the requester wants.
- BooksDiscover.createRequest sends no format (BooksDiscover.tsx:63-70) and the admin approve buttons pass no profile (Requests.tsx:111, Discover.tsx:641), so every book request gets DefaultProfile('book').
- If a requester asked for the other format of an existing book, Approve would hit books.ErrExists and never widen the book's profile.
- HasFile means ebook OR audiobook (books/repo.go:49,167), and the ready message is always 'ready to read' (usernotify.go:185-187). Someone who wanted the audiobook is told an EPUB is 'ready to read'.
  - **Approach:** 1. New migration NNNN_request_formats.sql: ALTER TABLE requests ADD COLUMN formats TEXT NOT NULL DEFAULT ''. '' means a legacy request (behaviour unchanged); otherwise the value is 'ebook', 'audiobook' or 'both'.
    2. Move automation.bookProfileFor (automation/books.go:1437-1449) into quality.Service.BookProfileFor(ctx, ebook, audio bool) string, and have automation call it. There is no books.want_formats override column: book profiles are exactly the three edition presets (migration 0032), so the profile already is the per-book format choice, and a second override would make the five WantedEditions callers disagree.
    3. handleCreateRequest (httpapi/requests.go:45), media_type 'book':
    - Accept formats (ebook|audiobook|both). Any other value returns 400.
    - When absent, default to the editions of the owner's default book profile.
    - Store formats and set quality_profile = BookProfileFor(formats).
    - Request JSON gains formats.
    4. requests.Service:
    - attachToExisting for books unions formats (repo.SetFormats plus repo.SetProfile). If the request is approved and a library book exists (book_id, or an alias via [BOOK-09](#book-09)), widen the book.
    - Approve 'book': an explicit admin profile wins and sets formats = WantedEditions(profile). After books.Add (new, ErrExists or existing book_id), compute union = the book's current wanted editions ∪ the request's formats. If the union is wider, call books.SetQualityProfile(BookProfileFor(union)) and AddEvent 'Now also wants the audiobook (requested)'. Then SearchBookNow if a requested edition is missing. Never narrow a book.
    5. Readiness helper bookReady(formats string, b books.Book) bool:
    - '' → HasFile.
    - ebook → Ebook != nil.
    - audiobook → Audiobook != nil or a version with a file.
    - both → both.
    Use it in enrichAvailability, SweepReadyRequests and My shelf. For a 'both' request with one format present, Track returns StagePartial with Note 'Ebook ready · audiobook on the way'.
    6. Notifications for formats != '':
    - On book.imported, read the event's 'edition'. When it is in the request's formats, send '“X” is ready to read.' (ebook) or 'The audiobook of “X” is ready to listen.' with ref requestRef(req)+':'+edition.
    - The sweep sends one notification per present requested edition; the refs keep it idempotent.
    - Legacy '' requests keep today's single ref and wording, so nothing re-fires on deploy.
  - **Files:** `internal/store/migrations/NNNN_request_formats.sql`, `internal/quality/service.go`, `internal/automation/books.go`, `internal/httpapi/requests.go`, `internal/requests/repo.go`, `internal/requests/service.go`, `internal/requests/usernotify.go`, `internal/requests/progress.go`, `internal/requests/service_test.go`, `internal/requests/usernotify_test.go`, `internal/httpapi/mybooks.go`
  - **Acceptance:**
    - A requester who picks Listen gets a request on the Audiobook preset, and approval searches only for the audiobook
    - A second requester asking for the other format of an already-requested book is subscribed, and the request now shows formats 'both'
    - Requesting the audiobook of a book that has only an ebook widens the book to Ebook + Audiobook and starts an audiobook search; a narrower request never narrows a book
    - A 'Listen' request whose ebook arrives first gets no 'ready'. When the audiobook lands the message says it's ready to listen. A 'Both' request gets one message per format
    - Existing (legacy) requests get no new notifications on deploy
  - **Tests:** Go: requests Create with formats maps to the right preset; an invalid formats value returns 400; Go: attachToExisting unions formats, widens the book profile and searches (fake coordinator); Go: Approve with ErrExists widens the existing book's profile and never narrows it; Go: bookReady table and enrichAvailability per-format readiness; Go: usernotify_test per-edition wording, refs, and legacy single-ref behaviour
  - **Depends on:** [BOOK-03](#book-03), [BOOK-09](#book-09), REQ — coordinate with REQ's request-model changes in internal/requests/*
  - **Risk:** If the owner's default is 'Ebook + Audiobook', requesters who pick one format get less than before, which is intended. The modal defaults to the owner's default editions. Widening a profile changes what the sweep searches, which is also intended.
  - **Resolves:** books-6, product-11
<a id="book-14"></a>
- [ ] **BOOK-14 · Discover and request UI for formats: Read / Listen / Both control, per-format badges, 'Request audiobook', format badges in request lists** — `P1` · `M` · Phase 6
  - **Problem:** Once a book has either format, badgeFor returns 'In library' (BooksDiscover.tsx:438-446, 596-601) and hides Request, so a listener who finds only an ebook can't ask for the audiobook. The request modal offers no format choice. Admin request lists don't show what was asked for.
  - **Approach:** 1. httpapi/books.go enrichBookCards adds has_ebook, has_audiobook, want_ebook and want_audiobook from the library row, plus request_formats when the card is requested. The discover payload also exposes default_book_formats, the editions of the owner's default book profile.
    2. BooksDiscover.tsx:
    - BookRequestModal gets a segmented control: 'Read (ebook)', 'Listen (audiobook)', 'Both'. The default is the viewer's last choice, held in localStorage with every access wrapped in try/catch, else default_book_formats.
    - badgeFor becomes per-format. 'Ebook ✓' plus a 'Request audiobook' button when only the ebook exists; 'Audiobook ✓' plus 'Request ebook' for the reverse. Both present reads 'Ebook ✓ · Audiobook ✓'.
    - The card's quick request uses the last choice.
    3. Requests.tsx and the Discover requests row show a small format badge ('Read', 'Listen', 'Both'). The admin approve control keeps its profile override.
    4. MyBooks.tsx 'on the way' shows which format is still coming ('Audiobook on the way').
    5. api.ts: a formats field on createRequest and the request types.
    Keep the existing visual style and type scale.
  - **Files:** `internal/httpapi/books.go`, `web/src/pages/BooksDiscover.tsx`, `web/src/pages/Requests.tsx`, `web/src/pages/Discover.tsx`, `web/src/pages/MyBooks.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - For a book with only an ebook on disk, the Discover card reads 'Ebook ✓ · Request audiobook', and requesting widens the book
    - The request modal offers Read / Listen / Both and remembers the last choice (and still works in a private window)
    - Admin request rows and the Discover requests row show the requested format
    - My shelf shows which format is still on the way for a partly ready request
  - **Tests:** Go: enrichBookCards sets per-format fields; UI: modal control defaulting and memory; per-format badges on a book with one edition; layout at 375px
  - **Depends on:** [BOOK-13](#book-13)
  - **Risk:** Low. UI only, on top of BOOK-13's API.
  - **Resolves:** books-6, product-11

#### Milestone: M5 — Grab the right edition

_A release the admin picks by hand imports into the book it was picked for. Automatic grabs reject releases in a language the owner doesn't read._

<a id="book-15"></a>
- [ ] **BOOK-15 · Hand-picked book grabs import into the book they were grabbed for** — `P2` · `S` · Phase 13
  - **Problem:** recordBookGrab (automation/books.go:1652-1667) never sets grabs.manual, so interactive and uploaded-torrent book grabs look automatic. The import identity gate (automation/books.go:595-618) re-checks the release name. A release the admin picked by hand whose name doesn't contain the library title (an accent, an alternate title, a translated series name) therefore goes to Review instead of importing.
  - **Approach:** 1. recordBookGrab gains manual bool and writes grabs.manual (column from 0076).
    - GrabForBook (POST /books/{id}/grab), handleBookGrabTorrent (POST /books/{id}/grabtorrent) and the audio-version manual grab in books_versions.go pass true.
    - The sweep, RSS and stall failover pass false.
    2. Extend grabbedMediaForHash (automation/reviews.go:125) to return the manual flag, or add grabIsManual(ctx, hash, name).
    3. In ImportBookDownloads, when the grab is manual and the expected book exists, set b = expected and ok = true, skipping the name gate. divertForeignBookFiles still splits packs.
    4. The timeline event reads 'Imported the ebook edition (hand-picked) from …'.
  - **Files:** `internal/automation/books.go`, `internal/automation/reviews.go`, `internal/automation/books_versions.go`, `internal/httpapi/books.go`, `internal/automation/manualgrab_test.go`
  - **Acceptance:**
    - Picking a release whose name doesn't contain the library title from 'Search indexers' imports it into that book with no Review entry
    - An automatic grab whose download resolves to a different book still goes to Review
  - **Tests:** Go: ImportBookDownloads with a manual grab whose name doesn't match imports into the expected book; Go: an automatic mismatched grab still creates a review
  - **Risk:** A mis-click imports the wrong content into a book. Movies already make this trade-off for manual grabs, and the timeline makes it visible.
  - **Resolves:** books-5
<a id="book-16"></a>
- [ ] **BOOK-16 · Preferred book languages: reject wrong-language releases in automatic grabs** — `P2` · `S` · Phase 13
  - **Problem:** bookRelScore (automation/books.go:544-562) is format score plus keyword score, times 1e6, plus seeders. It never reads rel.Language, and the MAM search body has no language filter (myanonamouse.go:103-112, 186-197). A German or Spanish edition that keeps the original title ('Dune', 'Fourth Wing') can win on seeders and count as the book being done. For an audiobook that means hours in the wrong language. The only workaround is a reject term like 'Language: DEU', which nobody would find.
  - **Approach:** 1. Setting books_languages (comma list of ISO 639-1 codes, default 'en') in settings, editable under Settings → Books next to the module toggle (httpapi/settings.go, Settings.tsx).
    2. indexer/myanonamouse.go releaseFrom: normalise MAM's lang_code with normLang (ENG→en, GER/DEU→de, SPA→es, FRE/FRA→fr, ITA→it, DUT/NLD→nl, POR→pt, SWE→sv, …; unknown codes pass through lower-cased) into Release.Language. Keep 'Language: xx' in Description.
    3. When the configured languages map to known MAM ids (a table in code: English=1, etc.), send browse_lang in mamTor. Verify the ids against one real response first.
    4. automation: bookRelScore takes the allowed set and returns ok=false when rel.Language != '' and isn't allowed.
    - RankBookReleases still lists those releases, marked rejected with 'Language: de — not in your book languages', so the admin can grab one by hand.
    - Releases with no language (most Torznab) are unaffected.
    5. Optional: make the Hardcover author-works language filter (hardcover_authorworks.go:74) follow the setting.
  - **Files:** `internal/httpapi/settings.go`, `internal/indexer/myanonamouse.go`, `internal/indexer/myanonamouse_test.go`, `internal/automation/books.go`, `internal/automation/books_scoring_test.go`, `internal/metadata/hardcover_authorworks.go`, `web/src/pages/Settings.tsx`
  - **Acceptance:**
    - With languages = en, the automatic sweep and RSS never grab a MAM release whose lang_code is GER
    - Interactive search shows that release with 'Language: de — not in your book languages' and still lets the admin grab it
    - Setting 'en,es' allows Spanish releases
  - **Tests:** Go: bookRelScore table (Language '', en, de with allowed {en} and {en,es}); Go: normLang table; Go: MAM search body includes browse_lang when the setting maps to known ids; Go: RankBookReleases marks the release rejected with the reason
  - **Risk:** MAM's lang_code values and browse_lang ids are unverified live, so check one real search response first. Translations on general trackers with no language field still pass the title gate.
  - **Resolves:** books-13

#### Milestone: M6 — Know what's coming

_Books added in bulk get descriptions, genres and series within a day. Upcoming books show 'Out <date>', aren't searched before release, and are searched daily from release day._

<a id="book-17"></a>
- [ ] **BOOK-17 · Budget-aware scheduled book metadata refresh, and retire 'Find series' when Hardcover is the source** — `P2` · `S` · Phase 13
  - **Problem:** Service.Refresh has a single caller, the manual 'Refresh & rescan' button (httpapi/books.go:202).
- Books created by AddWorks ('Add author') have no description, genres or series key, despite the comment saying they 'fill in on the next refresh' (service.go:209-212).
- 'Add missing in series' books do get a series key (catalogue.go:118-129) but no description or genres.
- 'Find series' runs one indexer search per unlabelled book (automation/books.go:309-339) even when Hardcover returns the series directly.
  - **Approach:** 1. New migration NNNN_book_meta_checked.sql: ALTER TABLE books ADD COLUMN meta_checked_at TEXT NOT NULL DEFAULT ''.
    2. Service.Refresh stamps meta_checked_at.
    3. Expose BookSources.HardcoverUsage() (used, budget int) in metadata/bookswitch.go, wrapping Hardcover.Usage (hardcover.go:54), and read it through an interface assertion.
    4. New Service.RefreshDue(ctx, limit int) (refreshed int, skipped string):
    - Never-checked ('') books first, then those older than 30 days, oldest first.
    - Skip the run when Hardcover usage is above 70% of the daily budget.
    - Refresh each book. Refresh also fills series, author and release_date once [BOOK-18](#book-18) and [BOOK-19](#book-19) land.
    - Stop on ErrHardcoverBudget.
    5. cmd/arrmada/main.go: sched.Register("refresh-books", 6*time.Hour, false, …) with limit 25 (about 100 a day). Log 'refresh-books: refreshed N' or 'skipped: Hardcover budget'.
    6. handleAddAuthor's background goroutine and AddMissingInSeries refresh each new book before its first search, so searches use the catalogue's corrected author and series. Budget-aware, falling back to the plain search.
    7. Books.tsx: hide 'Find series' when the metadata source is Hardcover. For Open Library setups, keep it with a tooltip saying it searches indexers.
  - **Files:** `internal/store/migrations/NNNN_book_meta_checked.sql`, `internal/books/service.go`, `internal/books/repo.go`, `internal/books/catalogue.go`, `internal/books/refresh_test.go`, `internal/metadata/bookswitch.go`, `internal/httpapi/books.go`, `cmd/arrmada/main.go`, `web/src/pages/Books.tsx`
  - **Acceptance:**
    - Within a day of adding an author, the new books show descriptions, genres and series on their pages
    - The refresh-books job logs how many books it refreshed, or that it skipped because the Hardcover budget was low
    - 'Find series' is not shown when Hardcover is the source
  - **Tests:** Go: RefreshDue picks never-checked books first, respects the limit, skips under the budget gate (fake usage) and stamps meta_checked_at
  - **Risk:** Hardcover's daily budget is shared with Discover. The limits are conservative, and the 70% gate leaves headroom for interactive use.
  - **Resolves:** books-9
<a id="book-18"></a>
- [ ] **BOOK-18 · Store release dates, search on release day, and say 'Out <date>'** — `P2` · `M` · Phase 13
  - **Problem:** Books carry only a year. A book requested before release is searched at add time and then on the ladder, and nothing lines a search up with publication. Request progress hard-codes released: true for books (requests/service.go:289), so 'Not out yet' never shows and requesters see 'Searching' for a book that doesn't exist yet. Early searches against an unreleased book also burn ladder misses.
  - **Approach:** 1. metadata/hardcover.go:
    - Add release_date to hcBookFields and the hcBook struct (*string).
    - Add BookResult.ReleaseDate and BookDetails.ReleaseDate (YYYY-MM-DD), filled by result().
    - Add the same field to the author-works query (hardcover_authorworks.go).
    - Open Library leaves it empty.
    - hardcover_browse.go already filters on release_date, so the field exists. After deploy, check the log first: a wrong field name breaks every GetBook.
    2. New migration NNNN_book_release_date.sql: ALTER TABLE books ADD COLUMN release_date TEXT NOT NULL DEFAULT ''.
    3. books repo: release_date in cols and scan, Create, UpdateMeta and Rematch. Set it in Service.Add, Refresh, applyUpgrade, AddWorks and AddMissingInSeries.
    4. SearchBooksMissing:
    - Skip books with release_date > today.
    - On the first sweep on or after release_date where last_search_at < release_date, call the new repo.ResetForRelease (misses=0 without the 'grabbed' semantics) and search.
    - For 14 days after release, wait at most 24h between searches.
    - The add-time search of an unreleased book never records a miss.
    5. requests enrichAvailability: released = release_date == '' || release_date <= today. Tracking.Note becomes 'Not out yet' and NextCheckAt becomes the release date. The UI renders 'Out 12 Mar — we'll grab it then'.
    6. UI:
    - BookDetail gets a release chip ('Out 12 Mar 2027'), and the edition row reads 'Not out yet — we'll search on 12 Mar'.
    - The BooksDiscover request modal shows the date for upcoming books.
    - The My shelf request card shows 'Out 12 Mar'.
    7. Existing books pick up dates through [BOOK-17](#book-17)'s refresh job.
  - **Files:** `internal/metadata/hardcover.go`, `internal/metadata/hardcover_authorworks.go`, `internal/metadata/provider.go`, `internal/metadata/hardcover_test.go`, `internal/store/migrations/NNNN_book_release_date.sql`, `internal/books/repo.go`, `internal/books/service.go`, `internal/books/upgrade.go`, `internal/books/catalogue.go`, `internal/books/ladder.go`, `internal/automation/books.go`, `internal/requests/service.go`, `internal/requests/progress.go`, `web/src/pages/BookDetail.tsx`
  - **Acceptance:**
    - An upcoming Hardcover book shows its release date on BookDetail, in the Discover modal and on the requester's My shelf card
    - No sweep search runs for that book before the date. A search runs on release day, then daily for two weeks, then on the normal ladder
    - Request tracking for an unreleased book reads 'Out <date>' instead of 'Searching'
    - Existing books pick up release dates on Refresh or through the refresh-books job
  - **Tests:** Go: hardcover_test parses release_date into BookResult and BookDetails; Go: books repo round-trip of release_date; Go: automation sweep test with the injected clock (future release: no search; release today: search plus misses reset; add-time search of an unreleased book: no miss; within 14 days: 24h cap); Go: progress_test 'Not out yet' note with next_check_at = release date
  - **Depends on:** [BOOK-04](#book-04), [BOOK-08](#book-08), [BOOK-17](#book-17)
  - **Risk:** Hardcover queries are unverified live, and a wrong field name breaks every GetBook. Ship behind a quick log check. Edition and book release dates can differ; use the book's date.
  - **Resolves:** books-3

#### Milestone: M7 — Follow authors

_Authors are real records with stable pages. Spelling variants are one author, and the page never shows the wrong person. Followed authors' new books are added automatically, with an admin alert._

<a id="book-19"></a>
- [ ] **BOOK-19 · Authors as entities (backend): authors table, books.author_id, catalogue author keys, /api/v1/book-authors** — `P2` · `M` · Phase 13
  - **Problem:** Authors are just strings. book_authors (migration 0079) only caches name, key, photo and bio, with no monitoring. 'J.K. Rowling' and 'J. K. Rowling' are two authors in the library grouping, and nothing links a book to a catalogue author id. That leaves the author page guessing by name and makes author monitoring impossible.
  - **Approach:** 1. New migration NNNN_authors.sql:
    - CREATE TABLE authors (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, catalogue_key TEXT NOT NULL DEFAULT '', image_url TEXT NOT NULL DEFAULT '', bio TEXT NOT NULL DEFAULT '', monitored INTEGER NOT NULL DEFAULT 0, monitor_new INTEGER NOT NULL DEFAULT 0, quality_profile TEXT NOT NULL DEFAULT '', added_at TEXT NOT NULL DEFAULT (datetime('now')), checked_at TEXT NOT NULL DEFAULT '', works_checked_at TEXT NOT NULL DEFAULT '').
    - CREATE UNIQUE INDEX idx_authors_key ON authors(catalogue_key) WHERE catalogue_key != ''.
    - ALTER TABLE books ADD COLUMN author_id INTEGER REFERENCES authors(id) ON DELETE SET NULL, plus an index.
    - No SQL copy from book_authors: two names there can share one key and would violate the unique index.
    2. Go BackfillAuthors(ctx) at boot (idempotent):
    - Group distinct books.author values by authorKey (folded, [BOOK-06](#book-06)).
    - Fold in the book_authors key, image and bio.
    - Never merge two groups whose catalogue keys differ.
    - Create or find one author per group, set books.author_id, and log counts.
    3. metadata: BookResult and BookDetails gain AuthorKey: the Hardcover contribution author id as 'hc:a:<id>' (hcAuthorPrefix, hardcover.go:28), and Open Library works authors[0].key ('OL…A'). Add, AddWorks and Refresh link author_id by key first, then by normalised name (UpsertByKey / UpsertByName).
    4. New internal/books/authors.go: repo and service with ListAuthors (owned and wanted counts), GetAuthor, UpsertByKey, UpsertByName, SetMonitoring(monitored, monitor_new, profile) and SetCatalogueKey. catalogue.go AuthorImages reads authors. book_authors stays for one release and is dropped later.
    5. handleAddAuthor (httpapi/books.go:700) upserts the author (catalogue_key=req.AuthorKey, monitored, monitor_new=true, profile) before AddWorks and passes author_id into AddWorks.
    6. Routes:
    - GET /api/v1/book-authors (optional ?name= lookup for redirects), protected.
    - GET /api/v1/book-authors/{id}, protected.
    - PUT /api/v1/book-authors/{id} {monitored, monitor_new, quality_profile, catalogue_key}, manager.
    - Not under /api/v1/books/authors/{id}: Go's ServeMux panics at startup on that path, because it overlaps GET /api/v1/books/{id}/series, /history and /covers and neither pattern is more specific.
    7. Merge ([BOOK-11](#book-11)) carries author_id when keep has none.
  - **Files:** `internal/store/migrations/NNNN_authors.sql`, `internal/books/authors.go`, `internal/books/authors_test.go`, `internal/books/service.go`, `internal/books/catalogue.go`, `internal/books/repo.go`, `internal/books/merge.go`, `internal/metadata/provider.go`, `internal/metadata/hardcover.go`, `internal/metadata/hardcover_authorworks.go`, `internal/metadata/openlibrary.go`, `internal/httpapi/books.go`, `internal/httpapi/server.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - Books credited to 'J.K. Rowling' and 'J. K. Rowling' share one author_id after the backfill
    - Two different people with the same name but different catalogue keys stay separate
    - Adding an author through 'Add author' creates a monitored author with monitor_new on, and links every added book
    - GET /api/v1/book-authors/{id} returns the author with owned and wanted counts; the server starts without a ServeMux conflict panic
  - **Tests:** Go: BackfillAuthors merges spelling variants, keeps different catalogue keys apart, and is idempotent; Go: Add links author_id by catalogue key, then by name; Go: handleAddAuthor creates a monitored author; Go: server route registration test (build the mux) to catch pattern conflicts
  - **Depends on:** [BOOK-06](#book-06), [BOOK-02](#book-02)
  - **Risk:** Merging two different people with the same normalised name. Merge by name only when no catalogue keys conflict, and keep books.author as the display string. Hardcover contribution ids are unverified live, so check the log after deploy.
  - **Resolves:** books-8
<a id="book-20"></a>
- [ ] **BOOK-20 · Author pages by id: stored catalogue author, a 'Which author is this?' picker, library grouping by author_id** — `P2` · `M` · Phase 13
  - **Problem:** AuthorDetail runs searchBookAuthors(name) on every visit. Without an exact name match it silently picks the result with the most works (AuthorDetail.tsx:29-31), so an author page can show another person's catalogue. The owned list uses an exact author-string comparison (AuthorDetail.tsx:41), and the library groups by the raw string (Books.tsx:192-202).
  - **Approach:** 1. web/src/App.tsx: new route /books/authors/:id. The old /books/author/:name resolves via GET /api/v1/book-authors?name= and redirects, or shows the picker when it is ambiguous.
    2. AuthorDetail.tsx loads by id and uses the stored catalogue_key for works and detail (/api/v1/books/discover/authors/{key}…). When catalogue_key is empty, show 'Which author is this?' with the candidates from searchBookAuthors(name) (photo, work count, sample titles). Save the pick with PUT /api/v1/book-authors/{id}. There is no silent most-works fallback.
    3. The owned list filters by author_id.
    4. Header: 'Monitor' and 'Add new books automatically' toggles and a profile select (PUT book-authors), in the existing style.
    5. Books.tsx author mode groups by author_id and falls back to the string for unlinked books. Author tiles link to /books/authors/:id.
  - **Files:** `web/src/App.tsx`, `web/src/pages/AuthorDetail.tsx`, `web/src/pages/Books.tsx`, `web/src/pages/BookDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Author pages live at /books/authors/<id> and always show the stored catalogue author
    - An author with no stored key asks the admin to pick the right person instead of guessing, and the pick persists
    - Old /books/author/<name> links redirect to the id page
    - The monitored and 'monitor new books' flags persist across reloads
  - **Tests:** UI: picker shown for an ambiguous name; the old name URL redirects; toggles persist; layout at 375px
  - **Depends on:** [BOOK-19](#book-19)
  - **Risk:** Low. Front-end only, on top of BOOK-19's API.
  - **Resolves:** books-8
<a id="book-21"></a>
- [ ] **BOOK-21 · Monitored authors: detect and add new releases on a schedule** — `P2` · `M` · Phase 13
  - **Problem:** 'Add author' takes a one-time snapshot of AuthorWorks into book rows, and no job re-checks the author. main.go only registers search-missing, import and rss-sync book jobs. New books by followed authors never appear unless someone notices, and that was Readarr's core loop.
  - **Approach:** 1. New migration NNNN_author_works_seen.sql: CREATE TABLE author_works_seen (author_id INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE, work_key TEXT NOT NULL, first_seen TEXT NOT NULL DEFAULT (datetime('now')), PRIMARY KEY (author_id, work_key)).
    2. books.Service.CheckAuthorReleases(ctx, limit int):
    - Take authors with monitored and monitor_new set whose works_checked_at is older than 7 days, oldest first, up to limit per run.
    - Stop when Hardcover usage is above 70% of the daily budget (BookSources.HardcoverUsage from [BOOK-17](#book-17)).
    - Fetch AuthorWorks(catalogue_key), filtered by the existing filterAuthorWorks.
    - The first check for an author only seeds author_works_seen, so following an author never re-adds back-catalogue the admin skipped.
    - Later checks add works that are new and not in the library (IdentityIndex from [BOOK-02](#book-02) plus book_keys aliases from [BOOK-09](#book-09)) through AddWorks: monitored, with the author's profile (or the default) and author_id. Mark them seen, write the event 'New from <author>', and search them via the coordinator's SearchBookNow, or leave unreleased ones to [BOOK-18](#book-18).
    - Stamp works_checked_at.
    3. cmd/arrmada/main.go: sched.Register("check-author-releases", 6*time.Hour, false, …) with limit 10. Persisted timestamps make it restart-safe.
    4. Publish the bus event 'book.author_release' {author, count, titles} for the OBS admin alerts catalogue ('2 new books from Brandon Sanderson').
    5. A deleted auto-added book is already 'seen', so it doesn't come back.
  - **Files:** `internal/store/migrations/NNNN_author_works_seen.sql`, `internal/books/authors.go`, `internal/books/authors_test.go`, `internal/metadata/bookswitch.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - Following an author adds nothing on the first check
    - When the catalogue later lists a new work, the next check adds it as monitored with the author's profile, a timeline event and an admin alert
    - A book the admin deletes after it was auto-added is not re-added
    - The job logs 'skipped: Hardcover budget' and adds nothing when the budget is low
  - **Tests:** Go: CheckAuthorReleases with a fake provider: the seed run adds 0; the next run with one extra work adds 1; a deleted book isn't re-added; unmonitored and monitor_new=false authors are skipped; the budget gate works; a prefix sibling of an owned book is still added (BOOK-02 identity)
  - **Depends on:** [BOOK-19](#book-19), [BOOK-02](#book-02), [BOOK-09](#book-09), [BOOK-17](#book-17), [BOOK-18](#book-18), OBS — admin alert catalogue to deliver 'book.author_release'
  - **Risk:** Hardcover author works include translations and companion junk; filterAuthorWorks already filters them, so reuse it unchanged. Budget use stays at about one request per author per week.
  - **Resolves:** books-8

#### Milestone: M8 — Ebooks reach e-readers and Kindles

_Family members add the server to KOReader, Moon+ or Thorium using their audiobook password. Kindle owners tap 'Send to Kindle'._

<a id="book-22"></a>
- [ ] **BOOK-22 · OPDS 1.2 catalogue for e-reader apps, authenticated with the per-user audiobook password** — `P2` · `M` · Phase 13
  - **Problem:** The only way an ebook reaches a device is handleBookEbook, a browser attachment download (httpapi/mybooks.go:136-171; My shelf offers only <a download>). A grep finds no OPDS anywhere. Family members on KOReader, Moon+, Librera or Thorium have to download on a computer or phone and sideload, which is worse than the Audiobookshelf setup Arrmada replaces.
  - **Approach:** 1. Move ebookFile, ebookContentType, ebookRank, downloadName and the RFC 2231 attachmentHeader logic from httpapi/mybooks.go into internal/books/ebookfile.go, so OPDS, My shelf and [BOOK-24](#book-24) share them.
    2. New package internal/opds with Handler(deps) http.Handler serving Atom (application/atom+xml;profile=opds-catalog):
    - GET /opds: navigation feed (New, My requests, Authors, Series, All, Search).
    - /opds/new?page=: acquisition feed of books with an ebook, newest first, 50 per page with rel=next.
    - /opds/mine: books behind the caller's requests, via book_id ([BOOK-03](#book-03)).
    - /opds/authors and /opds/authors/{id}: by author_id once [BOOK-19](#book-19) lands, by the normalised author string until then.
    - /opds/series and /opds/series/{key}.
    - /opds/search?q= plus /opds/opensearch.xml.
    - /opds/books/{id}/file, which streams with the right MIME type (application/epub+zip, application/pdf, application/x-mobipocket-ebook, application/vnd.comicbook+zip).
    - /opds/books/{id}/cover.
    - Entries carry title, author, summary, dc:issued, an http://opds-spec.org/image link and an http://opds-spec.org/acquisition link.
    - Visibility matches /me/books (library books with an ebook) and respects SEC's books adult-content filter once it exists.
    3. Auth is HTTP Basic: username plus the audiobook-server password.
    - Export audioserver.Accounts.Authenticate(ctx, user, pass) (*auth.User, error), which wraps verify() (keeping its bcrypt timing guard) and the allowed() check.
    - Require role >= requester, not disabled, and booksEnabled.
    - Failed attempts go through the existing loginLimiter (httpapi/ratelimit.go), passed in as an interface.
    - 401 responses carry WWW-Authenticate: Basic realm="Arrmada Books". A disabled user gets 403, and the module being off gives 404.
    4. Mount /opds in server.go before the SPA fallback. Non-/api paths already pass externalGate (external.go:107-110), so the handler's own auth is the gate.
    5. Privacy: request logging records the route pattern (r.Pattern), never /opds/books/{id} or the q= value. This matches the audiobook privacy rule and SEC's request-log redaction.
    6. UI:
    - My shelf and the Audiobooks page get a 'Read on your e-reader' box with the OPDS URL, a copy button and short setup notes for KOReader, Moon+, Librera and Thorium.
    - Note that HTTPS via the tunnel is required outside the home network, and that stock Kobo firmware and Apple Books don't support OPDS.
    - Relabel the password card 'Audiobook & e-reader password' and say it now unlocks both.
  - **Files:** `internal/opds/opds.go`, `internal/opds/feeds.go`, `internal/opds/opds_test.go`, `internal/books/ebookfile.go`, `internal/httpapi/mybooks.go`, `internal/httpapi/server.go`, `internal/httpapi/middleware.go`, `internal/httpapi/ratelimit.go`, `internal/audioserver/auth.go`, `web/src/pages/MyBooks.tsx`, `web/src/pages/Audiobooks.tsx`
  - **Acceptance:**
    - KOReader (or Thorium) can add <host>/opds with a requester's username and audiobook password, browse New, My requests, Authors, Series and Search, and download an EPUB
    - No credentials, or the normal Arrmada login password, returns 401 with WWW-Authenticate, and repeated failures are throttled. A disabled user gets 403. With the books module off, /opds returns 404
    - Feeds parse as Atom with OPDS acquisition and cover links, and books without an ebook are not listed
    - The request log contains no per-book OPDS paths or search terms
  - **Tests:** Go: opds_test parses each feed with encoding/xml and checks entries, link rels and MIME types; Go: Basic auth table (none, wrong, Arrmada password, audiobook password, disabled user, read-only role) plus throttling; Go: file download Content-Type and Content-Disposition; Go: pagination rel=next; search returns matching titles; Go: externalGate lets /opds through; Manual: KOReader or Moon+ on the owner's device
  - **Depends on:** [BOOK-03](#book-03), [BOOK-19](#book-19), SEC — books adult-content filter (apply once it exists) and request-log route-pattern redaction, AUD — password card relabel touches the Audiobooks page
  - **Risk:** Reusing the audiobook password broadens what it unlocks, so the UI says so. Basic auth must only travel over HTTPS outside the LAN; the tunnel provides TLS. OPDS support varies by reader, so don't promise Kobo or Apple Books.
  - **Resolves:** books-7, product-11
<a id="book-23"></a>
- [ ] **BOOK-23 · Outgoing email (SMTP) configured in Settings, with a mail package and a test button** — `P2` · `S` · Phase 13
  - **Problem:** Nothing in the codebase sends email (a grep for smtp finds nothing). Notifications go through Apprise, which can't carry a per-user attachment cleanly. Send-to-Kindle (BOOK-24) needs a sender configured in the app UI, with the credentials never in env vars or chat.
  - **Approach:** 1. New internal/mail/smtp.go:
    - type Config {Host string; Port int; Security string /* starttls|tls|none */; Username, From string}.
    - Send(ctx, cfg, password string, msg Message{To, Subject, Body string; Attachment *Attachment{Name, ContentType string; Open func() (io.ReadCloser, error); Size int64}}) error.
    - Build multipart/mixed with mime/multipart. Stream the base64-encoded attachment (encoding/base64.NewEncoder over a line-wrapping writer) so a 50 MB file isn't held in memory twice. Encode filenames per RFC 2231 and RFC 2047, reusing the helper moved to books/ebookfile.go in [BOOK-22](#book-22), or a copy here.
    - net/smtp with STARTTLS on 587 or implicit TLS on 465 (tls.Dial plus smtp.NewClient) and PLAIN auth. Honour ctx through a dial deadline.
    2. Settings keys smtp_host, smtp_port, smtp_security, smtp_username and smtp_from in the settings service. The password goes in the apikeys store as a new catalogue id 'smtp' (internal/apikeys): write-only, never returned by GET, never logged.
    3. Routes (RoleManager, like other settings writes):
    - GET /api/v1/settings/smtp returns the fields plus password_set: bool.
    - PUT /api/v1/settings/smtp.
    - POST /api/v1/settings/smtp/test {to} sends 'Arrmada test email' and returns the server's reply text on failure.
    4. Settings.tsx: an 'Outgoing email' card in the Notifications or Connections section, with fields, a masked password input, 'Send test email', and a short note recommending an app password from the owner's own mail provider.
    5. Logging: host and outcome only.
  - **Files:** `internal/mail/smtp.go`, `internal/mail/smtp_test.go`, `internal/apikeys/apikeys.go`, `internal/httpapi/settings.go`, `internal/httpapi/server.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - The owner saves SMTP settings in the UI, and 'Send test email' delivers to their inbox
    - GET settings never returns the SMTP password, and no log line contains it
    - A wrong password shows the server's reply in the UI instead of a generic error
  - **Tests:** Go: smtp_test against an in-process fake SMTP server (net.Listen with a scripted dialog) asserting the MIME structure, the base64 attachment, the RFC 2231 filename, the STARTTLS path (self-signed test cert) and auth failure handling; Go: settings GET redaction
  - **Depends on:** INT/OBS — if another epic adds email notifications later, it reuses internal/mail rather than adding a second sender
  - **Risk:** Deliverability depends on the sender mailbox's SPF and DKIM, so recommend the owner's own provider with an app password. Credentials are only ever entered in the UI (standing rule).
  - **Resolves:** books-7, product-11
<a id="book-24"></a>
- [ ] **BOOK-24 · Send to Kindle: per-user Kindle address, a send button, and the result in the inbox** — `P2` · `M` · Phase 13
  - **Problem:** Kindle owners in the family can't use OPDS. The only way to get a book onto their Kindle is to download it and email it to themselves by hand. Audiobookshelf, which Arrmada replaces, had email-to-device.
  - **Approach:** 1. New migration NNNN_user_kindle.sql: ALTER TABLE users ADD COLUMN kindle_email TEXT NOT NULL DEFAULT ''. auth.Service gets KindleEmail and SetKindleEmail.
    2. GET/PUT /api/v1/me/kindle {kindle_email} (protected). The /api/v1/me/ prefix is already external-allowed.
    - Validate it as an address. Warn, but don't block, when the domain isn't kindle.com or free.kindle.com.
    - GET also returns from_address and smtp_configured so the UI can explain the setup.
    3. POST /api/v1/me/books/{id}/send-to-kindle (requester+, books enabled):
    - No Kindle address: 400.
    - SMTP not configured: 409.
    - No ebook: 404.
    - Format not EPUB or PDF: 422 'this ebook is AZW3 — Kindle email takes EPUB or PDF'. The check lives in one function, books.KindleAccepts(format), because Amazon's list changes.
    - Size over 50 MB: 413.
    - More than 20 sends per user per day (in-memory counter keyed by user id and date): 429.
    - Otherwise return 202 and send in the background (3-minute context) with mail.Send ([BOOK-23](#book-23)).
    - The result goes to the user's own inbox: 'Sent “X” to your Kindle' or 'Couldn't send “X” to your Kindle: <smtp reply>'.
    - Log the outcome without the title or book id.
    4. MyBooks.tsx:
    - A 'Send to Kindle' button on ebook cards (and in APP's detail sheet when it lands), disabled with the reason for AZW3 or MOBI.
    - The first time, a sheet explains adding the From address to Amazon's Approved Personal Document E-mail List and asks for the Kindle address.
    - A toast says 'Sending… you'll get a message when it's done'.
    - If APP's Me page has landed, the Kindle address field lives there too.
  - **Files:** `internal/store/migrations/NNNN_user_kindle.sql`, `internal/httpapi/kindle.go`, `internal/httpapi/kindle_test.go`, `internal/httpapi/server.go`, `internal/auth/service.go`, `internal/books/ebookfile.go`, `web/src/pages/MyBooks.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A requester saves a Kindle address and taps Send to Kindle on an EPUB; the book arrives on the Kindle (manual check) and the inbox says it was sent
    - An AZW3-only book shows the button disabled with the reason; a file over 50 MB shows a clear message
    - The 21st send in a day returns 429 with a friendly message
    - No log line contains the book title or id
  - **Tests:** Go: handler tests (no Kindle address 400, SMTP missing 409, AZW3 422, too large 413, rate limit 429, read-only role 403, books module off 404); Go: background send against the fake SMTP server writes the inbox message; Go: KindleAccepts table; UI: first-time explainer sheet
  - **Depends on:** [BOOK-23](#book-23), APP — MyBooks detail sheet and Me page (optional placements; ship on cards first)
  - **Risk:** Amazon rejects senders that aren't approved, so the explainer must be prominent. Accepted formats change over time, so the check stays in one function.
  - **Resolves:** books-7, product-11

#### Milestone: M9 — Admin Books screens that fit

_The Books header and the detail toolbar fit a phone, card actions work on touch, the library can be sorted, and merged .m4b files carry proper tags and cover._

<a id="book-25"></a>
- [ ] **BOOK-25 · Admin Books screens: primary actions up front, a 'Library tools' menu, a grouped detail toolbar, touch-reachable card actions, sorting** — `P2` · `M` · Phase 13
  - **Problem:** The Books header's inner action row is 'flex items-center gap-2' with no wrap (Books.tsx:236). It holds up to 9 controls (two segmented toggles, Re-match, Search missing, Find series, Scan library, Select, + Add author, + Add book) and overflows on phones. Elsewhere on the Books screens:
- The BookDetail toolbar has 9 equal-weight buttons (BookDetail.tsx:318-333).
- Card delete and 'Search now' are 'hidden … group-hover' (Books.tsx:486-496), so touch users never see them.
- Status reads 'E+A ✓' (Books.tsx:32-40).
- The only sort is a fixed author-then-title order (Books.tsx:200-201).
  - **Approach:** 1. Books.tsx header:
    - Left: count and search.
    - Right: '+ Add book' (primary), '+ Add author', and 'Search missing (n)' when n > 0.
    - A '⋯ Library tools' menu holds Scan library, Re-match to Hardcover (n), Possible duplicates (n) ([BOOK-12](#book-12), moved out of its chip), Find series (Open Library setups only, [BOOK-17](#book-17)) and Select.
    - Merge the author/book and grid/table toggles into one 'View' control. Add a Sort dropdown (Title, Author, Recently added, Year, Release date once [BOOK-18](#book-18) lands) kept in URL search params.
    - Use flex-wrap. Under 640px the secondary buttons fold into the menu.
    2. statusOf spells out 'Ebook · Audiobook', 'Ebook' or 'Audiobook' with a check. 'Wanted' carries [BOOK-08](#book-08)'s next-check tooltip.
    3. Cards reveal on hover only under @media (hover: hover). On touch devices, a small always-visible '⋯' button opens Search now and Remove.
    4. BookDetail toolbar groups:
    - Monitor toggle.
    - Search ▾ (Automatic search, Interactive search).
    - Add files ▾ (Upload torrent, Manual import).
    - Fix ▾ (Edit metadata, Change match, Rename files, Refresh & rescan, Merge audiobook files).
    - A separate danger-styled Delete.
    5. Use the FE kit's Menu if it has landed. Otherwise build web/src/components/OverflowMenu.tsx: a button with aria-haspopup, Enter/Space/ArrowDown to open, arrow-key navigation, Escape to close, focus returned to the trigger, click-outside to close. Keep the dark warm palette, terracotta accent and current type scale. Handlers stay unchanged.
  - **Files:** `web/src/pages/Books.tsx`, `web/src/pages/BookDetail.tsx`, `web/src/components/OverflowMenu.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - At 375px the Books header has no horizontal overflow, and '+ Add book' stays visible
    - Scan library, Re-match, Find series and Possible duplicates are reachable from the ⋯ menu
    - With mobile emulation (touch), Search now and Remove are reachable on a card
    - BookDetail shows grouped controls (Monitor, Search, Add files, Fix, Delete) instead of nine buttons
    - The sort dropdown re-orders the library and survives a reload through the URL
  - **Tests:** UI: layout checks at 375, 768 and 1440px; UI: keyboard check of the menu (open, arrows, Escape, focus return); UI: touch-emulation check of card actions
  - **Depends on:** [BOOK-12](#book-12), [BOOK-08](#book-08), [BOOK-17](#book-17), FE — component kit Menu (if available)
  - **Risk:** Low; UI only. Keep the handlers unchanged to avoid regressions in the long-running jobs the buttons start.
  - **Resolves:** books-12
<a id="book-26"></a>
- [ ] **BOOK-26 · Merged .m4b keeps proper tags (title, author, series) and the source cover** — `P3` · `S` · Phase 13
  - **Problem:** merge.go builds the ffmpeg command with '-map_metadata 1', where input 1 is the chapters-only ffmetadata file, and '-map 0:a' (merge.go:99-104). The merged file therefore has no title, artist or album tags, and any embedded cover in the sources is dropped. Players other than Arrmada's audio server see an untagged, coverless file. (The hard-delete of sources in the same finding belongs to SAFE.)
  - **Approach:** 1. internal/audiobook/merge.go: Merge(ctx, files, out, Tags{Title, Author, Series, Year, Narrator}).
    - Add the first source as input 2.
    - Use '-map_metadata 2 -map_chapters 1 -map 0:a -map 2:v:0? -c:v copy -disposition:v:0 attached_pic' so the cover is carried over when there is one.
    - Then set explicit -metadata title/artist/album/album_artist/date, genre=Audiobook and the iTunes media type (stik=2) from Arrmada's book record, and composer=narrator once narrators exist.
    2. If ffmpeg rejects the cover stream (an exotic codec), retry once without '-map 2:v'.
    3. automation MergeAudiobook (automation/books.go:1498) passes the book's fields.
    4. This doesn't change how the audio server picks covers; the owner prefers Hardcover covers there.
    5. Test only on generated fixtures. Never on the owner's real library files.
  - **Files:** `internal/audiobook/merge.go`, `internal/audiobook/merge_test.go`, `internal/audiobook/live_check_test.go`, `internal/automation/books.go`
  - **Acceptance:**
    - ffprobe on a merged file shows the title, artist and album from Arrmada's book record, plus the chapters
    - When the first source has an embedded cover, the merged file has an attached_pic stream. When it has none, the merge still succeeds
  - **Tests:** Go audiobook: TestMergeArgsCarryTagsAndCover (argument building, including the no-cover retry); Go audiobook: extend TestMergeAgainstRealFFmpeg (skips without ffmpeg) to generate tagged sources with a cover and check the merged tags and attached_pic
  - **Depends on:** SAFE — audiobook merge safety (verify output duration, route source deletion through the recycle helper), which edits the same function
  - **Risk:** Low. The cover copy is optional, with a retry without it.
  - **Resolves:** audiobooks-9

#### Risks

- The full-title identity (BOOK-02) lets some catalogue variants become two rows. BOOK-12's review is the safety net, and Discover may briefly offer Request for a variant title the owner already has
- Books deleted by earlier boot merges are not restored automatically. BOOK-12 lists them (past 'merged' events) so the owner can re-add them by hand
- Re-keying listening rows (BOOK-10/BOOK-11) touches the sync guard's pending_* columns. A bad copy could apply a guarded backwards jump. Test thoroughly, then check with Lissen after deploy
- The never-give-up ladder (BOOK-04) and the MAM RSS poll (BOOK-07) raise load on private trackers. The first sweeps after deploy work through every previously abandoned book. The 40-per-sweep cap, the monthly tail and one MAM call per cycle bound it, but watch the MAM throttle logs
- Several Hardcover fields are unverified live: release_date on books, contribution author ids, and author-works fields. Per the books memory, a wrong field name breaks every GetBook, so check the log error first after each deploy (BOOK-18, BOOK-19)
- MyAnonaMouse behaviour is unverified: an empty-text dateDesc search, and lang_code / browse_lang ids (BOOK-07, BOOK-16). Verify with one real response using the owner's session
- Migration numbering collides with other epics. Every migration here is named NNNN_* and numbered at commit time
- Reusing the audiobook password for OPDS broadens what it unlocks. Basic auth outside the LAN depends on the tunnel's TLS
- Send to Kindle deliverability depends on the owner's SMTP provider (SPF/DKIM) and on Amazon's approved-sender list

#### Out of scope

- ebookFile and ebook support on the Audiobookshelf-compatible server (AUD epic)
- A built-in web ebook reader or in-app audiobook player
- Kobo sync, Apple Books, and Kindle delivery through Amazon APIs (email only)
- Converting AZW3/MOBI to EPUB for Kindle (no Calibre integration)
- Automatically restoring books deleted by past boot merges (they are only listed for manual re-add)
- Narrators as entities and narrator monitoring
- The books adult-content filter itself (SEC epic); this epic only applies it in OPDS
- Music module parity for identity, the search ladder and RSS (MUS epic)
- The mobile hamburger sidebar and requester app shell (APP/FE epics)
- Raising the Hardcover daily budget, or caching beyond the existing hccache

