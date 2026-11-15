package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/flaresolverr"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// `arrmada music-source-test` answers one question before any on-demand music is built:
// how much music do the connected indexers actually have, and how long does finding it
// take? It searches for a list of albums and reports, per group, how many were found, in
// which formats, with how many seeders, and how long each search took. It only searches:
// nothing is grabbed, downloaded or written to the database.

// testAlbum is one album to look for. Group names what kind of album it is, so coverage
// can be read per group (mainstream is easy everywhere; the rest is what decides a source).
type testAlbum struct{ Group, Artist, Album string }

// builtinTestAlbums is the default sample: 80 albums in five groups of 16.
var builtinTestAlbums = func() []testAlbum {
	groups := []struct {
		name string
		list [][2]string
	}{
		{"mainstream, recent", [][2]string{
			{"Taylor Swift", "The Tortured Poets Department"}, {"Billie Eilish", "Hit Me Hard and Soft"},
			{"Kendrick Lamar", "GNX"}, {"Sabrina Carpenter", "Short n' Sweet"}, {"Charli xcx", "Brat"},
			{"Beyoncé", "Cowboy Carter"}, {"Chappell Roan", "The Rise and Fall of a Midwest Princess"},
			{"Tyler, the Creator", "Chromakopia"}, {"The Weeknd", "Hurry Up Tomorrow"}, {"Dua Lipa", "Radical Optimism"},
			{"Olivia Rodrigo", "Guts"}, {"Fontaines D.C.", "Romance"}, {"Linkin Park", "From Zero"},
			{"Bad Bunny", "Debí Tirar Más Fotos"}, {"Lady Gaga", "Mayhem"}, {"Travis Scott", "Utopia"},
		}},
		{"classics", [][2]string{
			{"Pink Floyd", "The Dark Side of the Moon"}, {"Fleetwood Mac", "Rumours"}, {"Radiohead", "OK Computer"},
			{"Nirvana", "Nevermind"}, {"The Beatles", "Abbey Road"}, {"Michael Jackson", "Thriller"},
			{"Daft Punk", "Discovery"}, {"Metallica", "Master of Puppets"}, {"Miles Davis", "Kind of Blue"},
			{"Led Zeppelin", "Led Zeppelin IV"}, {"Kanye West", "My Beautiful Dark Twisted Fantasy"},
			{"Amy Winehouse", "Back to Black"}, {"Dr. Dre", "The Chronic"}, {"David Bowie", "Low"},
			{"Stevie Wonder", "Songs in the Key of Life"}, {"Lauryn Hill", "The Miseducation of Lauryn Hill"},
		}},
		{"Australian", [][2]string{
			{"Tame Impala", "Currents"}, {"King Gizzard & The Lizard Wizard", "Nonagon Infinity"},
			{"AC/DC", "Back in Black"}, {"Midnight Oil", "Diesel and Dust"}, {"The Avalanches", "Since I Left You"},
			{"Gang of Youths", "Go Farther in Lightness"}, {"Courtney Barnett", "Sometimes I Sit and Think, and Sometimes I Just Sit"},
			{"Powderfinger", "Odyssey Number Five"}, {"Silverchair", "Diorama"}, {"Flume", "Skin"},
			{"Amyl and the Sniffers", "Cartoon Darkness"}, {"Rüfüs Du Sol", "Surrender"}, {"Paul Kelly", "Gossip"},
			{"Hilltop Hoods", "The Calling"}, {"Cold Chisel", "East"}, {"Parkway Drive", "Reverence"},
		}},
		{"less mainstream", [][2]string{
			{"Slowdive", "Souvlaki"}, {"Big Thief", "Dragon New Warm Mountain I Believe in You"},
			{"Boards of Canada", "Music Has the Right to Children"}, {"Burial", "Untrue"}, {"Death Grips", "The Money Store"},
			{"Black Country, New Road", "Ants from Up There"}, {"Floating Points", "Promises"}, {"Swans", "To Be Kind"},
			{"Godspeed You! Black Emperor", "Lift Your Skinny Fists Like Antennas to Heaven"}, {"JPEGMAFIA", "Veteran"},
			{"Alvvays", "Blue Rev"}, {"Caroline Polachek", "Desire, I Want to Turn Into You"}, {"MF DOOM", "Mm..Food"},
			{"Khruangbin", "Con Todo el Mundo"}, {"Have a Nice Life", "Deathconsciousness"}, {"Duster", "Stratosphere"},
		}},
		{"non-English and niche", [][2]string{
			{"Rosalía", "Motomami"}, {"Stromae", "Racine carrée"}, {"Rammstein", "Mutter"},
			{"Fishmans", "Long Season"}, {"Mariya Takeuchi", "Variety"}, {"Sigur Rós", "Ágætis byrjun"},
			{"Buena Vista Social Club", "Buena Vista Social Club"}, {"Molchat Doma", "Etazhi"}, {"BTS", "Map of the Soul: 7"},
			{"Gilberto Gil", "Refazenda"}, {"Ichiko Aoba", "Windswept Adan"}, {"Tinariwen", "Emmaar"},
			{"Mdou Moctar", "Afrique Victime"}, {"Haruomi Hosono", "Hosono House"}, {"Kino", "Gruppa krovi"},
			{"Fela Kuti", "Zombie"},
		}},
	}
	var out []testAlbum
	for _, g := range groups {
		for _, a := range g.list {
			out = append(out, testAlbum{Group: g.name, Artist: a[0], Album: a[1]})
		}
	}
	return out
}()

