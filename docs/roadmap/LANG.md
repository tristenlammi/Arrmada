# Audio languages: badges on files and language rules in profiles

Status: built on `feat/audio-languages` (2026-10-16), from `main` @ dcf987f1. No migration.

Inspired by GitHub issue #12, which asked for two things: show the audio languages of the files on disk as badges on the movie and series pages, and colour them by whether the file matches the profile's languages. Profiles had no language settings, so the second half is new: an **Audio language** row in the quality profile's target, with the same choices as the codec, HDR and audio rows.

## Part 1 — what the files carry

- **Data source: Convert's library index.** `convert_library` already holds an analysis of every movie and episode file (`IndexAll` at startup and nightly, `IndexMovie`/`IndexSeries` after every import, rename and conversion), including every audio stream's language tag. The badges read it; a page load never probes a file. So coverage is library-wide already, through an existing throttled background pass (the tool runner's idle priority, one file at a time), and no new scanner was needed. A file not analysed yet — or analysed by an older analysis version, or replaced since (its size no longer matches) — shows no badges until the index reaches it.
- **Untagged tracks.** A track tagged `und` or nothing takes the language the subtitle AI heard on it when it has listened (the subtitles probe cache's `detected`, with the same confidence threshold the AI rung uses), read by `subtitles.DetectedAudio` from the cache alone and wired into Convert in `main` (Convert must not import subtitles). It is marked as detected (a dashed chip, "detected by the AI" in its tooltip). Otherwise it is one `?` chip.
- **Commentary tracks** are left out: an English commentary on a German dub isn't English audio.
- **Codes and order:** the one language table (`internal/langs`) folds every spelling (ger/deu, fre/fra, pt-BR…) to the base ISO 639-1 code, shown upper-case, with the English name in the tooltip and accessible name. One chip per language (a tag beats a detection), the title's original language first, then by code, an unidentified track last (`langs.OrderAudio`).
- **Where:** each movie version's file panel, each episode card, and a per-season line ("DE 10/10 · EN 9/10", plus how many files miss the language rules).
- **API:** `GET /api/v1/movies/{id}` adds `audio_langs` and `audio_lang_fit` to each version; `GET /api/v1/series/{id}` to each episode. Both are omitted when there's nothing to show. List endpoints are unchanged.

## Part 2 — language rules in profiles

The target (`IdealFile.Language`) maps a base code, or `original` (the title's TMDB `original_language`), to Must / Prefer / Avoid. No entries = no rules = exactly the old behaviour.

**What a release carries, by its name** (`quality/lang.go releaseLangs`, from the parser's `Languages`/`Multi`/`Dual`):

- no language named → the original language (assumed, and the release row says so);
- MULTi / German DL or ML → the original plus the languages it names;
- DUAL / Dual Audio naming nothing, on a foreign title → the original plus English (how anime and foreign-film groups use it); on an English title → English plus an unnamed other;
- MULTi naming nothing → the original plus others it doesn't name: a Prefer it doesn't name is **unknown** — never credited, never counted against it;
- a tagged name (GERMAN, TRUEFRENCH/VFF, iTALiAN, ITA.ENG…) → exactly those.

**A Must has to be named (issue #58).** A release on offer passes a Must only when its name names the language, or the language is the title's original and the release is untagged or MULTi/DL/Dual. A MULTi or Dual naming nothing, on a title whose original is a different language, is refused ("Doesn't name Italian audio…"), unless the profile's `multi_counts_language` switch is on (off by default and on every profile saved before it; migration 0347): then an unnamed Must is unknown, as it was before. Files on disk are never refused on what a name leaves out, so the rule changes grabbing only.

A title whose original language isn't known, with a release that names none, can't be judged: no rule applies to it (a Must doesn't refuse it), and an `original` entry is dropped for that title. TMDB's `xx` (no spoken language) counts as unknown; its `cn` and `yue` (Cantonese, most Hong Kong films) read as Chinese (`langs.AudioBase`). A MULTi or Dual naming nothing is still refused under a Must when the original isn't known.

**Subtitles named after the word (issue #58, second report).** Italian trackers write "ENG Sub ITA" / "Sub-ITA": English audio, Italian subtitles. The languages straight after Sub/Subs/Subbed are subtitles, and a language before such a word is the audio when subtitle languages follow it ("ENG.Sub.ITA" → English); with nothing after ("ENG.SUBS", "German.Subbed") it stays subtitles, as before. The old reading took "ENG.Sub.ITA" for Italian audio, so English-only releases passed an Italian Must and the files kept passing it. This reading applies to files on disk too (unlike the new spellings below): it only moves verdicts for profiles with a rule on the subtitle's language, toward what the file really carries.

The spellings added with #58 (FRA, DEU, NLD, ESP, JAP, VFB — capitals, never where an episode title sits) count for releases on offer only: a library file is read without them (`parser.Release.ForFile`), so the update moves no file's verdict.

**A file on disk** (`fileLangs`) is never judged on an assumption:

- its probed tracks (Convert's facts, AI-heard languages included) win over its name;
- an untagged name says nothing about a file (where a release on offer is read as the original), so a file with no analysis and a name that states no language — or only untagged tracks — is **unknown**: it fits every Must, isn't upgraded for a language (an upgrade gets no credit for a language the file might already carry — `upgradeBar.creditable`), meets the target as far as languages go, and its badges have no colour. Adding a language rule to a profile whose files nobody has read therefore re-downloads nothing (tested);
- an untagged track beside tagged ones is read as the name states, when it does; otherwise the set stays open (an unnamed Must/Prefer is unknown, as above);
- probe vs name: tools write `eng` when nobody set a language, so when every tagged track is English and the name explicitly names other languages, the file is read as both rather than letting a default tag contradict the name. Any other tag (an `ita` track in a file named GERMAN) wins.
- Norwegian's written forms (nb/nob, nn/nno, no/nor) are one language for audio (`langs.AudioBase`).

**The choices:**

| | Release on offer | File on disk |
|---|---|---|
| **Must** | refused when its name is known not to carry the language (reject code `missing_language`, "Missing German audio — your profile requires it"). Every Must is needed: they're languages a file has together, not alternatives like codecs. | known to lack it: doesn't fit — like any other Must, the upgrade sweep replaces it (v2: under every trigger; classic: when the trigger counts a preferences gain) |
| **Prefer** | each preferred (or required) language it has counts on the **preferences** key (v2) / scores +50 like a preferred format (classic). Not waived for a thin bitrate: the language is the point. | known to lack one: the target isn't met, so upgrades keep looking (preferences gain) |
| **Avoid** | a release whose *every* language is avoided goes to the avoided tier. An extra audio track costs nothing, so dual audio with one avoided language isn't avoided. | leaving it is a preferences gain, like an avoided target format; never upgraded *into* one |

**Badge colour on the detail pages** (`StoredProfile.LanguageFit`, the same reading as the engine): green — every Must, and a Prefer when any are set; orange — every Must but none of the preferred; red — a Must is missing, or every language is avoided; no colour without rules, and none when the file can't be judged (nothing to read, or an untagged track that might be the language asked for). A ✓ / ! / ✕ mark and a sentence in the tooltip and accessible name go with the colour, so colour is never the only signal.

## Decisions

1. **One row in the target, not a separate setting.** It sits beside codec, HDR and audio, uses the same buttons, is saved in the same `ideal` JSON column (no migration), and appears in the profile summary line ("German audio required · original audio preferred").
2. **Both rankings.** Must and Avoid live in `Evaluate`, which both share; Prefer is one more count on v2's preferences key and the format score classic already sums. Implementing classic was cheap, and leaving the rollback without language rules would have made "roll back to v1" quietly change what's grabbed.
3. **Foreign-only audio stands aside per release.** A release carrying a language the profile requires or prefers is never foreign-only to it (a German-preferring profile would otherwise see every GERMAN release avoided). Every other dub still is: "Prefer original" or "Prefer German" doesn't let an Italian-only 2160p beat the English 1080p. A release both would avoid is named once, by the language rule — no double counting (the tier is one tier anyway). The editor says so under the item.
4. **No foreign-only judgement on files, still.** The title's original language reaches files on disk and the import gate for the language rules only (`Candidate.ruleLang`), never `OrigLanguage`, so the foreign-only check keeps never judging a library file — otherwise profiles without language rules would have changed.
5. **Fit bars stay picture-and-sound.** `CheckFit` (the library Fit bars, Cutoff unmet) doesn't take languages; their verdict is the badges' colour. A profile with only language rules gets no Fit bars, as before it had any target (`IdealFile.FitEmpty`).
6. **Budgets, and no wave on a guess.** Adding a rule to a big profile makes every file *known* to lack the language upgradeable; the existing per-sweep upgrade budget applies to them like any other upgrade (tested), and the save dialog's dry run counts them first. Files nobody has read aren't judged at all (above).
7. **Parser extensions, conservative:** a language word where an episode title sits counts when a dub tag follows it (DL, ML, Dubbed) or when it's the only word there, so the technical tokens follow it ("Show.S01E01.German.720p", "…German.AC3.720p") — "Going.Dutch.720p" and "The.German.Spy" stay titles; PLDUB/DUBPL; a few more capitalised three-letter codes; DUAL told apart from MULTi (`Release.Dual`); everyday words (FIN, NOR, DAN) stay words.
8. **The parser changes reach profiles without rules too — intended.** The release languages the parser now reads (the TV dub positions, PLDUB, and the newly read POL/POR/DUT/CZE/HUN/TUR/UKR/CHI/HIN/SWE/HEB) also feed the Unwanted pack's foreign-only item and custom formats' language conditions for every profile. A HINDI or GERMAN-only dub of an English film that went unrecognised before is now foreign-only (avoided by default) — which is what that item always meant to do. The ranking and upgrade goldens have no such names, so they're unchanged.

## Checklist

- [x] Parser: more language tags, with tests (German.DL in TV names, PLDUB, POL/POR/DUT/CZE/HUN/TUR/UKR/CHI/HIN/SWE/HEB).
- [x] `langs.AudioLang` / `OrderAudio` / `AudioFit` (normalisation tests).
- [x] `IdealFile.Language`: read/normalised/compiled, summary line, book/music profiles never apply it.
- [x] Engine: Must reject (`missing_language`), Prefer on the preferences key and classic's score, Avoid tier; v1 and v2; `describe` lifts the language Must for not-fitting files; escape from an avoided language.
- [x] Foreign-only defers to Must/Prefer rules; not named twice beside a language Avoid.
- [x] Files on disk: Convert facts carry track languages (AI-heard for untagged), probed wins over the name; `CurrentFile.OrigLanguage` from the movie/show; import gate; save dry run (`ImpactFile.OrigLanguage`, `LibraryEpisodeFile.OrigLanguage`).
- [x] Goldens: ranking v1/v2 and upgrade goldens unchanged; new `ranking_lang_golden.json` for both rankings.
- [x] Per-sweep budget respected with a language rule (automation test).
- [x] Detail API: `audio_langs` / `audio_lang_fit` on movie versions and episodes (API shape tests).
- [x] Interactive search rows show the languages a name carries; refused rows say which language is missing.
- [x] UI: badges on movie files, episode cards and the season line; the Audio language row in the profile editor (searchable list + Original language); the foreign-only note.
- [x] e2e: series and movie detail badges, the editor row, release rows; contrast for the badges and the row.
- [x] User docs (docs/features.md).
- [x] Review fixes (each with a test): foreign-only stands aside per release, not per profile; German TV dubs where an episode title sits (alone, or before Dubbed/DL/ML); an unnamed Must/Prefer in a MULTi is unknown, and DUAL on a foreign title is original + English; files with nothing to read are left unjudged (no wave when a rule is added — automation test); Norwegian written forms folded; an all-English probe can't contradict a name that names other languages; `DetectedAudio` looks paths up by key in batches of 200; LANG.md notes that the parser changes reach profiles without rules (intended).

## Not done here / follow-ups

- Badges don't appear on list pages (by design: list endpoints stay light).
- The library Fit bars and Cutoff unmet don't list language misses; the badges' colour is that verdict. Adding language issues to `CheckFit` would need the title's original language in every Fit read.
- ~~The AI only listens to an untagged track when a subtitle needs it.~~ Done for #90: `subtitles/identify.go` listens to every untagged, non-commentary track in the library once (three 30 s samples at ¼, ½ and ¾ of the runtime, majority vote, mean p ≥ 0.6, else kept as "couldn't tell" and not asked again), one file at a time while the subtitle queue is idle, imports first, after each library pass. Answers go in the probe cache's `detected`, which `DetectedAudio` already serves to Convert; the file is never touched. A heard language sets `FileFacts.AudioHeard`, which keeps the file's set open in `fileLangs`: it satisfies a Must/Prefer, but a miss beside it is unknown to the engine (no upgrade, grab or impact count); `LanguageFit` alone judges it at its word, with a note. A failed sample is a retry, not a "couldn't tell"; zxx tracks are skipped; whisper never runs beside a GPU-lane job (`Service.gpu`), and a background run never probes or retires the oneAPI build. Setting `subs_identify_audio`, on by default, inert without whisper and a model.
- An "Original language" entry does nothing for titles without a TMDB original language (rare).
- Regional variants (pt-BR vs pt-PT audio) are treated as their base language: release names and track tags rarely say which.
