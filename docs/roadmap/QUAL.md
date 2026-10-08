# QUAL — Quality profiles & ranking

_Part of the [Arrmada roadmap](../../ROADMAP.md). 27 tasks. After each task: priority, size, and the phase it ships in._

**Goal.** Make every promise the quality-profile builder makes hold on every path. Deleting a profile moves its titles. Series honour the bitrate window. Scene WEB and fansub releases are eligible. Converted files are never re-downloaded. Saving a profile never silently starts a mass re-download. Every 'why' names what actually decided the pick. Then rebuild the ranking core as an ordered key-by-key comparison and grow the format library (Unwanted pack, custom-format engine, group tiers, TRaSH import), keeping the target-file builder UX exactly as it is.

**Why.** The audit scored Quality profiles 6/10. Its verdict was that the idea is the best in the app, but the plumbing breaks the UI's promises in ways the owner and the family can't see:
- **Deleting a profile does not move its titles (quality-2, high).** Titles fall to a hidden 'Any quality' fallback with no resolution limit and no pre-release reject, so cams become grabbable again and movie upgrades silently stop. Interactive search and the library fit use the default profile instead, so one title behaves three different ways.
- **AV1 conversions read back as their original codec (quality-4, high).** The appended stamp loses to detectCodec's x264-first order and breaks group parsing. The upgrade sweep then re-downloads over the converted file, which is the exact loop the stamp exists to prevent.
- **TV ignores the bitrate window (quality-1 high, series-5).** Series grabs, RSS, upgrades and interactive search never attach a runtime, so the ceiling and floor are inert, ties go to the biggest pack, and the import gate never checks a ceiling.
- **The shipped templates silently block common releases (quality-3, high).** Their 'WEB-DL+' minimum rejects scene 'WEB' releases and nearly all anime fansub releases, because an unstated source ranks like a cam.
- **Saving a profile can queue terabytes of re-downloads with no warning (quality-5, high).** No dry run, no per-sweep cap, no minimum improvement step, and the 'Off' copy is wrong. Scanned files are judged from a stripped baseline, so they never meet HDR or audio targets (quality-10).
- **Explanations are templated, not derived (quality-11).** You see 'Highest bitrate' when Prefer decided, 'fewer preferred extras' when seeders decided, and 'higher quality… download smaller version' when switching to a 4K-only profile.
- **The ranking mixes two models (quality-6).** Additive Prefers can beat the goal resolution, while any negative custom score acts as a full Avoid cliff.
- **Compared with TRaSH, the format library is thin (quality-7, quality-8, quality-9, quality-12).**
  - Missing defences: there is no BR-DISK, foreign-language, 3D, upscale or DV-without-fallback handling, and the hidden −180 low-quality-group penalty is invisible.
  - Custom formats need exact parser values and have no regex or OR.
  - Matching gaps: dead torrents win on size, untagged SD releases are rejected, and 'Cam.2018' reads as a cam.