// readTestAlbums reads "Artist - Album" lines (blank lines and # comments skipped).
func readTestAlbums(r io.Reader) ([]testAlbum, error) {
	var out []testAlbum
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		artist, album, ok := strings.Cut(line, " - ")
		if !ok || strings.TrimSpace(artist) == "" || strings.TrimSpace(album) == "" {
			return nil, fmt.Errorf("line %d: want \"Artist - Album\", got %q", n, line)
		}
		out = append(out, testAlbum{Group: "your list", Artist: strings.TrimSpace(artist), Album: strings.TrimSpace(album)})
	}
	return out, sc.Err()
}

// albumFinding is what one album's search came to.
type albumFinding struct {
	testAlbum
	took     time.Duration
	matches  int // releases that name this artist and album and aren't another kind of record
	lossless int
	lossy    int
	seeders  int // the best-seeded match (torrents)
	usenet   int // matches from usenet indexers
	err      string
}

// judgeReleases counts the releases that are this album, as the music search would read
// their names (music.WhyNotForAlbum, music.OtherReleaseKind).
func judgeReleases(a testAlbum, rels []indexer.Release) albumFinding {
	f := albumFinding{testAlbum: a}
	al := music.Album{Title: a.Album}
	for _, r := range rels {
		if music.WhyNotForAlbum(r.Title, a.Artist, a.Album) != "" || music.OtherReleaseKind(r.Title, a.Artist, al) != "" {
			continue
		}
		f.matches++
		switch music.DetectQuality(r.Title) {
		case music.QualityFLAC, music.QualityFLAC24, music.QualityALAC, music.QualityWAV:
			f.lossless++
		default:
			f.lossy++
		}
		if r.Transport == indexer.TransportUsenet {
			f.usenet++
		}
		if r.Seeders > f.seeders {
			f.seeders = r.Seeders
		}
	}
	return f
}

func cmdMusicSourceTest(ctx context.Context, c *cli, args []string) int {
	fs := newFlags(c, "music-source-test")
	file := fs.String("albums", "", "a text file of \"Artist - Album\" lines to test instead of the built-in 80")
	limit := fs.Int("limit", 0, "test only the first N albums")
	gap := fs.Duration("gap", 2*time.Second, "pause between searches, to go easy on the indexers")
	pos, stop, code := parseFlags(fs, args)
	if stop {
		return code
	}
	if len(pos) > 0 {
		fmt.Fprintf(c.stderr, "arrmada music-source-test: unexpected argument %q\n", pos[0])
		return exitUsage
	}
	albums := builtinTestAlbums
	if *file != "" {
		fh, err := os.Open(*file)
		if err != nil {
			fmt.Fprintf(c.stderr, "arrmada music-source-test: %v\n", err)
			return exitFail
		}
		albums, err = readTestAlbums(fh)
		_ = fh.Close()
		if err != nil || len(albums) == 0 {
			fmt.Fprintf(c.stderr, "arrmada music-source-test: %s: %v (no albums read)\n", *file, err)
			return exitFail
		}
	}
	if *limit > 0 && *limit < len(albums) {
		albums = albums[:*limit]
	}

	st, err := store.OpenNoMigrate(c.cfg.DataDir)
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada music-source-test: %v\n", err)
		return exitFail
	}
	defer func() { _ = st.Close() }()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Wired as the server wires it, so a search here behaves as one from the app: the
	// FlareSolverr address and the default-categories choice come from the saved settings.
	settingsSvc, err := settings.Open(ctx, st.DB())
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada music-source-test: couldn't read the settings: %v\n", err)
		return exitFail
	}
	svc := indexer.NewService(st.DB(), log, flaresolverr.NewFunc(apikeys.NewStore(settingsSvc).Func("flaresolverr")))
	svc.SetBundledProwlarr(c.cfg.AIOProwlarrURL)
	svc.SetDefaultCategories(func(ctx context.Context) bool {
		return settingsSvc.GetBool(ctx, indexer.KeyDefaultCategories, true)
	})

	all, err := svc.List(ctx)
	if err != nil {
		fmt.Fprintf(c.stderr, "arrmada music-source-test: couldn't read the indexers: %v\n", err)
		return exitFail
	}
	var serving []string
	for _, idx := range all {
		if idx.Enabled && idx.Serves(indexer.MediaMusic) {
			serving = append(serving, fmt.Sprintf("%s (%s)", idx.Name, idx.Transport()))
		}
	}
	if len(serving) == 0 {
		fmt.Fprintln(c.stderr, "arrmada music-source-test: no enabled indexer is set to search Music.")
		fmt.Fprintln(c.stderr, "In Arrmada, open Settings → Indexers, edit the indexer, tick Music, save, and run this again.")
		return exitFail
	}
	fmt.Fprintf(c.stdout, "Searching %d albums on: %s\n", len(albums), strings.Join(serving, ", "))
	fmt.Fprintf(c.stdout, "Search only: nothing is downloaded. About %s.\n\n", (time.Duration(len(albums)) * (*gap + 2*time.Second)).Round(time.Minute))

	ictx := indexer.WithInteractive(ctx)
	var found []albumFinding
	for i, a := range albums {
		if ctx.Err() != nil {
			break
		}
		start := time.Now()
		res, err := svc.Search(ictx, indexer.SearchQuery{
			Text: a.Artist + " " + a.Album, Artist: a.Artist, Album: a.Album, MediaType: indexer.MediaMusic, Limit: 100,
		})
		f := judgeReleases(a, res.Releases)
		f.took = time.Since(start)
		if err != nil {
			f.err = err.Error()
		} else if len(res.Errors) > 0 && len(res.Releases) == 0 {
			names := make([]string, 0, len(res.Errors))
			for n, e := range res.Errors {
				names = append(names, n+": "+e)
			}
			sort.Strings(names)
			f.err = strings.Join(names, "; ")
		}
		found = append(found, f)
		mark := "MISSING"
		if f.matches > 0 {
			mark = fmt.Sprintf("found %d", f.matches)
		}
		if f.err != "" {
			mark = "ERROR " + f.err
		}
		fmt.Fprintf(c.stdout, "[%2d/%d] %-9s %5.1fs  %s - %s\n", i+1, len(albums), mark, f.took.Seconds(), a.Artist, a.Album)
		if i < len(albums)-1 {
			select {
			case <-ctx.Done():
			case <-time.After(*gap):
			}
		}
	}
	printMusicTestReport(c.stdout, found)
	return exitOK
}

