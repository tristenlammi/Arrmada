package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func musicCallBody(h http.Handler, method, path, session, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:5000"
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// musicEditionServer is musicGateServer that also hands back the database.
func musicEditionServer(t *testing.T) (http.Handler, *settings.Service, string, *sql.DB) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	authSvc := auth.NewService(st.DB())
	u, err := authSvc.CreateUser(ctx, "manager", "correct-horse-battery", auth.RoleManager, true)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := authSvc.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	set := settings.NewService(st.DB())
	// Listings only: opening the picker would otherwise list editions from the real
	// MusicBrainz.
	svc := music.NewService(st.DB(), listingOnly{metadata.NewMusicBrainz(nil, nil)}, slog.Default())
	svc.SetSettingsLookup(set.Lookup)
	srv := New(Deps{Log: slog.Default(), Store: st, Auth: authSvc, Settings: set, Music: svc})
	return srv.Handler, set, session, st.DB().(*sql.DB)
}

// MUS-30's routes: 404 while Music is off; the editions list; a switch by hand (400 for a
// release that isn't the album's).
func TestMusicEditionRoutes(t *testing.T) {
	h, set, session, db := musicEditionServer(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/music/albums/1/editions"}, {"PUT", "/api/v1/music/albums/1/edition"},
	} {
		if rec := musicCallBody(h, c.method, c.path, session, `{}`); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s while off = %d, want 404", c.method, c.path, rec.Code)
		}
	}
	ctx := context.Background()
	if err := set.SetBool(ctx, settings.KeyModuleMusic, true); err != nil {
		t.Fatal(err)
	}

	// An album with two stored editions, the standard the target.
	repo := music.NewRepo(db)
	a, err := repo.CreateArtist(ctx, music.Artist{MBID: "mb-rh", Name: "Radiohead", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	albumID, _ := repo.UpsertAlbum(ctx, music.Album{ArtistID: a.ID, MBID: "rg-okc", Title: "OK Computer", Monitored: true})
	std, _ := repo.AddEdition(ctx, albumID, music.EditionInput{Edition: music.Edition{MBID: "rel-std", Country: "GB", ReleaseDate: "1997-05-21", MediaSummary: "CD", Status: "Official"},
		Tracks: []music.EditionTrack{{Disc: 1, Position: 1, Title: "Airbag", RecordingMBID: "r1"}, {Disc: 1, Position: 2, Title: "Lucky", RecordingMBID: "r2"}}})
	us, _ := repo.AddEdition(ctx, albumID, music.EditionInput{Edition: music.Edition{MBID: "rel-us", Country: "US", ReleaseDate: "1997-07-01", MediaSummary: "CD", Status: "Official"},
		Tracks: []music.EditionTrack{{Disc: 1, Position: 1, Title: "Airbag", RecordingMBID: "r1"}, {Disc: 1, Position: 2, Title: "Lucky", RecordingMBID: "r2"}}})
	if err := repo.SetTargetEdition(ctx, albumID, std, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordTrackFile(ctx, firstTrack(t, repo, albumID), music.FileRecord{Path: "/m/okc/01.flac", Quality: music.QualityFLAC}); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/music/albums/" + strconv.FormatInt(albumID, 10)

	rec := musicCallBody(h, "GET", base+"/editions", session, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("editions = %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Editions []struct {
			ID           int64  `json:"id"`
			Words        string `json:"words"`
			Country      string `json:"country"`
			Target       bool   `json:"target"`
			SameAsTarget bool   `json:"same_as_target"`
			Acceptable   bool   `json:"acceptable"`
			Files        int    `json:"files"`
		} `json:"editions"`
		Pinned bool `json:"pinned"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Editions) != 2 || !list.Editions[0].Target || list.Editions[0].ID != std || list.Editions[0].Files != 1 {
		t.Fatalf("editions %+v, want the standard first, as the target, with its file", list.Editions)
	}
	if !list.Editions[1].SameAsTarget || !list.Editions[1].Acceptable || list.Editions[1].Words == "" || list.Editions[1].Country != "US" {
		t.Errorf("US edition %+v, want marked the same as the target", list.Editions[1])
	}

	if rec := musicCallBody(h, "PUT", base+"/edition", session, `{"release_id": 999999}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown release = %d, want 400", rec.Code)
	}
	if rec := musicCallBody(h, "PUT", "/api/v1/music/albums/424242/edition", session, `{"release_id": 1}`); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown album = %d, want 404", rec.Code)
	}
	rec = musicCallBody(h, "PUT", base+"/edition", session, `{"release_id": `+strconv.FormatInt(us, 10)+`, "pinned": true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"moved":1`) {
		t.Fatalf("switch = %d %s, want 200 with the file moved", rec.Code, rec.Body.String())
	}
	al, _ := repo.GetAlbum(ctx, albumID)
	if al.TargetEditionID != us || !al.EditionPinned || al.HaveTracks != 1 {
		t.Errorf("album after the switch: target %d pinned %v have %d", al.TargetEditionID, al.EditionPinned, al.HaveTracks)
	}
}

func firstTrack(t *testing.T, repo *music.Repo, albumID int64) int64 {
	t.Helper()
	tracks, err := repo.TracksFor(context.Background(), albumID)
	if err != nil || len(tracks) == 0 {
		t.Fatalf("tracks: %v %v", tracks, err)
	}
	return tracks[0].ID
}
