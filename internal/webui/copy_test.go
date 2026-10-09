package webui

// The copy guard. Every stale string a copy audit found had been true once and was never
// revisited: "coming soon" on shipped features, an env var to set for a key the UI manages,
// a page that no longer exists. This test fails CI when one of those retired phrases comes
// back, in the web UI (web/src, every line) or in a Go string literal (internal/ and cmd/,
// tests excluded — comments may explain history freely).
//
// When a change retires a phrase, add it to bannedCopy with the reason, written as what to
// say instead. A line containing "copy-ok" is exempt, for a deliberate exception.

import (
	"bufio"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type bannedPhrase struct {
	re      *regexp.Regexp
	reason  string
	webOnly bool // only checked in web/src (the backend legitimately names it, e.g. config)
}

func phrase(pattern, reason string) bannedPhrase {
	return bannedPhrase{re: regexp.MustCompile(`(?i)` + pattern), reason: reason}
}

func webPhrase(pattern, reason string) bannedPhrase {
	p := phrase(pattern, reason)
	p.webOnly = true
	return p
}

var bannedCopy = []bannedPhrase{
	// COPY-02: the TMDB key is entered in the UI and works at once.
	webPhrase(`ARRMADA_TMDB_API_KEY`, "keys are entered in Settings → System → API keys and take effect without a restart — use <MetadataMissing/>"),
	phrase(`Settings → API keys`, "API keys live under Settings → System → API keys (link LINKS.apiKeys)"),
	// COPY-03 / COPY-05 / COPY-08: no "soon" on shipped work, no roadmap promises.
	phrase(`coming soon`, "say what the feature does today; Insights tabs are built — show a Connect Plex state instead"),
	phrase(`\(soon\)`, "describe what ships; if it isn't built, leave the control out"),
	phrase(`once [a-z ]+ ships`, "don't promise future work in the UI"),
	phrase(`isn['’]t built yet`, "a bad URL is 'Page not found' (pages/NotFound.tsx)"),
	phrase(`on the roadmap`, "describe what ships, not what's planned"),
	phrase(`lands with the`, "don't promise future work in the UI"),
	phrase(`AI are coming`, "AI transcription has shipped — say whether a model is installed"),
	phrase(`AI \+ embedded only`, "list the sources that work right now (Subtitles header pill)"),
	phrase(`stripped from the video`, "Subtitles never edits the video; Convert drops languages under its own setting"),
	// COPY-03: pages that don't exist.
	phrase(`(appear|show)[a-z]* in Activity`, "there is no Activity page — name Downloads → Searching via PAGE.downloads"),
	phrase(`Requests page`, "there is no Requests page — requests show in the row on Discover (LINKS.requests)"),
	// COPY-09: what requesters actually get.
	phrase(`Discover-only`, "requesters get Discover, Calendar, Books and Audiobooks — list the real pages"),
	phrase(`only the Discover page`, "requesters get Discover, Calendar, Books and Audiobooks — list the real pages"),
	webPhrase(`update\.sh`, "the UI never sends people to the install scripts; name the setting or the container"),
	// COPY-08: the Insights intro only asks to connect while Plex isn't connected.
	webPhrase(`Connect your server in`, "the intro's call to action is the 'Connect your Plex server in the Settings tab' button, shown only while not connected"),
	// COPY-10: system strings that described an older build.
	webPhrase(`auth_enabled`, "sign-in is always enforced, so a constant Auth stat says nothing — leave it off the Dashboard"),
	phrase(`kept in memory, up to`, "the viewer loads up to LOG_LIMIT lines; Arrmada keeps 50,000 in memory and writes rotating log files"),
	webPhrase(`ARRMADA_BASE_URL`, "sub-path hosting isn't supported — Arrmada is served at the root of its own port or hostname"),
	webPhrase(`dev-docker`, "builds are stamped with a dated version and the short commit (update.sh)"),
	// COPY-11: books come from Hardcover, Open Library or Google Books — name the right one.
	webPhrase(`openlibrary\.org/works/\$\{`, "link the book's own catalogue with b.catalogue.url (books.CatalogueLink); Hardcover and Google Books keys aren't Open Library works"),
	phrase(`search Open Library and re-link`, "Change match searches the current catalogue — say 'search the catalogue'"),
	phrase(`this author on Open Library`, "name the catalogue that answered: Hardcover for hc:a: authors, else Open Library"),
	// COPY-04: no music upgrade sweep exists.
	phrase(`keeps upgrading until`, "music albums aren't upgraded after the first grab"),
	phrase(`upgrades later if`, "music albums aren't upgraded after the first grab"),
	phrase(`Replace an album when`, "music albums aren't upgraded after the first grab"),
	// COPY-06: the upgrade sweep skips unmonitored films and non-upgrading profiles.
	phrase(`If your quality profile allows upgrades`, "say what happens, from upgrades_allowed"),
	// CONV-05: Convert copy.
	phrase(`and it plays everywhere`, "HEVC plays on most devices made since about 2016 (H.264 is the one that plays everywhere)"),
	phrase(`whatever the hours`, "a request starts when a conversion slot is free"),
	phrase(`every audio track (is|are) copied`, "tracks filtered out in Convert → Settings are removed"),
}

// match returns the first banned phrase on a line, or nil. web says whether the line is
// from the web UI (webOnly phrases apply there only).
func match(line string, web bool) *bannedPhrase {
	if strings.Contains(line, "copy-ok") {
		return nil
	}
	for i := range bannedCopy {
		b := &bannedCopy[i]
		if b.webOnly && !web {
			continue
		}
		if b.re.MatchString(line) {
			return b
		}
	}
	return nil
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("no go.mod above the test directory")
		}
		dir = parent
	}
}