// printMusicTestReport prints coverage per group and overall, the search times, and the
// albums not found.
func printMusicTestReport(w io.Writer, found []albumFinding) {
	type tally struct {
		n, hit, lossless, healthy, usenet, errs int
	}
	var order []string
	by := map[string]*tally{}
	total := &tally{}
	var times []time.Duration
	var missing []string
	for _, f := range found {
		t := by[f.Group]
		if t == nil {
			t = &tally{}
			by[f.Group] = t
			order = append(order, f.Group)
		}
		for _, x := range []*tally{t, total} {
			x.n++
			if f.err != "" {
				x.errs++
			}
			if f.matches > 0 {
				x.hit++
			}
			if f.lossless > 0 {
				x.lossless++
			}
			if f.seeders >= 5 {
				x.healthy++
			}
			if f.usenet > 0 {
				x.usenet++
			}
		}
		if f.err == "" {
			times = append(times, f.took)
		}
		if f.matches == 0 && f.err == "" {
			missing = append(missing, f.Artist+" - "+f.Album)
		}
	}
	pct := func(a, b int) string {
		if b == 0 {
			return "  -"
		}
		return fmt.Sprintf("%3d%%", a*100/b)
	}
	fmt.Fprintf(w, "\n%-24s %6s %7s %9s %11s %7s\n", "GROUP", "ALBUMS", "FOUND", "LOSSLESS", "5+ SEEDERS", "ERRORS")
	row := func(name string, t *tally) {
		fmt.Fprintf(w, "%-24s %6d %7s %9s %11s %7d\n", name, t.n, pct(t.hit, t.n), pct(t.lossless, t.n), pct(t.healthy, t.n), t.errs)
	}
	for _, g := range order {
		row(g, by[g])
	}
	row("ALL", total)
	if total.usenet > 0 {
		fmt.Fprintf(w, "On usenet: %d of %d albums.\n", total.usenet, total.n)
	}
	if len(times) > 0 {
		sort.Slice(times, func(i, k int) bool { return times[i] < times[k] })
		fmt.Fprintf(w, "\nSearch time: median %.1fs, slowest tenth from %.1fs, slowest %.1fs.\n",
			times[len(times)/2].Seconds(), times[len(times)*9/10].Seconds(), times[len(times)-1].Seconds())
	}
	if len(missing) > 0 {
		fmt.Fprintf(w, "\nNot found (%d):\n", len(missing))
		for _, m := range missing {
			fmt.Fprintln(w, "  "+m)
		}
	}
	fmt.Fprintln(w, "\n\"Found\" means a release names the artist and the album and isn't a live, demo or best-of")
	fmt.Fprintln(w, "record of another kind. It doesn't prove the files are right: nothing was downloaded.")
}
