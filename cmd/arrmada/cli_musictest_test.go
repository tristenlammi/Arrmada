package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/store"
)

func torznabItem(title string, seeders int) string {
	return fmt.Sprintf(`<item><title>%s</title><guid>g-%s</guid><link>http://x/dl</link><size>350000000</size>
<enclosure url="http://x/dl/%s.torrent" length="350000000" type="application/x-bittorrent"/>
<torznab:attr name="seeders" value="%d"/><torznab:attr name="category" value="3040"/></item>`, title, title, title, seeders)
}

// The command searches each album, counts only releases that are that album, and reports
// coverage, formats and what is missing. Nothing is grabbed: the fake indexer's download
// address is never requested.
func TestMusicSourceTestReportsCoverage(t *testing.T) {
	var searches, downloads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/dl") {
			downloads++
		}
		q := r.URL.Query()
		if q.Get("t") == "caps" {
			_, _ = io.WriteString(w, `<caps><searching><search available="yes" supportedParams="q"/><music-search available="yes" supportedParams="q"/></searching><categories><category id="3000" name="Audio"/></categories></caps>`)
			return
		}
		searches++
		items := ""
		if text := strings.ToLower(q.Get("q") + " " + q.Get("album")); strings.Contains(text, "ok computer") {
			items = torznabItem("Radiohead - OK Computer (1997) [FLAC]", 42) +
				torznabItem("Radiohead - OK Computer (1997) [MP3 320]", 3) +
				torznabItem("Radiohead - OK Computer Live in Berlin [FLAC]", 9) + // another kind of record
				torznabItem("Coldplay - Parachutes [FLAC]", 100) // not this album
		}
		_, _ = io.WriteString(w, `<?xml version="1.0"?><rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`+items+`</channel></rss>`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc := indexer.NewService(st.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if _, err := svc.Create(context.Background(), indexer.Indexer{Name: "Fake", Kind: indexer.KindTorznab, URL: srv.URL, APIKey: "k", Enabled: true, Priority: 25}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	list := dir + "/albums.txt"
	if err := writeFile(list, "# mine\nRadiohead - OK Computer\nNobody - An Album Nobody Has\n"); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	c := &cli{stdin: strings.NewReader(""), stdout: &out, stderr: &errOut, cfg: config.Config{DataDir: dir}}
	if code := cmdMusicSourceTest(context.Background(), c, []string{"--albums", list, "--gap", "0s"}); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"Searching 2 albums on: Fake (torrent)",
		"found 2", // the FLAC and the MP3; not the live one, not Coldplay
		"MISSING",
		"your list                     2     50%       50%         50%       0",
		"Not found (1):",
		"Nobody - An Album Nobody Has",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if searches < 2 {
		t.Errorf("searches = %d, want one per album", searches)
	}
	if downloads != 0 {
		t.Errorf("the command fetched %d downloads; it must only search", downloads)
	}
}

func TestReadTestAlbums(t *testing.T) {
	got, err := readTestAlbums(strings.NewReader("\n# c\nAC/DC - Back in Black\n  Sigur Rós - Ágætis byrjun \n"))
	if err != nil || len(got) != 2 || got[0].Artist != "AC/DC" || got[1].Album != "Ágætis byrjun" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := readTestAlbums(strings.NewReader("just a title\n")); err == nil {
		t.Error("a line without \" - \" was accepted")
	}
	if len(builtinTestAlbums) != 80 {
		t.Errorf("built-in list has %d albums, want 80", len(builtinTestAlbums))
	}
}

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o644) }