func TestNoRetiredCopy(t *testing.T) {
	root := repoRoot(t)
	var hits []string
	report := func(path string, line int, text string, b *bannedPhrase) {
		rel, _ := filepath.Rel(root, path)
		hits = append(hits, rel+":"+itoa(line)+": "+strings.TrimSpace(b.re.FindString(text))+" — "+b.reason)
	}

	web := filepath.Join(root, "web", "src")
	if _, err := os.Stat(web); err != nil {
		t.Log("web/src not present — checking Go strings only")
	} else {
		_ = filepath.WalkDir(web, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			if !(strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".tsx")) || strings.Contains(name, ".test.") {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1024*1024), 1024*1024)
			for n := 1; sc.Scan(); n++ {
				if b := match(sc.Text(), true); b != nil {
					report(path, n, sc.Text(), b)
				}
			}
			return nil
		})
	}

	for _, dir := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			lines := strings.Split(string(src), "\n")
			fset := token.NewFileSet()
			file := fset.AddFile(path, fset.Base(), len(src))
			var s scanner.Scanner
			s.Init(file, src, nil, 0) // comments skipped: they may explain history
			for {
				pos, tok, lit := s.Scan()
				if tok == token.EOF {
					break
				}
				if tok != token.STRING {
					continue
				}
				ln := fset.Position(pos).Line
				if ln-1 < len(lines) && strings.Contains(lines[ln-1], "copy-ok") {
					continue
				}
				if b := match(lit, false); b != nil {
					report(path, ln, lit, b)
				}
			}
			return nil
		})
	}

	for _, h := range hits {
		t.Error(h)
	}
}

// The matcher itself: every banned phrase is caught, a copy-ok line isn't, and ordinary
// wording that only resembles a phrase ("as soon as") passes.
func TestCopyGuardMatcher(t *testing.T) {
	samples := []string{
		`set ARRMADA_TMDB_API_KEY and restart`,
		`add one in Settings → API keys`,
		`Coming soon`,
		`or (soon) AI transcription`,
		`stripped (once stripping ships)`,
		`Not found isn't built yet`,
		`This module is on the roadmap.`,
		`Health scoring lands with the sync phase`,
		`image subs + AI are coming`,
		`AI + embedded only`,
		`everything else is stripped from the video`,
		`it'll appear in Activity once grabbed`,
		`it'll show in Activity once grabbed`,
		`they'll appear on the Requests page`,
		`a Requester account (Discover-only)`,
		`Requesters see only the Discover page.`,
		`re-run ./update.sh`,
		`Connect your server in <b>Settings</b> to begin.`,
		`<Stat k="Auth" v={status?.auth_enabled ? "enabled" : "disabled"} />`,
		`lines — kept in memory, up to 5000.`,
		`set ARRMADA_BASE_URL to serve under a path`,
		`version dev-docker`,
		"<a href={`https://openlibrary.org/works/${b.ol_key}`}>Open Library</a>",
		`This is the wrong book — search Open Library and re-link it to the right one`,
		`Couldn't find more books for this author on Open Library.`,
		`keeps upgrading until it reaches the top`,
		`and upgrades later if one appears`,
		`Replace an album when a higher tier turns up`,
		`If your quality profile allows upgrades, Arrmada keeps watching`,
		`the best quality and it plays everywhere`,
		`it starts right away, whatever the hours`,
		`Atmos and every audio track are copied untouched`,
	}
	caught := map[*bannedPhrase]bool{}
	for _, s := range samples {
		b := match(s, true)
		if b == nil {
			t.Errorf("not caught: %q", s)
			continue
		}
		caught[b] = true
	}
	for i := range bannedCopy {
		if !caught[&bannedCopy[i]] {
			t.Errorf("no sample exercises %q", bannedCopy[i].re)
		}
	}
	for _, ok := range []string{
		`Coming soon — copy-ok: quoting the old text on purpose`,
		`subtitles as soon as it imports`,
		`Downloads → Searching`,
		`Settings → System → API keys`,
	} {
		if b := match(ok, true); b != nil {
			t.Errorf("false positive on %q (%s)", ok, b.re)
		}
	}
	if match(`env("ARRMADA_TMDB_API_KEY", "")`, false) != nil {
		t.Error("web-only phrases must not apply to Go strings")
	}
}

func itoa(n int) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}