**Depends on:** SER (coordinate, not blocking): SER's single scope-aware series grab planner must build candidates through [QUAL-05](#qual-05)'s seriesCandidate/releaseRuntime (and [QUAL-20](#qual-20)'s original-language tag) instead of quality.NewCandidate. Whichever lands second wires it in. [QUAL-06](#qual-06)'s wantsEpisodeFile signature change also touches SER's import-gate work.; CONV (coordinate): [QUAL-03](#qual-03) edits convert/process.go stampEpisodeCodec and movies.RepointMovieFile, and [QUAL-09](#qual-09) adds convert.FactsForPath/FactsByPath reading convert_library (size_bytes, info_ver, probeSchemaVersion). CONV's rebuild (convert_history, Revert, DV handling) must keep those columns and that gate. Sequence the process.go edits to avoid conflicts.; REQ (coordinate): [QUAL-01](#qual-01) changes how Requests.Approve resolves a stored profile, and [QUAL-02](#qual-02) reassigns pending requests on delete. REQ's approve sheet should list only existing profiles of the request's media type.; CFG (optional): the new settings (upgrade_max_grabs_per_sweep in [QUAL-10](#qual-10), quality_ranking in [QUAL-22](#qual-22)) register in CFG's settings registry if it exists by then. Otherwise follow the handleGetSettings/handleUpdateSettings pattern in internal/httpapi/settings.go.; FE (optional): if FE ships shared Dialog/SegmentedControl/Chip primitives first, the QUAL dialogs and pickers ([QUAL-02](#qual-02), [QUAL-12](#qual-12), [QUAL-13](#qual-13), [QUAL-20](#qual-20), [QUAL-25](#qual-25)) use them. Otherwise they reuse Quality.tsx's TemplatePicker dialog and PrefPicker styles. FE owns the Quality editor's unsaved-changes guard.; ACQ (scope split): the stall-timeout template default with a migration, and the per-indexer min_seeders default (the other half of quality-9, quick win #4), belong to ACQ. [QUAL-07](#qual-07) only changes ranking and the template min_seeders.; SUB (optional): [QUAL-20](#qual-20)'s foreign-only check reads the owner's audio languages from Convert's keep_audio_langs today, and should switch to SUB's shared language table once it lands.

#### Design

## North star
Keep the target-file builder exactly as it is: layout, copy voice, dark warm palette, terracotta accent and type scale. Every change is plumbing under it, or an additive panel in the existing style (PrefPicker segmented controls, TemplatePicker-style dialogs, existing chip and colour tokens). The rule is one profile resolver, one candidate builder per media type, one way to judge the file on disk, and one ranking that records what decided it. Add a dry run before any save that would churn the library.

## 1. Profile resolution ([QUAL-01](#qual-01), [QUAL-02](#qual-02))
- `quality.Service.Effective(ctx, ref, media) string` is the only way a stored ref becomes a profile. `Coordinator.effectiveProfile` and the fit handler's `profileIdeals.resolve` delegate to it. Every acquisition, upgrade, import-gate, stall and record-grab call resolves once per title or version and uses that ref throughout.
- Dangling refs cannot exist:
  - `Repo.DeleteAndReassign` moves every row in one transaction: movies, movie_versions, series, books, artists, pending requests, in-flight grabs, and the `default_profile:<media>` setting.
  - `Service.RepairDanglingRefs` runs at every boot to fix libraries damaged before this change.
  - The last profile of a media type cannot be deleted.
- `fallbackProfile()` survives only for 'no profile of this media type exists', and it sets `RejectPreRelease: true`.

## 2. Candidates always carry context ([QUAL-05](#qual-05), [QUAL-20](#qual-20))
- Movies: `tagRuntime(m.Runtime)` (existing) plus `WithLanguage(m.Extra.OriginalLanguage)`.
- Series: `c.seriesCandidate(ctx, s, idx, rel)` builds `NewCandidate(...).WithRuntime(releaseRuntime(...)).WithLanguage(s.Extra.OriginalLanguage)`.
  - **Episode releases:** releaseRuntime resolves the episodes the release covers the same way coveredByFor does (AliasEpisodes, then ResolveEpisodes for anime, else Season+Episodes) and sums their runtimes.
  - **Packs:** it sums every aired-or-has-file episode in the covered seasons (SceneSeasonEpisodes for anime split seasons).
  - **Missing data:** zero runtimes use the show's median runtime. If nothing resolves, it returns 0 and the window is skipped, as today.
- The same builder feeds grabSeriesLimited (search, RSS, the absolute follow-up), RankSeriesReleasesWith (interactive search, GrabBestForScope, the builder's real-title test) and upgradeSeries. SER's future grab planner must call it too.

## 3. Judging the file on disk ([QUAL-03](#qual-03), [QUAL-09](#qual-09))
- `quality.CurrentFile{Release, SizeGB, RuntimeMin, Facts *FileFacts}` is what UpgradeCandidate, IsQualityUpgrade, AtCeiling, WouldReject and the impact dry run judge.
- Facts come from `convert.FactsForPath`. It reads the convert_library row and is accepted only when `size_bytes` matches and `info_ver == probeSchemaVersion`. It is injected into automation through a `FileFactsSource` interface.
- Without facts, the release name is used:
  - Codec stamps are canonical: `parser.RestampCodec` replaces the token in place, so the group still parses. A one-time repair fixes existing rows.
  - Synthetic baselines for scanned files include the probed HDR, Atmos and lossless tags.
- DV-only is judged the same way in CheckFit and the engine.

## 4. Parser ([QUAL-04](#qual-04), [QUAL-08](#qual-08), [QUAL-19](#qual-19))
New Release fields:
- `SourceInferred`, `ResolutionInferred`, `AudioLossless`
- `FullDisc`, `ThreeD`, `Upscaled`, `Extras`
- `Languages []string` (ISO 639-1), `Multi`, `Streaming`

Rules:
- Bare WEB is WEB-DL. A fansub [Group] or CRC name with no source is WEB-DL (inferred). Bounded 'BD' is BluRay.
- Explicit SD-era signals infer 480p.
- LPCM counts as lossless and DTS-HD HRA does not.
- Pre-release tokens count only after the title.
- Every new detection runs on the tail after the title cut, so words in titles are never misread.

## 5. Ranking
**v1, fixed now ([QUAL-06](#qual-06), [QUAL-07](#qual-07)):** avoid tier, then Total, then a 10% magnitude band in which seeders decide (magnitude is bitrate when both runtimes are known, else size), then magnitude, then source, then seeders. Zero-seeder torrents rank last within their tier. Source gates use a sourceTier where WEB-DL and WEBRip are one 'WEB' tier and an unstated source is never ranked like a cam.

**v2 ([QUAL-21](#qual-21) to [QUAL-23](#qual-23))** is an ordered comparator in `internal/quality/rank.go`: `compare(p, a, b) (int, RankKey)`. Keys in order:
1. avoid: target Avoids, the bitrate floor and Unwanted 'avoid' items only
2. resolution: the goal first
3. preferences: count of target Must and Prefer formats matched, waived when the bitrate collapsed
4. custom: custom formats plus keywords, additive, negatives allowed, no cliff
5. source: Remux > BluRay > WEB > HDTV > DVD > unstated, with PROPER +1 inside the tier
6. group: tier 1 > tier 2 > none
7. bitrate: codec-normalised; higher wins unless SmallBias > 0
8. health: seeders, with sizes within 10% treated as equal; zero seeders last
9. size

Behaviour:
- Every Decision records `DecidedBy`, and every eligible Evaluation records `LostOn`.
- `Total` stays for MinFormatScore and the Advanced raw view.
- Rollout setting `quality_ranking` = v1|v2. v2 runs in shadow and logs disagreements, and the default flips after the owner has reviewed the logs.

**Upgrades:**
- A candidate replaces the current file only when it wins on a key allowed by the profile's `upgrade_trigger`, or when IsBitrateUpgrade holds:
  - any: resolution, preferences, custom, source, proper
  - source: resolution, preferences, custom, source
  - format: resolution, preferences, custom
  - resolution: resolution only
- A same-group PROPER or REPACK is always allowed.
- Group, health and size never trigger an upgrade, and an avoided release never replaces a non-avoided file.
- A per-sweep grab budget caps how many upgrades one sweep can grab.

## 6. Format library
| Layer | What it is |
|---|---|
| Target formats | codec/HDR/audio rows; Must, Prefer and Avoid (unchanged) |
| Other formats | the built-in non-target formats, plus streaming services (AMZN, NF, ATVP, DSNP, HMAX/MAX, MA, PCOK) |
| Unwanted pack | BR-DISK, 3D, Extras (reject); Upscaled, Foreign-only, Low-quality group, DV without HDR fallback (avoid). Each item can be set to reject, avoid or allow, and stored in `quality_profiles.unwanted`. |
| Custom formats | conditions with NOT, Match all/any, a release_title regex (RE2, compiled once per Engine), value aliases (HEVC→x265, Dolby Vision→DV…), case-insensitive matching, and validation on save |
| Group tiers | `quality_profiles.group_tiers` holds editable tier-1 and tier-2 lists that feed the v2 group key |
| TRaSH import | paste custom-format JSON, convert it with explicit warnings for lossy parts, then add the formats with score 0 |

Keywords and reject terms match on separator-normalised names, so 'Directors Cut' matches 'Directors.Cut'.

## 7. Safety on save ([QUAL-10](#qual-10) to [QUAL-13](#qual-13))
- `POST /api/v1/quality/impact {profile}` classifies every file on that profile under the old and the edited profile as replace, search or settled. It returns the files whose state got worse, with their bytes and up to five example titles.
- The builder's Save shows a dialog with three actions:
  - [Save and allow upgrades]
  - [Save — keep existing files], which sets `upgrade_hold` on the affected rows
  - [Keep editing]
- The copy says 'eligible for replacement', never 'will be replaced'.

## 8. Explanations ([QUAL-14](#qual-14), [QUAL-15](#qual-15), [QUAL-17](#qual-17), [QUAL-18](#qual-18))
- One RankKey-to-text table produces 'Chosen over…', the per-row 'ranked lower: …' and the Why list.
- `RankedRelease` gains `matched`, `avoided`, `bonus_waived` and `lost_on`, and `ReleaseList` gains `chosen_over`.
- `POST /api/v1/quality/explain` powers a 'Test a release name' card: parsed chips, the verdict and matched formats against the unsaved profile.
- Contradictory target states produce inline warnings that do not block Save.

## Data model (migrations numbered at implementation time from 0090 up; other epics add migrations too, so check `ls internal/store/migrations | tail -1`)
| Change | Task |
|---|---|
| `quality_profiles.upgrade_trigger TEXT NOT NULL DEFAULT 'any'` | [QUAL-11](#qual-11) |
| `upgrade_hold INTEGER NOT NULL DEFAULT 0` on movies, movie_versions, episodes | [QUAL-13](#qual-13) |
| `quality_profiles.unwanted TEXT NOT NULL DEFAULT ''` (JSON) | [QUAL-20](#qual-20) |
| `quality_profiles.group_tiers TEXT NOT NULL DEFAULT ''` (JSON) | [QUAL-26](#qual-26) |
| settings `upgrade_max_grabs_per_sweep` (default 10), `quality_ranking` (v1/v2), `repair:codec_stamp_v1` | [QUAL-10](#qual-10), [QUAL-22](#qual-22), [QUAL-03](#qual-03) |

## API surface
- `DELETE /quality/profiles/{id}?move_to=custom:N` returns 200 `{moved:{...}, moved_to}`; 409 for the last profile; 400 for a target of another media type.
- `POST /quality/impact`, `POST /quality/profiles/{id}/hold-existing`, and `POST /movies/{id}/resume-upgrades` and `/series/{id}/resume-upgrades[?season=]`.
- `POST /quality/explain` and `POST /quality/import-trash`.
- `GET /quality/profiles` adds `vocabulary`.
- The preview adds `sample_label`, `conflicts` and `warnings`.
- `PUT /movies/{id}/quality-profile` adds `downgrade_reason` and `downgrade_kind`.

## Definition of done (every task)
- go vet and `go test -race ./...` pass in Docker before pushing.
- Commits go straight to main and end with the Co-Authored-By trailer.
- Behaviour changes (WEB tier, TV window, Unwanted defaults, budget) are called out in the commit message.
- Never test-convert or rewrite the owner's real files. Use temp-DB fixtures.
- The audiobook privacy, adult filter and credentials rules are untouched by this epic.

#### Milestone: M1: Profiles can't silently go permissive or loop

_Deleting a profile moves its titles to a chosen profile, and the last one can't be deleted. A title never grabs under the hidden permissive fallback, and cams stay rejected. Existing dangling refs are repaired at boot. AV1/HEVC-converted files read back as their real codec and are no longer re-downloaded over._

<a id="qual-01"></a>
- [ ] **QUAL-01 · One profile resolver: every acquisition path resolves a missing profile to the default, and the fallback never grabs cams** — `P0` · `S` · Phase 0
  - **Problem:** Several automation paths pass a title's raw profile ref straight to the quality service:
- RecordManualGrab (coordinator.go:302)
- grabMissing (coordinator.go:667 and :699)
- upgradeMovie (the AllowsUpgrades gate :828, UpgradeCandidate :871, recordGrab :894)
- RegrabMovie (:971 and :990)
- grabSeriesLimited (series.go:403, plus the grab closure's recordSeriesGrab around :485)

For a ref whose profile was deleted, Service.Resolve (service.go:63-70) returns fallbackProfile() (presets.go:26-32). That profile allows any resolution, adds DV/HDR10/Atmos bonuses and has no RejectPreRelease, so cams become eligible. AllowsUpgrades and StallMinutes return false/0, so movie upgrades and stall fail-over silently stop.

Other paths already resolve to the user's default (effectiveProfile, coordinator.go:425-433): interactive search, series upgrades, the series import gate, and the fit check (fit.go:48-69). One title therefore behaves three different ways.

Two more gaps:
- bookProfile (automation/books.go:562-567) falls to a hardcoded EPUB profile instead of the default book profile.
- Requests.Approve passes a non-empty dangling stored ref through unchanged (requests/service.go:166-173).
  - **Approach:** 1. **New resolver.** In internal/quality/service.go add `func (s *Service) Effective(ctx context.Context, ref, media string) string`:
       - If `ref != "n/a"` and `Known(ref)`, return ref. Check 'n/a' explicitly first, because Known('n/a') is true.
       - Else if `DefaultProfile(media)` is non-empty, return it.
       - Else return ref.
    
       Coordinator.effectiveProfile (coordinator.go:425) becomes a delegate. fit.go profileIdeals.resolve calls Effective and keeps its per-request cache.
    2. **Resolve once per title or version and use that ref everywhere.**
       - grabMissing and RegrabMovie: `ref := c.effectiveProfile(ctx, v.QualityProfile, "movie")` before Decide, StallMinutes and recordGrab.
       - upgradeMovie: resolve in the `want` loop (AllowsUpgrades) and again before UpgradeCandidate, StallMinutes and recordGrab.
       - RecordManualGrab: resolve m.QualityProfile.
       - grabSeriesLimited: `profile := c.effectiveProfile(ctx, s.QualityProfile, "series")` at the top, used by Decide and by the grab closure's recordSeriesGrab.
       - RankSeriesReleasesWith: return the effective ref in ReleaseList.Profile (it already decides with it).
       - books.go bookProfile: `GetStored(ctx, c.effectiveProfile(ctx, ref, quality.MediaBook))`. Keep the hardcoded EPUB profile only when no book profile exists.
    3. **Log dangling refs.** effectiveProfile logs once per ref per process (sync.Map) when it substitutes a non-empty, non-'n/a', unknown ref: `quality: title's profile no longer exists — using the default` with ref and default.
    4. **requests/service.go Approve.** After `profile = req.QualityProfile`, if that stored ref is non-empty and `!Known`, use DefaultProfile(req.MediaType). An explicit unknown profile from the approver still returns ErrUnknownProfile.
    5. **presets.go fallbackProfile.** Add `RejectPreRelease: true`. Rewrite its comment, and the comment on Service.Resolve: the fallback is used only when no profile of the media type exists.
  - **Files:** `internal/quality/service.go`, `internal/quality/presets.go`, `internal/automation/coordinator.go`, `internal/automation/series.go`, `internal/automation/series_interactive.go`, `internal/automation/books.go`, `internal/httpapi/fit.go`, `internal/requests/service.go`, `internal/automation/effectiveprofile_test.go`, `internal/quality/quality_test.go`
  - **Acceptance:**
    - With a movie version and a series whose stored ref is custom:999 (deleted), grabMissing, RegrabMovie, RankReleases, upgradeMovie's gate and grabSeriesLimited all decide under the default profile of their media type.
    - With a dangling ref and a candidate list containing only a CAM release, grabMissing grabs nothing. With zero profiles (fallback), a CAM is still rejected.
    - Movie upgrades keep running for a title whose stored ref is dangling (AllowsUpgrades is evaluated on the default profile).
    - Approving a request whose stored profile was deleted adds the title on the default profile, and an explicitly chosen unknown profile still returns ErrUnknownProfile.
    - A dangling ref is logged once, with the ref and the substituted default.
  - **Tests:** Go internal/automation/effectiveprofile_test.go: TestEffectiveDanglingRefUsesDefault. TestGrabMissingDanglingRefRejectsCam: Coordinator{quality, db, log} with a store.Open temp DB, grabMissing with one 'Movie.2024.HDCAM.x264' candidate and v.QualityProfile 'custom:999' returns 0 and never calls c.Grab (the indexers field is nil, so the old code would panic).; Go internal/quality: TestFallbackProfileRejectsPreRelease (Resolve on an unknown ref with no profiles stored never makes a CAM eligible).; Go internal/requests: Approve with a stored ref of a deleted profile adds under the default (follow the existing requests service test setup).; Go internal/automation: bookProfile with a dangling ref returns the default book profile's format scores.
  - **Risk:** Library-scanned 'n/a' titles must keep routing to the default. The explicit 'n/a' check guards this, and the existing TestEffectiveProfileRoutesScannedToDefault must still pass. Grabs recorded from now on store the effective ref, not the dangling one, which changes what the Activity page shows for those rows. That is intended.
  - **Resolves:** quality-2
<a id="qual-02"></a>
- [ ] **QUAL-02 · Deleting a quality profile reassigns its titles in one transaction; dangling refs are repaired at boot; the delete UI picks the target** — `P0` · `M` · Phase 1
  - **Problem:** The delete button promises 'Delete — N films move to your default' (Quality.tsx:354), but Repo.Delete (quality/repo.go:132-141) only runs `DELETE FROM quality_profiles`. Nothing is reassigned:
- movies, movie_versions, series, books and artists rows
- pending requests and in-flight grabs
- the 'default_profile:<media>' setting

You can also delete the last profile of a media type, leaving everything on the fallback. Libraries where a profile was already deleted carry dangling 'custom:N' refs today.
  - **Approach:** 1. **Repo.** In internal/quality/repo.go add `func (r *Repo) DeleteAndReassign(ctx context.Context, id int64, to string) (Reassigned, error)`. `Reassigned` is {Movies, Versions, Series, Books, Artists, Requests, Grabs int}. In one tx:
       - Read media_type for id and for customID(to). Return ErrNotFound when either is missing.
       - Return ErrMediaMismatch when the types differ, and ErrSameProfile when to equals the deleted ref.
       - Run UPDATE ... SET quality_profile=to WHERE quality_profile=from on: movies, movie_versions, series, books, artists, requests (status='pending') and grabs (status='grabbed').
       - If the setting 'default_profile:<media>' equals from, rewrite it to to.
       - DELETE the profile, then commit.
    2. **Service.** `Delete(ctx, id int64, moveTo string) (Reassigned, string, error)`.
       - When moveTo is empty, use DefaultProfile(media), or the first other profile of that media when the deleted one is the default.
       - Return a new ErrLastProfile when no other profile of that media exists.
       - Return the resolved target ref.
    3. **Boot repair.** `Service.RepairDanglingRefs(ctx) (map[string]int, error)`. For each table, find quality_profile LIKE 'custom:%' whose id is not in quality_profiles and set it to DefaultProfile(media). Media comes from:
       - movies and movie_versions: movie
       - series: series
       - books: book
       - artists: music
       - requests: requests.media_type
       - grabs: grabs.media_type, where '' means movie
    
       Leave '' and 'n/a' alone. It is idempotent and cheap, so it runs on every boot. Call it from cmd/arrmada/main.go right after `qualitySvc := quality.NewService(st.DB())` (~line 166) and log counts per table when any are non-zero.
    4. **HTTP.** handleDeleteQualityProfile (httpapi/quality.go) reads `?move_to=custom:N`.
       - 200 `{moved:{movies,versions,series,books,artists,requests,grabs}, moved_to}`
       - 409 'Create another profile first' for ErrLastProfile
       - 400 for ErrMediaMismatch, ErrSameProfile or an unknown target
       - 404 for an unknown id
    5. **UI** (Quality.tsx ProfileCard, the confirm step).
       - A 'Move N films to' select lists the other profiles of the same media (from the list Quality() already loads). It defaults to the default profile, or the first other one when deleting the default.
       - The button reads 'Delete and move N films to <name>'.
       - When the profile is the only one of its media, Delete is disabled with the title 'Create another profile first'.
       - After success, show 'Moved 12 films and 1 request to <name>' inline in the card style.
       - In web/src/lib/api.ts, deleteQualityProfile(id, moveTo) returns the moved counts.
  - **Files:** `internal/quality/repo.go`, `internal/quality/service.go`, `internal/quality/repo_test.go`, `internal/httpapi/quality.go`, `cmd/arrmada/main.go`, `web/src/pages/Quality.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - After deleting a profile used by N movies (including extra versions), M shows, a pending request and an in-flight grab, every one of those rows carries the chosen target ref in the DB.
    - Deleting the default profile makes the target the new 'default_profile:<media>'.
    - DELETE on the last profile of a media type returns 409, and the UI disables Delete for it.
    - A target of another media type returns 400 and changes nothing (the tx rolls back).
    - On boot, a DB seeded with movies on 'custom:99' (nonexistent) is repaired to the movie default, and the log names the counts.
    - The Quality page shows the target picker and the moved counts.
  - **Tests:** Go internal/quality/repo_test.go (new; store.Open on a temp dir): DeleteAndReassign moves rows in movies, movie_versions, series, books, artists, requests (pending only; approved untouched) and grabs (grabbed only); rewrites default_profile:movie; refuses the last profile (ErrLastProfile via Service.Delete); refuses a series target for a movie profile and leaves the DB unchanged.; Go: RepairDanglingRefs with seeded dangling custom:99 rows in movies, series and requests (media_type 'series') maps each to its media default and leaves 'n/a' and '' rows alone. A second run is a no-op.; Go internal/httpapi: handler test for 200 with counts, 409 last profile, 400 mismatch.; UI check: Quality, Movies tab, Delete on a profile with titles shows the picker; the only profile's Delete is disabled; the counts show after delete.
  - **Depends on:** [QUAL-01](#qual-01)
  - **Risk:** A wrong media-type check could move book or music titles onto a video profile, so validate inside the tx and test the mismatch. The boot repair rewrites user data: it must only touch refs whose profile id does not exist, and it logs counts. If REQ's approve-sheet work lands in parallel, both edit request profile handling; rebase carefully.
  - **Resolves:** quality-2
<a id="qual-03"></a>
- [ ] **QUAL-03 · AV1/HEVC conversions read back as their real codec: restamp the codec token in place, repair existing rows, and never re-grab the release a file was converted from** — `P0` · `S` · Phase 0
  - **Problem:** After a conversion, Convert appends ' AV1' or ' x265' to the recorded source_release:
- convert/process.go stampEpisodeCodec (~700-717)
- movies/service.go RepointMovieFile appendToken (~628-636)

This causes three problems:
- **The stamp reads as the old codec.** detectCodec (parser.go:707-723) checks x264/h264/avc before av1, so 'Movie.2020.1080p.WEB.H.264-GRP AV1' parses as x264, and an HEVC source converted to AV1 parses as x265.
- **The group stops parsing.** The trailing token defeats reGroup `-([A-Za-z0-9]{2,})$` (parser.go:158) for every stamped file, so release-group formats stop matching.
- **The upgrade sweep re-downloads the original.** The AV1 file never satisfies Prefer AV1, is costed at H.264 efficiency in IsBitrateUpgrade, and slips past the curKey dedupe in UpgradeCandidate (service.go:140,153). The sweep then re-grabs the original release over the converted file, which is the exact loop the stamp was added to prevent.
  - **Approach:** 1. **parser.RestampCodec(release string, c Codec) string.** Manual token scanning (RE2 has no lookaround).
       - Find separator-bounded codec tokens: x264, x265, h.264, h264, 'h 264', h.265, h265, hevc, avc, av1, xvid, divx, vc-1, vc1.
       - Replace the first with the canonical token ('AV1' or 'x265') and drop the rest.
       - With no codec token, insert '.AV1' (or '.x265') immediately before a trailing '-GROUP' so reGroup still matches, or append when there is no group.
       - Also add `parser.WithoutCodec(release) string`, which strips codec tokens and normalises separators, for dedupe.
    2. **parser.detectCodec.** Check a bounded trailing or standalone ' av1 ' before the x265/x264 cases, so legacy appended stamps still read as AV1. A release only names AV1 when it is AV1.
    3. **Use RestampCodec in both stamp sites.** movies/service.go RepointMovieFile replaces appendToken, and convert/process.go stampEpisodeCodec uses it too. Keep the 'already reads as this codec' early-return.
    4. **One-time repair.** New internal/convert/repair_stamps.go: `RepairCodecStamps(ctx, db *sql.DB, log *slog.Logger) (int, error)`, called from cmd/arrmada/main.go after migrations.
       - Guarded by settings key 'repair:codec_stamp_v1'.
       - For movies, movie_versions and episodes rows whose source_release ends in ' AV1' or ' x265', strip the suffix and RestampCodec it with that codec.
       - Write only when the result differs. Log the count and the before/after of the first 5 rows.
    5. **Defence in UpgradeCandidate and IsQualityUpgrade** (quality/service.go). Skip a candidate whose WithoutCodec(name) equals WithoutCodec(currentRelease), case-insensitive. The exact release a file was converted from is never re-grabbed because of the stamp.
  - **Files:** `internal/parser/parser.go`, `internal/parser/restamp_test.go`, `internal/parser/bugfix_test.go`, `internal/movies/service.go`, `internal/convert/process.go`, `internal/convert/repair_stamps.go`, `internal/convert/repair_stamps_test.go`, `internal/quality/service.go`, `internal/quality/bitrateupgrade_test.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - Parse('Movie.2020.1080p.WEB.H.264-GRP AV1').Codec == AV1 (legacy stamp).
    - Parse(RestampCodec('Movie.2020.1080p.WEB.H.264-GRP', AV1)) gives Codec AV1 and Group 'GRP', and the string reads 'Movie.2020.1080p.WEB.AV1-GRP'.
    - After a convert job finishes, the recorded source_release reads like 'Film.2021.1080p.BluRay.AV1-GRP', never '... x264-GRP AV1'.
    - After the repair runs on an existing DB, no source_release in movies, movie_versions or episodes ends in ' AV1' or ' x265', and a second boot does nothing (settings key set).
    - On a profile with Prefer HEVC/AV1 and upgrades on, an AV1-converted movie (4 GB, 120 min) is not picked for upgrade by an equal-resolution HEVC release, nor by its own original x264 release.
  - **Tests:** Go internal/parser/restamp_test.go table:
- H.264 to AV1; x265 to AV1; H.264 to x265
- no codec token (inserted before the group); no group (appended)
- already AV1 (unchanged)
- 'H.264' spelled with dots and with spaces
- 'AVC' inside a word left untouched
- WithoutCodec equality across a restamp; Go internal/parser/bugfix_test.go: the legacy 'x264-GRP AV1' parses as AV1; the existing codec cases still pass.; Go internal/quality: UpgradeCandidate with baseline 'Film.2021.1080p.BluRay.AV1-GRP' (4 GB, 120 min), candidates 'Film.2021.1080p.BluRay.x265-OTHER' (6 GB) and 'Film.2021.1080p.BluRay.x264-GRP' (8 GB) returns no upgrade.; Go internal/convert/repair_stamps_test.go: temp DB (store.Open) with stamped rows in all three tables is repaired, unstamped rows are unchanged, and the guard key prevents a rerun.; Never run the convert real-encode recipe against library files; these are DB-only fixtures.
  - **Risk:** Moving the AV1 check earlier could misread a title word 'AV1'; the bounded token makes that unlikely. The repair rewrites recorded release names, so it logs before/after and is idempotent. CONV's rebuild (convert_history/Revert) also edits process.go, so land this first or rebase onto it.
  - **Resolves:** quality-4

#### Milestone: M2: TV, anime and scene WEB grab what the profile promises

_Scene 'WEB' and fansub releases are eligible under the templates. Series grabs, RSS, interactive search and upgrades honour the bitrate window, packs show a bitrate, and the import gate refuses over-ceiling replacements. Near-equal releases go to the healthier torrent. SD, lossless and pre-release parsing is correct._

<a id="qual-04"></a>
- [ ] **QUAL-04 · One WEB source tier: scene WEB, fansub and BD parse correctly, and an unstated source is no longer treated like a cam** — `P1` · `S` · Phase 4
  - **Problem:** Three parser and gate rules combine to block common releases:
- A bare 'WEB' token parses as WEBRip (parser.go:699-702), and bugfix_test.go:115 locks that in.
- Fansub names ('[SubsPlease] Frieren - 01 (1080p) [CRC]') and bare 'BD' batches parse as SourceUnknown.
- sourceRank has no entry for unknown, so it gets 0, the same as CAM (quality.go:52-55), and Evaluate's MinSource gate (363-365) rejects it.

The '4K HDR collection' and '1080p efficient' templates set min_source 'WEB-DL' (Quality.tsx:182,192) and are offered for Series. Scene WEB TV is therefore rejected with 'Not WEB-DL — this is WEBRip', nearly all simulcast anime with 'this is unknown', and shows silently never download.
  - **Approach:** 1. **parser.detectSource.**
       - A bare ' web ' becomes SourceWebDL (Sonarr semantics). Update bugfix_test.go:115.
       - A bounded ' bd ' and 'bdmux' become SourceBluray. Leave 'bdmv', 'bdiso' and BD25-100 alone: those are full discs, handled in [QUAL-19](#qual-19).
       - In Parse, after the title cut: when Source is still unknown and the name has a leading fansub [Group] (titleStart>0) or reAnimeCRC matches, set Source=SourceWebDL and a new `Release.SourceInferred=true`. An explicit 'WEBRip' token still wins.
    2. **quality.go: add sourceTier(s) for the MinSource/MaxSource gates only.**
       - WEB-DL and WEBRip share one WEB tier. sourceBonus keeps 8 vs 6, so WEB-DL still wins ties.
       - An unknown source passes a MinSource at or below the WEB tier. Under a BluRay+ minimum it is rejected with 'Source isn't stated in the name — this profile needs BluRay or better'.
       - An unknown source passes any MaxSource, because it can't be judged.
       - sourceLabel('') becomes 'an unstated source', so reasons never read like a cam.
    3. **UI** (Quality.tsx SOURCES / MAX_SOURCES).
       - Replace 'WEBRip+' and 'WEB-DL+' with one 'WEB+ (WEB-DL or WEBRip)' option (value 'WEB-DL'). A stored 'WEBRip' displays as WEB+, and the same applies to 'up to WEB'.
       - sourceSummary uses the new labels.
       - The '4K HDR collection' and '1080p efficient' templates set min_source ''. They rely on the target and the windows.
    4. **No data migration.** Stored WEB-DL/WEBRip minimums now mean the WEB tier. Call out the behaviour change in the commit message.
  - **Files:** `internal/parser/parser.go`, `internal/parser/bugfix_test.go`, `internal/parser/parser_test.go`, `internal/quality/quality.go`, `internal/quality/reject_test.go`, `internal/quality/quality_test.go`, `web/src/pages/Quality.tsx`
  - **Acceptance:**
    - 'Show.S01E01.1080p.WEB.h264-GROUP' is eligible under a WEB-DL+ profile.
    - '[SubsPlease] Frieren - 01 (1080p) [ABCD1234].mkv' and '[Erai-raws] Show - 12 [1080p]' parse with Source WEB-DL and SourceInferred=true, and are eligible under both templates.
    - '[Group] Show (BD 1080p HEVC FLAC)' parses as BluRay; 'Movie.BDMV' does not.
    - Under a BluRay+ profile, an unstated-source release is rejected with the new 'isn't stated' reason. CAM reasons are unchanged.
    - The Sources dropdown shows a single WEB+ option, and new 4K/1080p template profiles have no minimum source.
  - **Tests:** Go parser table:
- bare WEB becomes WEB-DL
- 'Charlottes.Web.2006.DVDRip' stays DVD; 'Web.of.Lies.S01E01.HDTV' stays HDTV
- SubsPlease/Erai-raws fansubs are inferred WEB-DL; a [Group] name with an explicit WEBRip token stays WEBRip
- BD becomes BluRay; BDMV does not
- the parser_test.go:116-117 expectations for Erai-raws are updated deliberately; Go quality reject_test:
- MinSource 'WEB-DL' accepts WEBRip and an inferred fansub
- MinSource 'BluRay' rejects unknown with the new text
- MaxSource 'WEB-DL' still rejects BluRay; Go ranking: a 1080p WEB-DL beats a 1080p WEBRip of equal size.; UI check: the template picker no longer sets a minimum source, and a stored WEBRip minimum displays as WEB+.
  - **Risk:** Users who chose WEB-DL+ to exclude WEBRip lose that distinction. Ranking still prefers WEB-DL, and group tiers (QUAL-26) restore control. Several anime parser tests expect an empty Source and must be updated on purpose, not blindly. The import path parses too (matching, not quality), so run the full library and automation suites.
  - **Resolves:** quality-3
<a id="qual-05"></a>
- [ ] **QUAL-05 · Series candidates carry a runtime, so the bitrate window applies to TV grabs, RSS, interactive search and upgrades** — `P1` · `M` · Phase 4
  - **Problem:** The ceiling and floor in Engine.Evaluate (quality.go:377-386, 450-456) need Candidate.RuntimeMin, and only the movie paths set it (tagRuntime, called at coordinator.go:465, 667, 860, 971). Series candidates are built with bare quality.NewCandidate in three places:
- grabSeriesLimited (series.go:401), which also serves RSS and the absolute follow-up
- RankSeriesReleasesWith (series_interactive.go:129), which also serves GrabBestForScope and the builder's real-title test
- upgradeSeries (series_reliability.go:229)

coordinator.go:388-391 admits the gap. The results:
- A 25 Mb/s BluRay beats a 5 Mb/s WEB-DL under a '1080p 5–15 Mb/s' profile.
- Packs never show a bitrate in interactive search.
- Upgrades climb past the ceiling.

The UI promises the opposite (Quality.tsx:665, 783).
  - **Approach:** 1. **New file internal/automation/seriesruntime.go.**
       - `type runtimeIndex struct { byEp map[epKey]int; typical int; seasonEps map[int][]epKey }`.
       - `newRuntimeIndex(s series.Series) runtimeIndex`. typical is the median of non-zero e.Runtime. seasonEps lists, per season > 0, the episodes that have aired (aired(e.AirDate)) or have a file.
    2. **`func (c *Coordinator) releaseRuntime(ctx, s series.Series, idx runtimeIndex, r parser.Release) int`.** It resolves the release to episodes the same way coveredByFor (series.go:818-843) does:
       1. If AliasEpisodes is ok, use those refs.
       2. Else for an anime Kind episode, ResolveEpisodes.
       3. Else for an anime split-season pack whose season TMDB doesn't have, SceneSeasonEpisodes.
       4. Else for an episode, (r.Season, r.Episodes).
       5. Else for a pack, multi-season or complete-show release, every key in idx.seasonEps for the seasons where r.CoversSeason(season).
    
       Sum byEp, using typical for zeros. Return 0 when no episode resolves, or when every runtime is 0 and typical is 0.
    3. **`c.seriesCandidate(ctx, s, idx, rel indexer.Release) quality.Candidate`** = NewCandidate(rel.Title, rel.SizeGB(), rel.Seeders).WithRuntime(releaseRuntime(...)). Use it in:
       - grabSeriesLimited's candidate loop: build idx once per call.
       - RankSeriesReleasesWith: replace the episode-only epRuntime block (series_interactive.go ~150-165) with `Bitrate: bitrateMbps(ev.Candidate.SizeGB, ev.Candidate.RuntimeMin)`, so packs show a bitrate.
       - upgradeSeries: the candidates for each `have` use releaseRuntime too, which covers S01E01E02 double episodes.
    4. **Delete the caveat** in the tagRuntime comment (coordinator.go:388-391).
    5. **Templates.** TV encodes are leaner than film. Confirm with the owner and give VIDEO_TEMPLATES media-aware windows for Series (suggested: 2160p 10–30, 1080p 3–12, 720p 1.5–6), so efficient x265 WEB episodes don't drop into the floor's avoid tier the day this ships.
    6. **Logging.** grabSeriesLimited's 'found releases but grabbed none' log gains `example_over_ceiling` (name, computed runtime, Mb/s), so a false ceiling rejection is diagnosable.
  - **Files:** `internal/automation/seriesruntime.go`, `internal/automation/seriesruntime_test.go`, `internal/automation/series.go`, `internal/automation/series_interactive.go`, `internal/automation/series_reliability.go`, `internal/automation/series_reliability_test.go`, `internal/automation/coordinator.go`, `web/src/pages/Quality.tsx`
  - **Acceptance:**
    - Under a series profile with a 1080p window of 5–15 Mb/s, an 8 GB 1080p x265 BluRay for a 45-minute episode is rejected as 'Over your 15 Mbps ceiling', both in the automatic grab and in the interactive search modal.
    - A 10-episode, 45-minute season pack is eligible at 40 GB (~12.7 Mb/s) and rejected at 80 GB.
    - Interactive series search shows a bitrate for season packs and multi-episode releases.
    - The upgrade sweep never picks an over-ceiling episode release.
    - Episodes with unknown runtime and no typical runtime behave as before: the ceiling is skipped.
  - **Tests:** Go internal/automation/seriesruntime_test.go, releaseRuntime on a hand-built series.Series:
- single episode; S01E01E02 sums both
- a season pack sums aired-or-has-file episodes only (future episodes excluded)
- zero runtimes are filled with the median
- a complete-show pack spans seasons
- the unresolvable case returns 0

Anime absolute and alias resolution use a temp-DB series.Service seeded as in series_alias_test.go.; Go series_reliability_test.go: upgradeSeries' candidate building attaches runtime. Extract the per-episode decision (candidates to UpgradeCandidate) into a helper so it is testable without indexers; it picks a within-window release over a larger over-ceiling one.; Go quality ceiling_test.go: a pack candidate with a summed runtime is judged against the ceiling.; UI check: Series detail, then interactive search for a season, shows the pack bitrate and the ceiling rejection text.
  - **Depends on:** [QUAL-01](#qual-01)
  - **Risk:** Wrong or missing TMDB episode runtimes, anime arcs mapped through aliases, and packs that contain fewer episodes than TMDB lists all skew pack bitrate. That can cause false 'over ceiling' rejections or put packs under the floor. Mitigations: the median fallback, counting only aired episodes, logging the computed runtime with rejections, and media-aware template windows. SER's planned single grab planner must call seriesCandidate rather than NewCandidate; whichever lands second wires it in.
  - **Resolves:** quality-1, series-5
<a id="qual-06"></a>
- [ ] **QUAL-06 · Series import gate refuses over-ceiling replacements, and ties between different-length releases break on bitrate, not raw size** — `P1` · `S` · Phase 4
  - **Problem:** Two problems remain after the search side is fixed:
- **The import gate has no ceiling.** wantsEpisodeFile (reviews.go:778-855) never checks a ceiling, so an automatic over-ceiling replacement still lands. It also costs a multi-episode file's bytes against one episode's runtime (reviews.go:828-831), which doubles the apparent bitrate of an E01E02 file.
- **Ties go to the biggest file.** Engine.Decide breaks equal-score ties toward the larger file (quality.go:528-533). That is right for one movie, but once series candidates carry different runtimes it makes a huge pack beat single episodes just by being bigger.
  - **Approach:** 1. **quality.** Add `func (s *Service) ExceedsCeiling(ctx, ref, release string, sizeGB float64, runtimeMin int) (bool, string)`. It resolves the profile and uses `p.capFor(parser.Parse(release).Resolution)` and BitrateMbps. It is raw, like Evaluate's ceiling, and returns the same 'Over your N Mbps ceiling (X Mbps)' text.
    2. **reviews.go.** The caller (reviews.go:556) passes `len(refs)`, and wantsEpisodeFile gets a new `fileEpisodes int` parameter.
       - Compute the candidate share `candShareGB = candBytes/GiB / max(1,fileEpisodes)`.
       - After the 'nothing there yet' and 'gone from disk' early returns, and BEFORE the resolution comparison: if ExceedsCeiling(profile, candName, candShareGB, cur.RuntimeMin), log 'series import: refusing an over-ceiling replacement' (episode, Mb/s, ceiling) and return false.
       - Use candShareGB in the existing IsBitrateUpgrade and IsQualityUpgrade calls too.
       - First imports (no current file) and forced/manual imports are unaffected.
    3. **quality.go Decide tie-break.** Inside the equal-Total branch, compare bitrateMbps() when both candidates have RuntimeMin > 0, else SizeGB. Keep the same preferSmaller semantics. Movies are unchanged, because their candidates share one runtime.
  - **Files:** `internal/quality/service.go`, `internal/quality/quality.go`, `internal/quality/ceiling_test.go`, `internal/quality/quality_test.go`, `internal/automation/reviews.go`
  - **Acceptance:**
    - An automatic import whose per-episode bitrate is above the profile ceiling does not replace an existing episode file, and the log names the reason. With no existing file it imports.
    - A double-episode file is costed per episode: a 4 GB E01E02 file for two 45-minute episodes is ~6.4 Mb/s, not ~12.7.
    - With equal scores, a 30 GB 10-episode pack (~9.5 Mb/s) loses to a 2.5 GB single episode (~7.9 Mb/s)? No: the higher bitrate wins, so the pack wins here. A 60 GB pack no longer wins over a 3 GB episode just because it is larger once both are inside the window: the comparison uses Mb/s.
    - Movie tie-breaks are unchanged.
  - **Tests:** Go quality: TestDecideTieBreaksOnBitrateWhenRuntimesDiffer, TestDecideMovieTieBreakUnchanged, TestExceedsCeiling (with and without runtime).; Go automation: wantsEpisodeFile refuses an over-ceiling equal-resolution replacement and an over-ceiling resolution upgrade, and accepts a first import (temp-DB series.Service with a current episode file pointing at a temp file, as reviews tests do).; Go automation: the double-episode share test.
  - **Depends on:** [QUAL-05](#qual-05)
  - **Risk:** A higher-resolution upgrade that is over the ceiling is now refused at import, although the searcher would already have rejected it. Only grabs from before this change or from outside the planner hit this path. Logged.
  - **Resolves:** quality-1, series-5
<a id="qual-07"></a>
- [ ] **QUAL-07 · Prefer healthy torrents: near-equal releases tie on magnitude and seeders decide; dead torrents rank last** — `P2` · `S` · Phase 4
  - **Problem:** Seeders are the very last tie-breaker, after Total, exact size and source (quality.go:525-538), so the largest release wins even with 0–1 seeders. emptyProfile and every template set min_seeders 0 (Quality.tsx:168). The per-indexer min_seeders filter also defaults to 0 (indexer/service.go:320-323, migration 0011), so nothing steers away from dead torrents.
  - **Approach:** 1. **Decide comparator (v1).** Within the same avoided tier and equal Total:
       - Magnitude is bitrate when both runtimes are known ([QUAL-06](#qual-06)), else SizeGB.
       - When the two magnitudes are within 10% of the larger, compare seeders first, then fall back to magnitude (preferSmaller respected) and source.
       - A torrent with 0 seeders sorts after every seeded candidate in the same avoided/non-avoided tier, regardless of Total.
       - Usenet is already excluded by grabbable(), and an unknown seeder count from an indexer reads as 0, so this ranks and never rejects.
    2. **Templates** (Quality.tsx VIDEO_TEMPLATES) set min_seeders: 1. emptyProfile stays 0, and the Minimum seeders NumberField hint reads: 'Some indexers don't report seeders — leave 0 if releases vanish'.
    3. [QUAL-22](#qual-22) carries this into ranking v2 as the health key. If [QUAL-14](#qual-14) has already landed, update decidingFactor's seeders branch in the same commit.
  - **Files:** `internal/quality/quality.go`, `internal/quality/quality_test.go`, `web/src/pages/Quality.tsx`
  - **Acceptance:**
    - Between a 10.0 GB release with 2 seeders and a 9.5 GB release with 150 seeders of the same score, the 150-seeder release wins.
    - A >10% size difference still goes to the larger file (SmallBias 0) or the smaller one (SmallBias > 0).
    - A 0-seeder release is picked only when no seeded alternative exists in the same tier.
    - New profiles from templates have min_seeders 1.
  - **Tests:** Go quality_test: near-equal sizes tie and go to seeders; outside the band, size decides in both SmallBias directions; a 0-seeder release is last even with a higher Total; series candidates with runtimes use bitrate for the band.
  - **Depends on:** [QUAL-06](#qual-06)
  - **Risk:** Low. It slightly changes picks between near-identical releases. Indexers that never report seeders rank all their releases together, below seeded ones from other indexers, which is acceptable. The stall-timeout default (the other half of quality-9) is ACQ's quick win #4, not this task.
  - **Resolves:** quality-9
<a id="qual-08"></a>
- [ ] **QUAL-08 · Parser gaps: infer SD, make lossless correct (LPCM in, DTS-HD HRA out), and check pre-release tokens only after the title** — `P2` · `S` · Phase 4
  - **Problem:** Three parser gaps:
- **SD is never inferred.** detectResolution (parser.go:638-657) never infers SD, and Evaluate rejects an unknown resolution whenever the profile lists resolutions (quality.go:359-361). 'Show.S01E01.HDTV.x264' and 'DVDRip.XviD' releases therefore fail even a profile that allows 480p.
- **'Lossless' is wrong both ways.** It misses LPCM/PCM, and it counts lossy DTS-HD HRA as lossless because the 'dts hd' needle matches it (parser.go:752; presets.go:13; ideal.go:318).
- **Pre-release tokens are matched in the title.** isPreRelease scans the whole name (parser.go:668-680), so 'Cam.2018.1080p.WEB.x264' parses as CAM and is refused.
  - **Approach:** 1. **SD inference.** In Parse, after source detection: when Resolution is unknown and any explicit SD-era signal is present, set Resolution=Res480p and a new `Release.ResolutionInferred=true`. The signals are:
       - Source is DVD or HDTV
       - Codec is XviD
       - bounded sdtv/dsr/pdtv/dvb tokens
    
       The 'SD' window key already covers 480p.
    2. **Audio.**
       - Add an {'LPCM', ['lpcm', ' pcm ']} audioTags entry (bounded).
       - Add `Release.AudioLossless`, computed in Parse: TrueHD, FLAC or LPCM; or DTS-HD when 'ma', 'dts x' or 'dtsx' is present and 'hra' or 'hi res' is not.
       - New `CondLossless` condition type (no value). The built-in 'Lossless' format (presets.go) uses it.
       - ReleaseFacts.Lossless = r.AudioLossless.
       - Update formatMeta's description to 'TrueHD, DTS-HD MA, DTS:X, FLAC or LPCM'.
       - The 'DTS-HD' label and format stay as they are, so existing custom formats are unaffected.
    3. **Pre-release.** Run this only when a year or season marker produced a real cut (cut < len(name)): after the cut is computed, if Source==CAM and the tail (name[cut:] after the year token) has no pre-release token, re-run detectSource on the tail. Names with no year or season marker keep today's whole-name behaviour, so 'Movie.HDCAM.x264' stays CAM.
  - **Files:** `internal/parser/parser.go`, `internal/parser/prerelease_test.go`, `internal/parser/parser_test.go`, `internal/quality/presets.go`, `internal/quality/quality.go`, `internal/quality/ideal.go`, `internal/quality/stored.go`
  - **Acceptance:**
    - 'Show.S01E01.HDTV.x264-GRP' and 'Old.Show.S02E03.DVDRip.XviD-GRP' are eligible under a profile allowing 480p, while 'Show.S01E01.720p.HDTV' stays 720p.
    - 'Movie.1999.1080p.BluRay.REMUX.LPCM.2.0' matches Lossless; 'Movie.2010.1080p.BluRay.x264.DTS-HD.HRA.7.1' does not; 'DTS-HD.MA' and 'DTS-X' do.
    - 'Cam.2018.1080p.WEB.x264-GRP' parses as WEB; 'Movie.2024.HDCAM.x264' and 'Movie.HDCAM.x264' are still CAM.
  - **Tests:** Go parser: SD inference (HDTV, DVDRip, XviD, explicit 720p untouched), the LPCM tag, AudioLossless cases (MA, DTS:X, HRA, TrueHD, FLAC), pre-release in the title versus the tail (extend prerelease_test.go), and a no-year cam name.; Go quality: the Lossless format and ReleaseFacts.Lossless agree on the HRA and LPCM cases.
  - **Risk:** Inferring 480p could let an untagged HD release through as SD under an SD-only profile. That is rare, and the inference is limited to explicit SD-era signals. The parser is shared with import matching, so run the full parser, library and automation suites.
  - **Resolves:** quality-12

#### Milestone: M3: No surprise re-downloads

_The current file is judged from its probed facts. One sweep grabs at most N upgrades. Each profile chooses what is worth an upgrade. Saving a profile first shows how many files (and TB) become eligible for replacement, with a 'keep existing files' option._

<a id="qual-09"></a>
- [ ] **QUAL-09 · One set of facts: judge the file on disk from Convert's probed MediaInfo for target, upgrade, ceiling and downgrade decisions** — `P1` · `M` · Phase 4
  - **Problem:** The upgrader and the library fit judge the same file from different facts:
- **Two sources of truth.** TargetMet, UpgradeCandidate and AtCeiling judge ReleaseFacts(parser.Parse(currentRelease)) (service.go:133, 338-341). The Library fit bars judge convert.Facts(MediaInfo) (fit.go:84-93, fit_profiles.go:96).
- **Scanned files lose their probed facts.** upgradeBaseline (coordinator.go:910-929) builds the baseline from resolution, source and codec only. It throws away the probed HDR, Atmos and Audio already on MovieFile (movies/movie.go:94-100). Those files never meet HDR or audio targets, so the upgrader keeps churning them.
- **DV-only is judged two ways.** A DV-only release name gives HDR 'SDR' plus DV in ReleaseFacts (ideal.go:297,316). CheckFit then flags it under 'Avoid SDR', while the engine's SDR format (quality.go:133) does not match it.

'Is this file done?' has no single answer.
  - **Approach:** 1. **convert/facts.go.** Add `func (s *Service) FactsForPath(ctx, path string, sizeBytes int64) (quality.FileFacts, bool)`. It reads the convert_library row (info_json, size_bytes, info_ver) and returns false when info_ver != probeSchemaVersion or size_bytes != sizeBytes, so a stale entry for a replaced file is ignored.
       - Add `FileFacts.DVNoFallback`, set from the probe (DV with no HDR10/HLG base).
       - Also add a batch `FactsByPath(ctx, media string) (map[string]quality.FileFacts, error)` for [QUAL-12](#qual-12), built from IndexedFiles with the same gates.
    2. **automation.** Add `type FileFactsSource interface{ FactsForPath(ctx, string, int64) (quality.FileFacts, bool) }` and `Coordinator.SetFileFacts(src)`. Wire it in cmd/arrmada/main.go after the convert service is built. A nil source means release-name behaviour.
    3. **quality.** Add `type CurrentFile struct{ Release string; SizeGB float64; RuntimeMin int; Facts *FileFacts }`. UpgradeCandidate, AtCeiling, IsQualityUpgrade and WouldReject take a CurrentFile. With Facts set:
       - TargetMet uses them.
       - The current-file Evaluate uses `releaseWithFacts(parser.Parse(Release), facts)`. It overrides Resolution, Codec, HDR tags and Audio (Atmos, plus a lossless tag that sets AudioLossless when [QUAL-08](#qual-08) has landed), and keeps Source, Group and Edition from the name.
       - IsBitrateUpgrade uses the probed codec.
    
       Without Facts, behaviour is unchanged.
    4. **Callers.**
       - upgradeMovie: v.FilePath; size from v.File.SizeBytes.
       - upgradeSeries: e.FilePath. Build the CurrentFile before AtCeiling as well.
       - wantsEpisodeFile: cur.Path, for the IsQualityUpgrade and IsBitrateUpgrade current side.
       - handleSetQualityProfile (httpapi/movies.go:449-462): WouldReject; the handler gets facts through a new `Automation.CurrentMovieFile(ctx, m)` helper.
    5. **Fallback for files without facts.** upgradeBaseline appends v.File.HDR tags, 'Atmos' when v.File.Atmos, and the lossless labels from v.File.Audio to the synthetic baseline.
    6. **Align DV-only.** One `hdrValues(FileFacts) []string` helper is used by CheckFit (ideal.go:357-365) and prefersMet (471-477). A DV-only file is ['DV'], not ['DV','SDR'], whether it comes from DVNoFallback or from a release name with a DV tag and no HDR tag. This matches the engine, where the SDR format needs no HDR tags.
  - **Files:** `internal/convert/facts.go`, `internal/convert/facts_test.go`, `internal/quality/ideal.go`, `internal/quality/service.go`, `internal/quality/ideal_test.go`, `internal/quality/bitrateupgrade_test.go`, `internal/automation/coordinator.go`, `internal/automation/series_reliability.go`, `internal/automation/reviews.go`, `internal/httpapi/movies.go`, `cmd/arrmada/main.go`
  - **Acceptance:**
    - A movie whose release name has no HDR or Atmos tags, but whose analysed file is HDR10 with Atmos, meets a target that prefers both, and the upgrade sweep stops searching for it.
    - An AV1 file (by probe) is judged as AV1 even when its release string says x264.
    - A library-scanned file without an index entry gets a baseline that includes its probed HDR and Atmos from MovieFile.
    - Under 'Avoid SDR', a DV-only file gets the same verdict from the Library fit and the upgrader.
    - When the index entry's size doesn't match the file on disk, or info_ver is old, the release-name path is used.
  - **Tests:** Go quality:
- TargetMet with Facts{HDR10, Atmos} on a tagless release is met
- UpgradeCandidate with Facts{Codec:av1} on an x264 release name and Prefer AV1 gives no upgrade
- AtCeiling uses the facts bitrate
- DV-only consistency between CheckFit and the engine's SDR avoid; Go convert facts_test: FactsForPath on a temp DB covering match, size mismatch (false) and old info_ver (false).; Go automation: upgradeBaseline with probed HDR and Atmos on MovieFile includes the tags.; All existing quality and automation tests pass unchanged when no facts source is set.
  - **Depends on:** [QUAL-03](#qual-03)
  - **Risk:** This changes upgrade verdicts for every analysed file, and in most cases it stops churn. A wrong probe (for example DV base unknown on old entries) could make a file look 'done'; the info_ver gate keeps those entries out. The nil-source path must be exercised by tests. CONV must keep convert_library's size_bytes and info_ver semantics.
  - **Resolves:** quality-10
<a id="qual-10"></a>
- [ ] **QUAL-10 · Per-sweep upgrade budget: one sweep grabs at most N upgrades and logs the rest** — `P1` · `S` · Phase 4
  - **Problem:** UpgradeMovies (coordinator.go:776-800) and UpgradeSeries (series_reliability.go ~95-120) grab every upgrade they find in one sweep, limited only by free disk (diskOKFor). After a profile edit that can mean hundreds of grabs at once.
  - **Approach:** 1. **Budget type.** New internal/automation/upgradebudget.go: `type upgradeBudget struct{ max, used, deferred int }` with `allow() bool` (max 0 = unlimited), `take()` and `defer1()`.
    2. **Read the setting.** Add `c.settingInt(ctx, key string, def int) int`, which reads the settings table. UpgradeMovies and UpgradeSeries each create a budget from settings key 'upgrade_max_grabs_per_sweep' (default 10).
    3. **Thread it through.** Pass it into upgradeMovie(ctx, m, b) and upgradeSeries(ctx, id, b). They call b.allow() before each Grab and b.take() after a successful one. When the budget is exhausted, they count the remaining would-be grabs as deferred instead of grabbing them.
       - Each episode counts as one grab.
       - The manual single-title paths (UpgradeMovie after a profile change, the series single upgrade) pass nil, meaning unlimited.
    4. **Stop the sweep early.** Once the budget is spent the sweep stops iterating titles and logs `upgrade budget reached — N grabbed; remaining titles wait for the next sweep` with the module.
    5. **Settings.** httpapi/settings.go handleGetSettings/handleUpdateSettings expose 'upgrade_max_grabs_per_sweep' as a number string, like recycle_max_gb. Settings.tsx gets a number field 'Upgrades per sweep (0 = no limit)' in the automation/search section next to search_on_add. If CFG has shipped a settings registry by then, register it there.
  - **Files:** `internal/automation/upgradebudget.go`, `internal/automation/upgradebudget_test.go`, `internal/automation/coordinator.go`, `internal/automation/series_reliability.go`, `internal/httpapi/settings.go`, `web/src/pages/Settings.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With 15 upgradeable movies and the default budget, one sweep grabs 10 and logs that the rest wait.
    - Setting 0 restores unlimited sweeps.
    - A manual 'upgrade after profile change' for one movie is never blocked by the budget.
    - The Settings page shows and saves the value.
  - **Tests:** Go upgradebudget_test: allow/take/deferred accounting, and unlimited when max is 0.; Go automation: extract the per-version grab loop of upgradeMovie into a helper that takes picks plus a budget and returns the grab list, so it is testable without indexers. With 15 picks and budget 10 it grabs 10. The series per-episode loop gets the same treatment.; Go httpapi: a settings round-trip for the key.
  - **Risk:** A low budget delays wanted upgrades after a deliberate profile change. That is acceptable and logged. The sweep order is the list order, so the same titles go first each sweep; an upgraded title drops out of the candidates, so progress is made.
  - **Resolves:** quality-5
<a id="qual-11"></a>
- [ ] **QUAL-11 · Per-profile upgrade trigger ('Replace for') and honest 'Off' copy** — `P1` · `M` · Phase 4
  - **Problem:** qualityBetter in UpgradeCandidate and IsQualityUpgrade (service.go:156, 202) is any Total gain at all: a +1 PROPER, a +2 WEBRip to WEB-DL source step, or a small keyword nudge. Radarr has a minimum score increment; Arrmada has none.

The 'Off' upgrade step says 'A file is only replaced by a better resolution or a format you want' (Quality.tsx:86). In fact a source gain (WEB-DL to Remux), a PROPER and keywords also replace files.
  - **Approach:** 1. **Migration** (next free number, e.g. 009x_quality_upgrade_trigger.sql): `ALTER TABLE quality_profiles ADD COLUMN upgrade_trigger TEXT NOT NULL DEFAULT 'any'`. Add `StoredProfile.UpgradeTrigger` (json 'upgrade_trigger'; values any|source|format|resolution, where '' means any) and include it in the repo's profileCols, scan, Create and Update.
    2. **Split the score.** In Evaluate, split FormatScore into `TargetScore` (formats in targetFormats) and `CustomScore` (custom formats plus keywords). This is additive and Total is unchanged.
    3. **New internal/quality/rank.go.** Add `type RankKey string` with consts avoid, resolution, preferences, custom, source, proper, group, bitrate, health, size. Add `improvementKey(cand, cur Evaluation) RankKey`, which returns the most significant improving component in the order resolution (resRank up), preferences (TargetScore up), custom (CustomScore up), source (sourceBonus up), proper. Add `triggerAllows(trigger string, k RankKey) bool`:
       - any: all of the above
       - source: everything except proper
       - format: resolution, preferences, custom
       - resolution: resolution only
    4. **Gate the upgrade.** In UpgradeCandidate and IsQualityUpgrade, qualityBetter becomes `ev.Total > cur.Total && triggerAllows(sp.UpgradeTrigger, improvementKey(ev, cur))`, with one exception: a PROPER or REPACK whose group, resolution and source equal the current file's is always allowed. The bitrate path (IsBitrateUpgrade, governed by upgrade_min_percent) is unchanged.
    5. **UI (UpgradesEditor).** Add a 'Replace for' select, shown when upgrades are on, in the existing field style:
       - 'Any improvement'
       - 'A better source or a format you prefer'
       - 'A format you prefer or a higher resolution'
       - 'Higher resolution only'
    
       New profiles default to 'any'. upgradesSummary shows it.
    6. **Copy.** UPGRADE_STEPS[0].detail becomes 'Size is ignored. A file is replaced by a higher resolution, a better source (WEB → BluRay → Remux), a PROPER fix, or a format you prefer — as limited by Replace for.' Once [QUAL-05](#qual-05) has landed, re-read the 'Never above a bitrate ceiling' line (Quality.tsx:783), which is then true for series too.
  - **Files:** `internal/store/migrations/009x_quality_upgrade_trigger.sql`, `internal/quality/stored.go`, `internal/quality/repo.go`, `internal/quality/quality.go`, `internal/quality/rank.go`, `internal/quality/rank_test.go`, `internal/quality/service.go`, `web/src/pages/Quality.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - With 'A format you prefer or a higher resolution', a same-resolution WEB-DL to BluRay release is not grabbed as an upgrade, a release with a newly preferred HEVC is, and a same-group PROPER is.
    - With 'Higher resolution only', only a resolution gain or a same-group PROPER upgrades (plus bitrate upgrades when a percentage step is set).
    - Existing profiles keep today's behaviour ('any').
    - The 'Off' step copy lists source and PROPER replacements, and the Replace-for select round-trips through save and reload.
  - **Tests:** Go quality rank_test: improvementKey per component; triggerAllows matrix; UpgradeCandidate with trigger 'format' refuses a +10 source step, accepts a +50 Prefer and accepts a same-group PROPER; IsQualityUpgrade applies the same rule.; Go repo round-trip for upgrade_trigger, with '' read as 'any'.; UI check: Quality, Upgrades shows the select and the corrected Off copy.
  - **Risk:** Ranking v2 (QUAL-23) must map triggers to its keys. Storing an enum, not a score threshold, makes that a direct mapping. The TargetScore/CustomScore split must keep Total identical, and the existing scoring tests prove it.
  - **Resolves:** quality-5
<a id="qual-12"></a>
- [ ] **QUAL-12 · Dry-run a profile edit before saving: 'Saving will make N files (~X TB) eligible for replacement'** — `P1` · `M` · Phase 4
  - **Problem:** handleUpdateQualityProfile (httpapi/quality.go:86-100) saves with no impact check, and the builder's Save (Quality.tsx:472-488) has no preview. When the current file misses a newly added Must, Evaluate returns Total 0, so every eligible candidate counts as 'qualityBetter' (service.go:136-156). A new Prefer adds +50. Upgrades default to on (Quality.tsx:170), and UpgradeMovies sweeps the whole library (coordinator.go:776-800). Clicking Must on HEVC therefore quietly queues terabytes of re-downloads with no number shown first. Changing a single movie's profile does ask (movies.go:455-458).
  - **Approach:** 1. **internal/quality/impact.go (pure).**
       - `type ImpactFile struct{ Title string; Cur CurrentFile }`
       - `type Bucket struct{ Files int; Bytes int64; Examples []string }`
       - `type Impact struct{ Replace, Search Bucket }`
       - `func (s *Service) Impact(old, edited StoredProfile, files []ImpactFile) Impact`
    
       Classify each file per profile:
       - 'replace': the current file is ineligible under the profile (Evaluate of the CurrentFile rejects it: Must, resolution, MinSource, ceiling, Unwanted reject) and upgrades are on.
       - 'search': upgrades on, and neither TargetMet nor at-ceiling.
       - 'settled': otherwise.
    
       Count files whose state got worse from old to edited, with total bytes and up to 5 example titles per bucket.
    2. **HTTP.** `POST /api/v1/quality/impact {profile}` (RoleManager; saved profiles only, id>0; old = the stored row). Gather the files on that ref:
       - movies: the default version plus versions with files; size from the file; runtime m.Runtime.
       - series: episodes with files on shows using the ref; runtime e.Runtime.
       - release: the upgrade baseline.
       - facts: one convert.FactsByPath(media) map ([QUAL-09](#qual-09)). Never query per file.
    3. **UI (VideoBuilder.save).** For an existing profile, call impact first. When replace+search > 0, show a modal in the TemplatePicker dialog style:
       - Message: 'Saving will make 212 files (~3.4 TB) eligible for replacement — 180 no longer meet this profile, 32 will be searched for a format you prefer', with example titles.
       - Buttons: [Save and allow upgrades]; [Save — keep existing files] (calls [QUAL-13](#qual-13)'s hold; until that lands it reads 'Save with upgrades off' and sets upgrades_enabled=false); [Keep editing].
       - No dialog when nothing worsens. The copy says 'eligible for replacement', never 'will be replaced'.
  - **Files:** `internal/quality/impact.go`, `internal/quality/impact_test.go`, `internal/httpapi/quality.go`, `internal/httpapi/server.go`, `internal/httpapi/fit_profiles.go`, `web/src/pages/Quality.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - On a profile whose files are H.264, switching HEVC to Must shows a dialog with the file count and TB before anything is saved.
    - Adding Prefer Atmos puts the files lacking Atmos in the 'search' bucket.
    - Saving an unchanged profile, or turning upgrades off, shows no dialog.
    - 'Keep editing' saves nothing.
    - For Must and resolution changes, the replace count agrees with the Library fit bar's 'don't fit' count for the same edit.
  - **Tests:** Go internal/quality/impact_test.go:
- adding Must HEVC moves all H.264 files to replace
- adding Prefer Atmos moves files without it to search
- a lowered 1080p ceiling moves files above it to replace
- upgrades turned off, or an unchanged profile, gives 0
- a file with facts versus a release-only file; Go httpapi: handler test with a temp DB of 3 movies on the profile, checking bytes and examples.; UI check: Quality, Edit, toggle Must, then Save shows the dialog; 'Keep editing' leaves the profile unchanged on reload.
  - **Depends on:** [QUAL-09](#qual-09), [QUAL-11](#qual-11)
  - **Risk:** This is an estimate: actual grabs depend on what indexers offer, so the copy says 'eligible'. Large TV libraries (tens of thousands of episodes) need one facts query and one episodes query; measure the endpoint on a fixture of about 20k episodes.
  - **Resolves:** quality-5
<a id="qual-13"></a>
- [ ] **QUAL-13 · 'Keep existing files': hold current files from profile-driven upgrades, with visible chips and Resume** — `P2` · `M` · Phase 4
  - **Problem:** The audit recommends offering 'apply to new grabs only' when a profile edit would churn the library. Today the only way to change a profile for future grabs while keeping today's files is to switch upgrades off for every title on the profile.
  - **Approach:** 1. **Migration** (next free number, e.g. 009x_upgrade_hold.sql): add `upgrade_hold INTEGER NOT NULL DEFAULT 0` to movies, movie_versions and episodes.
    2. **Endpoint.** `POST /api/v1/quality/profiles/{id}/hold-existing` (RoleManager) sets upgrade_hold=1 on every row with a file whose effective profile is this one. Use the impact sets from [QUAL-12](#qual-12) when the body passes {only_affected:true}. It returns counts.
    3. **Honour the hold.**
       - upgradeMovie skips held versions (the movie row is the default version).
       - upgradeSeries skips held episodes.
       - wantsEpisodeFile refuses automatic replacement of held episodes.
       - Missing-file grabs are unaffected.
    4. **Clearing the hold.**
       - Cleared on a real import of a new file: movies MarkImported/MarkImportedManual and the version import path, series MarkEpisodeImported.
       - NOT cleared on a convert repoint (RepointMovieFile, RepointEpisodeFile).
       - Cleared when the title's profile is changed (movies SetQualityProfile, series profile change).
       - Cleared by the new 'Resume upgrades' endpoints: POST /movies/{id}/resume-upgrades and POST /series/{id}/resume-upgrades[?season=N].
    5. **UI.**
       - [QUAL-12](#qual-12)'s dialog button 'Save — keep existing files' calls save, then hold-existing.
       - MovieDetail and SeriesDetail show a small chip 'Upgrades paused — kept when the profile changed' with Resume, in the existing chip tokens.
       - ProfileCard shows 'N files kept as is', with counts from the profile-list response.
  - **Files:** `internal/store/migrations/009x_upgrade_hold.sql`, `internal/movies/repo.go`, `internal/movies/service.go`, `internal/series/repo.go`, `internal/series/service.go`, `internal/automation/coordinator.go`, `internal/automation/series_reliability.go`, `internal/automation/reviews.go`, `internal/httpapi/quality.go`, `internal/httpapi/movies.go`, `internal/httpapi/series.go`, `internal/httpapi/server.go`, `web/src/pages/Quality.tsx`, `web/src/pages/MovieDetail.tsx`
  - **Acceptance:**
    - After 'Save — keep existing files', the next upgrade sweep grabs nothing for held files, while missing titles on the profile still grab under the new rules.
    - Importing a new file for a held title clears its hold; converting it does not.
    - Resume on a movie makes it upgradeable on the next sweep, and Resume on a season clears only that season.
    - The detail pages and the profile card show the hold.
  - **Tests:** Go movies and series repo tests: bulk hold; the hold survives a repoint; the hold is cleared on import and on profile change.; Go automation: upgradeMovie's and upgradeSeries' candidate selection skips held rows (via the helpers extracted in QUAL-10), and wantsEpisodeFile refuses a held episode.; UI check: MovieDetail shows the chip and Resume works; ProfileCard shows the kept count.
  - **Depends on:** [QUAL-12](#qual-12)
  - **Risk:** Forgotten holds leave files never upgraded, so the chip and the profile-card count must be visible. A hold column on three tables must be kept consistent by every import path; grep every MarkImported and SetFile path.
  - **Resolves:** quality-5

#### Milestone: M4: Explanations you can trust

_The Why text, 'Chosen over' and the movie profile-change prompt are true. Interactive search explains every row. Series profiles preview episode and pack samples. A release name can be tested against the unsaved profile. Contradictory settings are flagged._

<a id="qual-14"></a>
- [ ] **QUAL-14 · Make today's 'why' copy true: the deciding factor in 'Chosen over', honest winner reasons, the Sources line, and the movie profile-change prompt** — `P1` · `S` · Phase 5
  - **Problem:** Several reasons the app shows are wrong in common cases:
- whyReasons prints 'Highest bitrate under your N Mbps ceiling' whenever a ceiling exists and SmallBias is 0 (quality.go:614-615), even when a +50 Prefer picked a smaller file.
- loseReason falls back to 'fewer preferred extras' (quality.go:629) when the runner-up actually lost on size or seeders.
- The Sources section says 'Arrmada picks the highest-bitrate release' (Quality.tsx:524), but bitrate is only a tie-break.
- The movie profile-change prompt says 'Your current file is higher quality than this profile targets… Download smaller version' whenever WouldReject is true (MovieDetail.tsx:581-584). That includes switching a 1080p file to a 4K-only or Must-HEVC profile.
  - **Approach:** 1. **rank.go: replace loseReason.** Add `decidingFactor(p Profile, winner, runnerUp Evaluation) (RankKey, string)`. It mirrors the final v1 comparator in order:
       - avoided: 'it has X, which you avoid'
       - a QualityScore difference, split into resolution ('lower resolution'), source ('WEBRip, not BluRay', using sourceLabel) and proper ('the PROPER fix replaces it')
       - TargetScore difference ('no HEVC, which you prefer', naming the formats in winner.Matched but not in runnerUp.Matched) and CustomScore difference ('scores lower on your custom formats')
       - SizeScore ('larger, and you prefer smaller files')
       - the health band ('fewer seeders') and zero seeders ('no seeders')
       - magnitude ('lower bitrate', or 'smaller file' without runtimes)
       - source tie, then seeders
    
       ChosenOver uses the text.
    2. **whyReasons.** Claim 'Highest bitrate (under your N Mbps ceiling)' only when the winner's bitrate (or size, without a runtime) is the maximum among non-avoided eligible releases. Otherwise list the matched formats and 'Best fit for your profile'.
    3. **Copy.** Quality.tsx:524 becomes 'Releases rank by resolution, then your preferences, then source; bitrate breaks ties.'
    4. **Downgrade prompt.**
       - Add `quality.Service.WouldRejectReason(ctx, ref string, cur CurrentFile) (string, bool)`, using [QUAL-09](#qual-09)'s CurrentFile when available.
       - handleSetQualityProfile (movies.go:449-462) returns `downgrade_reason` and `downgrade_kind`. The kind is 'smaller' when the reason is the ceiling or MaxSource, and 'different' otherwise.
       - MovieDetail renders either 'Your file is above this profile's N Mb/s ceiling. Download a smaller release, or keep it?' or 'Your file doesn't meet this profile (<reason>). Find a release that does, or keep it?'. The button label matches: 'Download smaller version' or 'Find a matching release'. Both call regrab.
  - **Files:** `internal/quality/rank.go`, `internal/quality/quality.go`, `internal/quality/service.go`, `internal/quality/rank_test.go`, `internal/httpapi/movies.go`, `web/src/pages/Quality.tsx`, `web/src/pages/MovieDetail.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - When a smaller HEVC release wins on Prefer, the Why list does not say 'Highest bitrate'.
    - When the runner-up lost only on seeders, Chosen over reads '— fewer seeders'.
    - Switching a 1080p movie to a 4K-only profile shows the 'doesn't meet this profile (Not in profile — 1080p)' copy with 'Find a matching release'.
    - Switching to a profile with a lower ceiling shows the 'above the ceiling' copy with 'Download smaller version'.
    - The Sources copy no longer claims highest bitrate.
  - **Tests:** Go quality rank_test: decidingFactor table covering every branch (avoid, resolution, source, proper, prefer format, custom, small bias, health band, zero seeders, magnitude).; Go: whyReasons where the Prefer winner is smaller (no bitrate claim), and where the max-bitrate winner gets the claim.; Go: WouldRejectReason for the resolution, Must and ceiling cases.; UI check: in the MovieDetail profile select, switching to a 4K-only profile shows the new copy and button label.
  - **Depends on:** [QUAL-06](#qual-06), [QUAL-07](#qual-07), [QUAL-11](#qual-11)
  - **Risk:** Low. decidingFactor must track the v1 comparator exactly. The comparator tests and decidingFactor tests share fixtures so they can't drift. Ranking v2 (QUAL-22) later returns the deciding key natively and reuses the same text table.
  - **Resolves:** quality-11
<a id="qual-15"></a>
- [ ] **QUAL-15 · Explain every row: matched, avoided, waived and 'ranked lower because…' in interactive search and the builder** — `P2` · `M` · Phase 14
  - **Problem:** RankedRelease (coordinator.go:347-376) carries no matched, avoided or waived information, and RankReleasesWith and RankSeriesReleasesWith drop ChosenOver (coordinator.go:472-496; series_interactive.go ~170-200). The interactive search modal therefore only explains rejected releases plus the winner's templated why (ReleaseSearchModal.tsx:279-283). Users can't see why an eligible release ranked where it did.
  - **Approach:** 1. **quality.Decide.** Fill `Evaluation.LostOn` (a RankKey) and `Evaluation.LostOnText` for each eligible release after the first, using decidingFactor ([QUAL-14](#qual-14)) against the release ranked just above it. Fill `Decision.DecidedBy` for winner versus runner-up. Under v2 ([QUAL-22](#qual-22)) these come from compare natively.
    2. **RankedRelease.** Add `Matched []string`, `Avoided []string`, `BonusWaived bool` and `LostOn string`. Add `ReleaseList.ChosenOver`, filled in RankReleasesWith and RankSeriesReleasesWith.
    3. **ReleaseSearchModal.** Each eligible row shows compact chips in the existing chip and colour tokens:
       - '✓ HEVC' for each matched format
       - '⚠ DV (avoided)' for each avoided one, in the avoid colour
       - 'preference ignored — bitrate too low' when waived
       - a muted line 'ranked lower: <LostOn>'
    
       The chips wrap on phones, with no horizontal scroll.
    4. **Builder.** The Hero's 'Why this one' and 'Chosen over', and the CmpRow eligible list, read the same fields.
  - **Files:** `internal/quality/quality.go`, `internal/quality/rank.go`, `internal/automation/coordinator.go`, `internal/automation/series_interactive.go`, `web/src/components/ReleaseSearchModal.tsx`, `web/src/pages/Quality.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - In movie and series interactive search, every eligible row shows its matched and avoided formats and a 'ranked lower' reason that names the actual deciding factor.
    - The winner's Why list and Chosen over agree with the deciding factor.
    - Waived preferences are labelled.
    - Rows stay readable at 375px.
  - **Tests:** Go quality: Decide populates LostOn for every eligible release after the first, with a fixed candidate set covering resolution, prefer, source and seeders losses.; Go automation: building RankedRelease from a Decision maps matched, avoided, lost_on and chosen_over. Extract the mapping into a helper so it is testable without indexers.; UI check: ReleaseSearchModal on a movie and a series season shows chips and the ranked-lower line, in light and dark and at phone width.
  - **Depends on:** [QUAL-14](#qual-14)
  - **Risk:** Low. More text per row needs a compact layout that stays usable on phones.
  - **Resolves:** quality-11
<a id="qual-16"></a>
- [ ] **QUAL-16 · The series profile preview scores episode and season-pack samples, not Dune** — `P2` · `S` · Phase 14
  - **Problem:** Service.Preview (service.go:405-408) always scores SampleCandidates() (sample.go: Dune: Part Two releases at 166 minutes). A series profile's 'What you'll get' therefore shows movie picks and judges the ceiling at film length, which does not reflect a real series grab (episode sizes, scene WEB, packs).
  - **Approach:** 1. **sample.go.** Add SeriesSampleCandidates(): about 8 releases for a fictional 50-minute show with fictional groups, each carrying its runtime (packs summed):
       - a 2160p DV/HDR WEB-DL H.265
       - a 1080p scene 'WEB.h264' at ~2 GB
       - a 1080p WEB-DL x265 at ~1.2 GB
       - a heavy 1080p BluRay x265 at ~8 GB (over a 15 Mb/s ceiling)
       - a 720p HDTV
       - a fansub '[Group] Show - 05 (1080p) [ABCD1234]'
       - a 1080p 10-episode season pack at 500 minutes
       - a 0-seeder copy of a good release
    2. **Service.Preview.** Pick the sample set by sp.MediaType. The preview response gains `sample_label`: 'Dune: Part Two (2024), 166 min' or 'An episode of a 50-minute show'. Quality.tsx shows it above the sample list in the existing faint caption style.
  - **Files:** `internal/quality/sample.go`, `internal/quality/service.go`, `internal/quality/quality_test.go`, `internal/httpapi/quality.go`, `web/src/pages/Quality.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Editing a Series profile shows the series samples with episode-length bitrates and the series label.
    - Under the '1080p efficient' template (series windows), the 8 GB episode sample is rejected 'Over your 15 Mbps ceiling' (or the series template's ceiling), and a WEB release wins.
    - The movie preview is unchanged.
  - **Tests:** Go quality: Preview on a series profile with a 1080p 5–15 window rejects the heavy BluRay sample and picks a WEB release; the fansub sample is eligible (QUAL-04).; Go: the movie Preview still returns today's Dune winner.; UI check: Quality, Series tab, Edit shows the series sample label.
  - **Depends on:** [QUAL-04](#qual-04), [QUAL-05](#qual-05)
  - **Risk:** Low. Sample names must use fictional show and group names, so they don't look like real indexer data.
  - **Resolves:** quality-1
<a id="qual-17"></a>
- [ ] **QUAL-17 · 'Test a release name' box in the profile builder** — `P2` · `S` · Phase 14
  - **Problem:** GET /api/v1/parse exists (server.go:174, parse.go), but nothing in web/src calls it. A user who wonders why a release never matches a format, or why it is rejected, cannot check a name against the profile they are editing.
  - **Approach:** 1. **Endpoint.** `POST /api/v1/quality/explain` (RoleManager, like /quality/test). Body: {profile: StoredProfile, name, size_gb?, runtime_min?}. Response: {release: parser.Release, evaluation: quality.Evaluation, formats: [{name, matched, score}]}.
       - Add `quality.Service.Explain(sp, name, sizeGB, runtimeMin)`. It normalizes the unsaved profile, evaluates with sp.Engine(), and checks every built-in and custom format against the parsed release.
    2. **UI.** A 'Test a release name' card in the builder's right column, under the Library fit and Test panels, in the same panel style.
       - Inputs: a text field, plus optional size (GB) and runtime (min).
       - Output: parsed chips (resolution with an 'inferred' badge, source with an 'inferred' badge, codec, HDR, audio, group, edition; and, once [QUAL-19](#qual-19) lands, streaming, languages and disc/3D flags), the verdict (eligible; avoided with its formats; or the reject reason), and a table of formats with matched ticks.
       - Debounce 250 ms, as the preview does.
  - **Files:** `internal/quality/service.go`, `internal/httpapi/quality.go`, `internal/httpapi/server.go`, `web/src/pages/Quality.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Pasting 'Show.S01E01.1080p.WEB.h264-GROUP' into a profile with Must HEVC shows the parsed attributes and 'No HEVC — your profile requires it'.
    - Pasting a name that matches a custom format ticks it.
    - The result follows unsaved edits.
    - Requesters (non-managers) get 403.
  - **Tests:** Go httpapi: explain returns the parsed release, the evaluation reject reason and the matched custom format for an unsaved profile; RoleManager is enforced.; UI check: in the builder, the test box shows the verdict and updates live with edits.
  - **Risk:** Low. Keep it manager-only. The chips list grows as parser fields land.
  - **Resolves:** quality-8
<a id="qual-18"></a>
- [ ] **QUAL-18 · Warn about contradictory target settings in the profile editor** — `P3` · `S` · Phase 14
  - **Problem:** The editor accepts contradictory states that make a profile silently behave opposite to what the user meant:
- Prefer HDR10+ with Avoid HDR10 avoids every HDR10+ release, because HDR10+ also tags HDR10 (parser.go:731-733).
- Must DV with Avoid HDR10 rules out DV/HDR10 hybrids.
- Avoid on every codec rules out everything.
  - **Approach:** 1. **quality/ideal.go.** Add `func (sp StoredProfile) Conflicts() []Conflict{Row, Msg string}`. It covers:
       - Must or Prefer HDR10+ together with Avoid HDR10
       - Must DV together with Avoid HDR10 (hybrids are avoided)
       - every option in a row set to Avoid
       - a Must codec or HDR whose row also has every other option avoided, together with a MinSource or MaxSource that excludes where it usually comes from (for example Must DV with 'up to WEB' is fine, but Must AV1 with a BluRay minimum is rare)
       - a window floor above its ceiling
       - a Must format also named in Rejected terms
    
       Messages are plain language.
    2. **API.** The GET profile and preview responses include 'conflicts'.
    3. **UI.** TargetEditor shows each conflict inline under the affected row in the warning tone (the existing avoid-soft style). Save is not blocked.
  - **Files:** `internal/quality/ideal.go`, `internal/quality/ideal_test.go`, `internal/httpapi/quality.go`, `web/src/pages/Quality.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Setting Prefer HDR10+ and Avoid HDR10 shows 'Avoiding HDR10 also avoids HDR10+ (it's HDR10 underneath)' under the HDR row.
    - Clearing the contradiction removes the warning.
    - Save is still allowed.
    - Legitimate setups such as Avoid DV alone show nothing.
  - **Tests:** Go: Conflicts() table test, one case per rule plus a clean profile and Avoid-DV-only with no conflicts.; UI check: the Quality editor shows and clears the warning.
  - **Risk:** Low. The messages must not be noisy.
  - **Resolves:** quality-6

#### Milestone: M5: Unwanted pack on by default

_BR-DISK, 3D and extras are never grabbed. Foreign-only, upscaled, DV-without-fallback and low-quality-group releases are visibly avoided with named reasons. Each item is adjustable per profile._

<a id="qual-19"></a>
- [ ] **QUAL-19 · Parser: detect full-disc (BR-DISK), 3D, upscaled, extras, release language and streaming service** — `P2` · `M` · Phase 14
  - **Problem:** parser.Release has no field for full-disc, 3D, upscale, extras, language or streaming service (parser.go:72-96).
- 'Movie.2020.1080p.Blu-ray.AVC.DTS-HD.MA.5.1-GRP' parses as a BluRay x264 encode. It is really a disc image the importer can't import, because there is no .m2ts or .iso in the importer's videoExts (importer.go:24-27).
- FRENCH and iTALiAN-only releases are not recognised.
- AMZN, NF and ATVP appear only in reQualityStart (parser.go:809), so no format can match them.
  - **Approach:** Add the fields below to parser.Release. Detect them only on the tail after the title cut, so title words are never misread. Compute the tail once in Parse and pass it to the detectors.
    - **FullDisc bool.** TRaSH-style logic in Go code, since RE2 has no lookaround.
      - True for explicit tokens: BDMV, BDISO, ISO, BR-DISK, BD25/BD50/BD66/BD100, UHD.BD50-style tokens, COMPLETE.(UHD.)BLURAY, Full.Blu-ray and 3D.BD.
      - Also true for (bluray|blu-ray|bd|hd-dvd) together with a disc video codec (AVC|HEVC|VC-1|MVC|MPEG-2), when none of remux, bdrip, brrip, an x264/x265/h264/h265 encoder tag or 720p is present. 1080p HEVC counts as an encode.
    - **ThreeD bool:** 3D, SBS, HSBS, H-SBS, Half-OU, HOU, all bounded.
    - **Upscaled bool:** UPSCALED, UPSCALE, AI.UPSCALE. 'Regrade' is excluded.
    - **Extras bool:** EXTRAS, FEATURETTE(S), BONUS, Special.Features, Behind.The.Scenes, all bounded.
    - **Languages []string (ISO 639-1)** and **Multi bool.**
      - FRENCH, TRUEFRENCH, VFF, VF2 and VFQ map to fr; GERMAN to de; iTALiAN to it; SPANiSH, CASTELLANO and LATINO to es; RUSSIAN to ru; HINDI to hi; JAPANESE to ja; KOREAN to ko.
      - Multi is set by MULTi or DUAL, and by German 'DL'/'ML' only when combined with a language token.
    - **Streaming string:** AMZN, NF, ATVP, DSNP, HMAX, MAX, PCOK, PMTP, HULU, iT, CRAV, STAN and MA, all token-bounded. MA counts only next to a WEB token, and iT only in its exact case next to WEB.
    
    summarize() in coordinator.go appends the streaming service when it is present.
  - **Files:** `internal/parser/parser.go`, `internal/parser/unwanted_test.go`, `internal/automation/coordinator.go`
  - **Acceptance:**
    - 'Movie.2020.1080p.Blu-ray.AVC.DTS-HD.MA.5.1-GRP' gives FullDisc true; 'Movie.2020.1080p.BluRay.REMUX.AVC.DTS-HD.MA' and 'Movie.2020.1080p.BluRay.x264' give false.
    - 'Movie.2020.MULTi.1080p.WEB' gives Multi; 'Film.2019.FRENCH.1080p.WEB' gives Languages [fr].
    - 'Show.S01E01.1080p.AMZN.WEB-DL' gives Streaming AMZN; 'Movie.2020.1080p.BluRay.DTS-HD.MA' does not give Streaming MA.
    - Movies titled 'The 3D Man' or 'Extras' are not flagged.
    - All existing parser tests pass.
  - **Tests:** Go new internal/parser/unwanted_test.go, with tables for:
- FullDisc positives and negatives (UHD BD66, BDMV, COMPLETE.UHD.BLURAY, 2160p HEVC BluRay without remux, a 1080p HEVC encode)
- 3D variants, upscaled, extras
- language tokens, including TRUEFRENCH, MULTi and German DL
- streaming tokens, including the MA/iT edge cases
- title-word false positives; Go: the full existing parser suite plus the library and automation suites (the parser is shared with import matching).
  - **Risk:** False positives block good releases once QUAL-20 rejects on them, so detection stays conservative and token-bounded and is tested against real-looking names. Short tokens like MA, iT and DL are the riskiest.
  - **Resolves:** quality-7, quality-8
<a id="qual-20"></a>
- [ ] **QUAL-20 · A built-in 'Unwanted' pack, on by default and visible: BR-DISK, 3D, extras, upscaled, foreign-only, DV without HDR fallback, low-quality groups** — `P2` · `M` · Phase 14
  - **Problem:** There are no defences against the TRaSH 'Unwanted' set:
- Disc images download and then fail to import.
- A family request can come back French-dubbed.
- Profile-5 DV-only files play with wrong colours on clients without DV.
- The low-quality group rule is three hardcoded substring matches with a hidden −180 that no reason ever names (quality.go:76-89, 404-410). A YTS release ranks oddly with no explanation, and a group like 'MYTSGRP' matches by accident.
  - **Approach:** 1. **quality: new condition types.**
       - CondFullDisc, CondThreeD, CondUpscaled, CondExtras, CondForeignLanguage, and CondDVNoFallback (DV present, no HDR10/HDR10+/HLG).
       - A LowQualityGroups list matched case-insensitively and exactly against Release.Group, not as a substring.
       - Candidate gains OrigLanguage via WithLanguage(lang). Automation sets it next to the runtime in every candidate builder: movies from m.Extra.OriginalLanguage (grab, RSS, upgrade, interactive, regrab), series from s.Extra.OriginalLanguage (seriesCandidate from [QUAL-05](#qual-05), and upgrades).
       - Foreign-only means: Release.Languages is non-empty, Multi is false, and no language equals the original language or one of the owner's audio languages. The owner's languages come from Convert's keep_audio_langs, with 'en' when that is empty; once SUB's shared language table lands, use that.
       - An empty original language means no judgement.
    2. **Profile setting.** Migration (next free number, e.g. 009x_quality_unwanted.sql) adds `quality_profiles.unwanted TEXT NOT NULL DEFAULT ''`, a JSON map of item to 'reject'|'avoid'|'allow' ('' means defaults), plus `low_quality_groups` inside the same JSON. Defaults:
       - BR-DISK, 3D and Extras: reject.
       - Upscaled, Foreign-only and Low-quality group: avoid.
       - DV without HDR fallback: avoid, unless the target sets DV to Prefer or Must.
       - Groups seeded with YIFY, YTS, MeGusta.
    3. **Evaluate.** Rejects carry explicit reasons, for example 'A full Blu-ray disc image — Arrmada can't import these' or 'French-only — this title's original language is English'. Avoids join the avoid tier with a named AvoidedFormats entry ('Low-quality group (YTS)'). Remove the hidden −180 and the substring lowQualityGroup().
    4. **UI.** An 'Unwanted' block in the Rules collapsible:
       - one row per item with a Reject/Avoid/Allow segmented control in the PrefPicker style
       - an editable low-quality group chip list
       - rulesSummary mentions non-default choices
    
       The existing '3D' reject-term chip stays for back-compat.
  - **Files:** `internal/quality/quality.go`, `internal/quality/stored.go`, `internal/quality/repo.go`, `internal/quality/presets.go`, `internal/quality/unwanted_test.go`, `internal/store/migrations/009x_quality_unwanted.sql`, `internal/automation/coordinator.go`, `internal/automation/seriesruntime.go`, `internal/automation/series_reliability.go`, `internal/convert/settings.go`, `web/src/pages/Quality.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A BR-DISK release is never grabbed under default settings and shows the disc-image reject reason in interactive search.
    - For an English-original movie, a FRENCH-only release ranks in the avoid tier, while a MULTi release does not.
    - For a Japanese-original anime, a JAPANESE-tagged release is not foreign.
    - A DV-only WEB-DL is avoided on a profile that doesn't prefer DV, and not avoided when DV is Prefer.
    - A YTS release shows 'it has Low-quality group (YTS), which you avoid' instead of a hidden penalty, and a group named 'MYTSGRP' is not matched.
    - Setting an item to Allow removes its effect, and the change shows in the live preview.
  - **Tests:** Go quality unwanted_test:
- each item in its reject, avoid and allow states
- the DV-prefer exception
- original-language handling: en, ja, and an unknown original means no judgement
- the owner-language list
- exact group matching; Go repo round-trip for the unwanted column, with defaults when it is ''.; Go automation: the movie and series candidate builders attach the original language.; UI check: the Rules section shows the Unwanted rows, and changes reflect in the live preview.
  - **Depends on:** [QUAL-19](#qual-19), [QUAL-05](#qual-05)
  - **Risk:** The defaults change grab behaviour for existing profiles; for example, foreign-only avoid on titles whose only releases are dubbed. Avoid, not reject, keeps those as a last resort. YTS moving from −180 to the avoid tier is stricter (below every non-avoided release), so call it out in the commit. Older titles with no original language must mean 'no judgement'.
  - **Resolves:** quality-7

#### Milestone: M6: Ranking v2 (ordered keys)

_Releases are compared key by key: avoid, resolution, preferences, custom, source, group, bitrate, health, size. Custom negatives no longer act as cliffs, and every pick records its deciding key. v2 is validated in shadow mode, then becomes the default with a one-setting rollback._

<a id="qual-21"></a>
- [ ] **QUAL-21 · Ranking v2, step 1: decompose each Evaluation into rank keys (no behaviour change)** — `P2` · `M` · Phase 14
  - **Problem:** Ranking mixes two models:
- Prefer is additive (+50 each, ideal.go:158; HDR10+ also tags HDR10), so stacked Prefers can outscore a higher goal resolution (the 2160p to 1080p gap is 150).
- Any negative format score, custom formats included, sets Avoided, which Decide sorts strictly last (quality.go:420-423, 522-524). A custom format scored −5 behaves like a full Avoid, while keywords stay additive.

The Avoid cliff for target Avoids is intended and documented (Quality.tsx:126), so it should stay there only. Before the comparator can change, each Evaluation has to expose its components.
  - **Approach:** Evaluate additionally fills these fields, which are pure additions; Total and Avoided are computed exactly as today:
    - `TargetAvoided bool`: a target-format Avoid, the bitrate floor, or an Unwanted 'avoid' item.
    - `CustomAvoided bool`: a negative custom-format or keyword score only.
    - `ResTier int`: the goal (the highest allowed resolution) is the top tier, then fallbacks by rank. Any-resolution profiles rank by resRank.
    - `PrefCount int`: matched target Prefer/Must formats.
    - `CustomScore int`: from [QUAL-11](#qual-11), additive, negatives included.
    - `SourceTier int`: Remux 6 > BluRay 5 > WEB 4 (WEB-DL = WEBRip) > HDTV 2 > DVD 1 > unstated 0, plus `ProperFix bool`.
    - `GroupTier int`: 0 until [QUAL-26](#qual-26).
    - `EffBitrate float64`: codec-normalised, 0 without runtime.
    - `InWindow bool`: a bitrate inside the resolution's window, or no window.
    
    waiveCollapsedBonuses also zeroes PrefCount when it waives. Expose the fields in JSON (omitempty) so the Advanced raw view and [QUAL-15](#qual-15) can show them.
  - **Files:** `internal/quality/quality.go`, `internal/quality/rank.go`, `internal/quality/rank_test.go`
  - **Acceptance:**
    - Every existing quality test passes unchanged; no decision changes.
    - For fixed sample releases, the decomposition fields have the expected values (goal tier, prefer count, custom score with negatives, source tier with WEB merged, effective bitrate).
  - **Tests:** Go rank_test: a field table for 8 representative releases under a 4K/1080p profile with prefers, a custom negative and a window.; Go: the whole existing quality suite unchanged.
  - **Depends on:** [QUAL-04](#qual-04), [QUAL-11](#qual-11), [QUAL-20](#qual-20)
  - **Risk:** Low. These are additive fields only. Unwanted avoids must land in TargetAvoided, which is why this depends on QUAL-20.
  - **Resolves:** quality-6
<a id="qual-22"></a>
- [ ] **QUAL-22 · Ranking v2, step 2: the ordered comparator records the deciding key; it ships in shadow mode behind 'quality_ranking'** — `P2` · `M` · Phase 14
  - **Problem:** Even with the decomposed fields, Decide still sorts by summed Total, so the goal resolution and target Avoids can still be outweighed by additive bonuses. The overhaul needs a key-by-key comparator that records what decided each comparison. It must be validated against real indexer results before it changes any grab.
  - **Approach:** 1. **rank.go comparator.** Add `func compare(p Profile, a, b *Evaluation) (int, RankKey)` with this key order:
       1. avoid: TargetAvoided only
       2. resolution: ResTier
       3. preferences: PrefCount
       4. custom: CustomScore
       5. source: SourceTier, then ProperFix
       6. group: GroupTier
       7. bitrate: InWindow first; then EffBitrate, higher wins unless SmallBias>0, in which case lower wins
       8. health: seeders, with magnitudes within 10% treated as equal; zero seeders last
       9. size
    2. **Ranking mode.** Engine gains `Ranking string` ('v1'|'v2'), set by Service from settings key 'quality_ranking' (default 'v1'). Decide sorts with compare under v2 and fills `Evaluation.LostOn` and `Decision.DecidedBy` from it. decidingFactor's text table ([QUAL-14](#qual-14)) is reused for the text.
    3. **Shadow mode.** With v1 active, Service.Decide also runs v2 on every automatic decision and logs `quality: ranking v2 would pick X instead of Y (decided by K)` when the winners differ. Service gets SetLogger, wired in main. DecideSpec, Preview and interactive search can show both winners in the Advanced raw view for the owner's review.
    4. **Unchanged under v2.** MinFormatScore still uses FormatScore. Decide keeps the 'Only releases with a format you avoid' Why line.
    5. **Settings.** Expose 'quality_ranking' in settings.go (owner-only), plus a small Advanced toggle 'Ranking: classic / ordered (preview)' on the Quality page.
  - **Files:** `internal/quality/rank.go`, `internal/quality/quality.go`, `internal/quality/service.go`, `internal/quality/rank_test.go`, `internal/quality/avoidtier_test.go`, `internal/quality/scoring_fixes_test.go`, `internal/quality/ceiling_test.go`, `internal/httpapi/settings.go`, `cmd/arrmada/main.go`, `web/src/pages/Quality.tsx`
  - **Acceptance:**
    - Under v2, a 2160p WEB-DL with 1 Prefer beats a 1080p BluRay with 4 Prefers when both resolutions are allowed.
    - Under v2, a custom format scored −5 lowers a release within its tier without dropping it below every resolution, while a target Avoid still does.
    - Under v2, WEB-DL and WEBRip of the same resolution and preferences are decided by the source bonus, then bitrate.
    - Every v2 Decision carries DecidedBy, and every eligible evaluation after the first has LostOn.
    - In shadow mode the automatic grabs are unchanged, and disagreements are logged with both release names and the key.
  - **Tests:** Go rank_test:
- goal resolution versus stacked prefers
- a custom negative is not a cliff; the target avoid cliff stays
- the floor counts as an avoid
- the WEB source tier merge
- bitrate inside versus outside the window, and the SmallBias direction
- seeders within the 10% band; the zero-seeder release last; Go: run avoidtier_test, scoring_fixes_test and ceiling_test under both v1 and v2 (table-driven over Ranking), documenting each intentional difference inline.; Go: shadow mode logs exactly when the winners differ (capture via a test slog handler).
  - **Depends on:** [QUAL-21](#qual-21), [QUAL-14](#qual-14), [QUAL-07](#qual-07)
  - **Risk:** Even dark, the shadow run doubles Decide cost; it is negligible next to indexer I/O. The default must stay v1 until the owner has reviewed a week of shadow logs.
  - **Resolves:** quality-6
<a id="qual-23"></a>
- [ ] **QUAL-23 · Ranking v2, step 3: upgrades decide by key, the default flips to v2, and v1 stays as a one-setting rollback** — `P2` · `M` · Phase 14
  - **Problem:** Upgrade decisions still use the summed Total (UpgradeCandidate, IsQualityUpgrade), so after the comparator changes, the grab side and the upgrade side would disagree. That disagreement is exactly what produces grab-then-refuse loops.
  - **Approach:** 1. **Upgrade decisions under v2.** UpgradeCandidate and IsQualityUpgrade accept a candidate when `compare(cand, cur)` says better and its key is allowed by the profile's upgrade trigger ([QUAL-11](#qual-11) maps directly):
       - any: resolution, preferences, custom, source, proper
       - source: resolution, preferences, custom, source
       - format: resolution, preferences, custom
       - resolution: resolution
    
       They also accept when IsBitrateUpgrade holds. Group, health and size keys never upgrade. An avoided release never replaces a non-avoided file. The same-group PROPER exception stays. CurrentFile facts ([QUAL-09](#qual-09)) feed the current side.
    2. **Flip the default.** After the owner confirms the shadow logs, change the default of 'quality_ranking' to 'v2' (settings default, not a migration, so an explicit 'v1' keeps working as rollback). Update the Quality.tsx copy that explains ranking (the Sources line and the Advanced toggle).
    3. **Follow-up.** Remove the v1 comparator and shadow mode two weeks after the flip with no rollback, as a separate small commit, and note it in the epic.
  - **Files:** `internal/quality/service.go`, `internal/quality/rank.go`, `internal/quality/bitrateupgrade_test.go`, `internal/quality/rank_test.go`, `internal/httpapi/settings.go`, `web/src/pages/Quality.tsx`
  - **Acceptance:**
    - Under v2, UpgradeCandidate never upgrades on a group, health or size key.
    - Under v2, an upgrade grabbed by the sweep is always accepted by the series import gate (the same verdict).
    - With the default flipped, setting quality_ranking='v1' restores the old behaviour for grabs and upgrades.
    - The bitrateupgrade tests pass under both modes.
  - **Tests:** Go: UpgradeCandidate under v2 never upgrades on group, health or size; the trigger matrix maps to keys; IsQualityUpgrade agrees with UpgradeCandidate on a shared fixture set.; Go: run bitrateupgrade_test under v1 and v2.
  - **Depends on:** [QUAL-22](#qual-22), [QUAL-09](#qual-09), [QUAL-11](#qual-11)
  - **Risk:** This changes every grab and upgrade decision. Mitigations: the shadow week, the dual-run tests and the one-setting rollback. Upgrade semantics need care to avoid loops between keys, for example a source gain that then loses on bitrate; the import gate shares the same function, which prevents a grab-then-refuse.
  - **Resolves:** quality-6

#### Milestone: M7: Format library for power users

_Custom formats support regex, any/all, NOT, aliases and validation, and get a proper editor. Release-group tiers and streaming-service formats are available, and TRaSH custom-format JSON can be imported with explicit warnings._

<a id="qual-24"></a>
- [ ] **QUAL-24 · Custom-format engine: release-title regex, any/all matching, aliases and case-insensitive values, separator-normalised keywords, validation** — `P2` · `M` · Phase 14
  - **Problem:** Custom formats built TRaSH-style silently never fire:
- matchOne compares exact, case-sensitive internal parser values (quality.go:129-148). Codec must be 'x265', not 'HEVC'; DV, not 'Dolby Vision'.
- There is no release-title regex type.
- A CustomFormat is AND-only (quality.go:150-164).
- Keyword and reject terms match the raw lowercased name with its dots kept (quality.go:391, 428), so 'Directors Cut' never matches 'Directors.Cut'.
  - **Approach:** 1. **New condition types.** CondReleaseTitle: Value is a Go regexp, compiled case-insensitively once per Engine and cached in Engine.regex. CondStreaming and CondLanguage use the [QUAL-19](#qual-19) fields.
    2. **Match mode.** CustomFormat gains `Match string` ('all' default | 'any'). Negate already exists per condition.
    3. **Alias normalisation.** Canonical alias maps per type are applied in NewEngine, so stored values are untouched:
       - codec: HEVC/H.265/h265 to x265; AVC/H.264 to x264; av1 to AV1
       - dynamic range: Dolby Vision/DoVi to DV; HDR10Plus to HDR10+
       - audio: 'DTS-HD MA' and 'DTS:X' to DTS-HD; 'Dolby Atmos' to Atmos
       - source: Blu-ray to BluRay; WEB to WEB-DL
    
       Codec, source and resolution comparisons become case-insensitive. Edition compares via parser.FoldAccents with apostrophes stripped.
    4. **Keywords and Rejected.** Normalise separators ('.', '_' and '-' to space) on both the term and the name before containsTerm. Apply the same change to the shared KeywordScore and Rejects helpers (stored.go), which books use, so token bounds are kept ('com' still doesn't match 'Complete').
    5. **Validation.** Add `StoredProfile.Validate() []error`: invalid regex, unknown condition type, empty value. Service.Create and Service.Update return them, and the handlers answer 400 naming the format. Values outside the vocabulary are warnings returned by preview ('warnings').
  - **Files:** `internal/quality/quality.go`, `internal/quality/stored.go`, `internal/quality/service.go`, `internal/quality/quality_test.go`, `internal/quality/customformat_test.go`, `internal/httpapi/quality.go`
  - **Acceptance:**
    - A custom format with codec 'HEVC' matches x265 releases.
    - A release_title regex '\bFraMeSToR\b' matches.
    - A format with Match 'any' over release_group FLUX|NTb matches either group, and a negated condition excludes a group.
    - Keyword 'Directors Cut' scores 'Movie.2020.Directors.Cut.1080p'.
    - Saving an invalid regex returns 400 naming the format.
    - Existing stored formats keep matching exactly as before.
  - **Tests:** Go customformat_test: each alias family, case-insensitivity, release_title regex (valid and invalid), any/all with negate, separator-normalised keyword and reject terms (including 'com' versus 'Complete'), and Validate errors.; Go: a regression test that the stored custom formats of the current seeded profiles produce identical matches on a fixed set of sample names.; Go books: the KeywordScore/Rejects behaviour for book titles is unchanged on the existing books scoring tests.
  - **Depends on:** [QUAL-19](#qual-19)
  - **Risk:** Regex cost per release × per format: compile once per Engine. RE2 is linear-time. Alias normalisation must not change matches for existing exact values. The book keyword path shares the helpers, so run the book scoring tests.
  - **Resolves:** quality-8
<a id="qual-25"></a>
- [ ] **QUAL-25 · Custom-format editor UI: value dropdowns, several conditions, NOT, any/all, and editing existing formats** — `P2` · `M` · Phase 14
  - **Problem:** The builder adds a single condition per format, with a free-text value and the 'FraMeSToR' placeholder for every type (Quality.tsx:819-871). Negate and multiple conditions exist in the backend but aren't exposed, and existing formats can't be edited, only removed and re-added.
  - **Approach:** 1. **Vocabulary.** GET /api/v1/quality/profiles also returns 'vocabulary' from a new quality.ConditionVocabulary(): codec, source, resolution, dynamic_range, audio, edition, streaming and language values, built from parser constants plus the audioTags and editions tables.
    2. **Format cards.** Rewrite the AdvancedPanel custom formats as a list of cards. Each card has:
       - a name and a Match all/any toggle
       - condition rows of [type select] [value] [NOT toggle] [remove]. The value is a dropdown from the vocabulary for enumerable types, a text input for release_group, and a regex input with inline validation (via [QUAL-24](#qual-24)'s Validate in the preview response) for release_title.
       - a score input and an 'Add condition' button
    3. **Placeholders and editing.** Per-type placeholders replace 'FraMeSToR'. Editing a card updates sp.custom_formats in place, and renaming migrates its format_scores key.
    4. **Preview.** It shows 'matches N of the sample releases' per format.
    5. **Layout.** Keep the existing panel and field styles, inside the Advanced collapsible, with no horizontal scroll at 375px.
  - **Files:** `internal/quality/quality.go`, `internal/quality/vocabulary.go`, `internal/quality/vocabulary_test.go`, `internal/httpapi/quality.go`, `web/src/pages/Quality.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - A format with two conditions (source = BluRay AND NOT release_group = YTS) saves, reloads and edits without being re-created.
    - The codec value is chosen from a dropdown showing HEVC/H.264/AV1 (stored canonical).
    - An invalid regex shows an inline error and blocks Save.
    - Renaming a format keeps its score.
  - **Tests:** Go: ConditionVocabulary contains every parser Source and Codec constant and every audio label.; UI check: in the Advanced custom-format editor, adding, editing, NOT, any/all, regex validation and rename (keeping the score) all work, with no horizontal scroll at phone width.
  - **Depends on:** [QUAL-24](#qual-24)
  - **Risk:** Low. The UI grows inside an already long editor (Quality.tsx is about 1,475 lines). Consider moving AdvancedPanel into web/src/pages/quality/AdvancedPanel.tsx as part of this task.
  - **Resolves:** quality-8
<a id="qual-26"></a>
- [ ] **QUAL-26 · Release-group tiers and streaming-service formats** — `P3` · `M` · Phase 14
  - **Problem:** Neither of these is possible today, and TRaSH's guides rely on both:
- Tiering release groups, for example FraMeSToR above other remux groups, or preferring particular WEB groups.
- Preferring AMZN/MA/ATVP web sources.
  - **Approach:** 1. **Group tiers.** The profile gains GroupTiers [{tier:1..2, groups:[]}] in a new JSON column (migration 009x_quality_group_tiers.sql; '' = built-in defaults). Built-in defaults come from a small, clearly editable list in presets.go: a few remux tier-1 names and WEB tier-1 and tier-2 names as starting suggestions. Group matching is case-insensitive and exact on Release.Group.
    2. **Ranking.** Evaluate fills GroupTier ([QUAL-21](#qual-21)). The v2 group key ([QUAL-22](#qual-22)) ranks tier 1 above tier 2 above untiered, after source. Under v1 it has no effect, which is documented in the UI hint.
    3. **Streaming formats.** Add built-in formats for AMZN, NF, ATVP, DSNP, HMAX/MAX, MA and PCOK (CondStreaming) to DefaultFormats as non-target 'other formats', so they can be preferred in Advanced. Add them to formatMeta with group 'streaming'.
    4. **UI.** A 'Release groups' block in Advanced with two tier rows of chips plus an add input, in the existing chip style. The streaming formats appear in the existing Other formats list.
  - **Files:** `internal/quality/presets.go`, `internal/quality/stored.go`, `internal/quality/repo.go`, `internal/quality/quality.go`, `internal/quality/rank.go`, `internal/quality/rank_test.go`, `internal/store/migrations/009x_quality_group_tiers.sql`, `web/src/pages/Quality.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Under v2, two otherwise-equal 2160p remuxes rank the tier-1 group first, and Chosen over says 'not a tier-1 release group'.
    - Prefer AMZN ranks an AMZN WEB-DL above an otherwise-equal one.
    - Editing the tiers changes the live preview.
    - Group tiers never reject anything.
  - **Tests:** Go rank_test: GroupTier key cases and the streaming format match.; Go: repo round-trip for group tiers, with defaults when ''.; UI check: tier chips can be added and removed.
  - **Depends on:** [QUAL-22](#qual-22), [QUAL-19](#qual-19)
  - **Risk:** Shipped group lists go stale and are opinionated, so keep them minimal and editable, and never use them to reject.
  - **Resolves:** quality-8
<a id="qual-27"></a>
- [ ] **QUAL-27 · Import TRaSH custom-format JSON into a profile** — `P3` · `M` · Phase 14
  - **Problem:** Power users who maintain a TRaSH/Recyclarr setup have to rebuild every custom format by hand in Arrmada's editor.
  - **Approach:** 1. **quality/trash.go.** Add `ImportTrashCF(json []byte) ([]CustomFormat, []string /*warnings*/)`, which maps TRaSH specifications:
       - ReleaseTitleSpecification to CondReleaseTitle. The regex is converted to RE2 where possible; lookarounds are reported as unsupported and that condition is skipped.
       - SourceSpecification to source, via a value map.
       - ResolutionSpecification to resolution.
       - LanguageSpecification to language.
       - ReleaseGroupSpecification to release_group or a regex.
       - IndexerFlag and other specs are unsupported and reported.
    
       'negate' maps to Negate. TRaSH semantics (required specs AND; non-required specs of one type OR) are approximated with Match any/all, and any lossy conversion is reported.
    2. **Endpoint.** `POST /api/v1/quality/import-trash {json}` (RoleManager) returns {formats, warnings} without saving.
    3. **UI.** An 'Import from TRaSH' button in Advanced opens a paste box (the TemplatePicker dialog style). It previews the converted formats and warnings, then adds the chosen ones to the profile with score 0 for the user to set. Nothing is saved until the profile is saved.
  - **Files:** `internal/quality/trash.go`, `internal/quality/trash_test.go`, `internal/quality/testdata/trash/`, `internal/httpapi/quality.go`, `internal/httpapi/server.go`, `web/src/pages/Quality.tsx`, `web/src/lib/api.ts`
  - **Acceptance:**
    - Pasting TRaSH's 'BR-DISK' or 'x265 (HD)' CF JSON produces equivalent formats, or clear warnings for the parts that can't be expressed.
    - Imported formats match the same sample names the TRaSH regex intends (for the RE2-expressible parts).
    - Nothing is saved until the user adds the formats and saves the profile.
  - **Tests:** Go trash_test.go with 3–4 TRaSH CF JSON fixtures under testdata, covering mapped conditions, an unsupported lookaround that is reported, and negate/required handling.
  - **Depends on:** [QUAL-24](#qual-24), [QUAL-25](#qual-25)
  - **Risk:** Many TRaSH regexes use lookarounds that RE2 can't run, so imports can be lossy. The warnings must be explicit so nothing silently never matches. The fixtures are public TRaSH JSON, which contains no credentials.
  - **Resolves:** quality-8

#### Risks

- Ranking and parser changes alter what gets grabbed across the whole library. The plan contains them as follows:
- Each behaviour change is its own commit, called out in the message: WEB tier, TV bitrate window, Unwanted defaults, seeders band, upgrade budget.
- v2 ships in shadow mode behind 'quality_ranking', with dual-run tests and a one-setting rollback.
- The default flips only after the owner has reviewed a week of shadow logs.
- TMDB episode runtimes are often 0 or wrong, and packs may hold fewer episodes than TMDB lists. Once QUAL-05 ships, wrong pack bitrates can cause false ceiling rejections or push lean TV encodes into the floor's avoid tier. Mitigations: the median fallback, counting only aired episodes, logging the computed runtime with rejections, and series-specific template windows confirmed with the owner.
- The parser is shared with import matching and title resolution, not just quality. Every parser task runs the full parser, library and automation suites. New detections run only on the tail after the title cut.
- Two tasks rewrite stored user data. The codec-stamp repair (QUAL-03) is guarded by a settings key. The dangling-profile repair (QUAL-02) touches only refs whose profile id does not exist. Both are idempotent and log their counts (and before/after samples). Neither touches media files.
- Migration-number collisions: several epics add migrations to main in parallel. Each QUAL migration takes the next free number at implementation time (from 0090 up), never a pre-reserved one.
- Quality.tsx is about 1,475 lines, and roughly 14 QUAL tasks edit it, so merge conflicts and regressions in the existing look are likely. Mitigations:
- Extract touched sub-panels (AdvancedPanel, UpgradesEditor, ProfileCard) into web/src/pages/quality/*.tsx as each task touches them.
- Keep the palette, terracotta accent and type scale unchanged.
- Check light and dark at phone width.
- Upgrade guard rails (the budget, Replace-for, holds) can delay upgrades the owner actually wants. Every deferral is logged. Holds are visible as chips and counts with a Resume action.
- Automation tests have no fake indexer (indexers is a concrete *indexer.Service). Tasks that need sweep-level tests extract pure helpers (per-version pick and grab loops, RankedRelease mapping) rather than mocking HTTP. That refactor must not change behaviour.
- Race tests must be run in Docker before each push. CI runs go test -race on Linux, and the owner gets an email for every red push.

#### Out of scope

- Stall-timeout default on templates and its migration, and the per-indexer min_seeders default (ACQ, quick win #4)
- Series grab-policy unification: pack tiers in GrabBestForScope, its manual flag skipping the import gate, the numbering source (SER)
- Convert's DV handling (dovi_tool/profile 8.1), convert_history/Revert, and the originals hold (CONV)
- Book and music quality ranking (format-score pickers and the music ladder) beyond the reassign-on-delete and resolver fixes (BOOK, MUS)
- Movie versions: Edition as a Must condition, import routing by grabs.version_id, Plex edition tags (MOV). Its conditions can later use QUAL-24's engine.
- The Quality editor's unsaved-changes guard and the general design-system work (FE)
- Indexer-level category filtering to exclude BR-DISK categories, and usenet support
- Importing .m2ts/.iso disc images. Full discs are rejected instead.
- Scheduled TRaSH/Recyclarr sync. QUAL-27 is paste-only import.
- Per-user (requester) quality choices; profiles stay owner/manager-managed

