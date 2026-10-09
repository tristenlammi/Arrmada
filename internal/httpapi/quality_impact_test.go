package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// impactServer has a 1080p movie profile with upgrades on, and five films with 1080p H.264
// files: three monitored on it, one unmonitored on it, and one on another profile.
func impactServer(t *testing.T) (*routeServer, quality.StoredProfile, *http.Cookie) {
	t.Helper()
	s := newRouteServer(t, func(d *Deps) {
		d.Quality = quality.NewService(d.Store.DB())
		d.Movies = movies.NewService(d.Store.DB(), nil, nil, t.TempDir(), "", nil, d.Log)
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	ctx := context.Background()
	sp, err := s.deps.Quality.Create(ctx, quality.StoredProfile{MediaType: quality.MediaMovie, Name: "HD", UpgradesEnabled: true, AllowedResolutions: []string{"1080p"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.deps.Quality.Create(ctx, quality.StoredProfile{MediaType: quality.MediaMovie, Name: "Other", UpgradesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ref, otherRef := fmt.Sprintf("custom:%d", sp.ID), fmt.Sprintf("custom:%d", other.ID)
	films := []struct {
		title     string
		ref       string
		monitored bool
		gb        int64
	}{
		{"Heat", ref, true, 10},
		{"Ronin", ref, true, 8},
		{"Thief", ref, true, 6},
		{"Collateral", ref, false, 9},    // unmonitored: the sweep never looks
		{"Manhunter", otherRef, true, 7}, // another profile
	}
	for i, f := range films {
		media := fmt.Sprintf(`{"path":"/m/%[1]s.mkv","filename":"%[1]s.mkv","size_bytes":%d}`, f.title, f.gb<<30)
		if _, err := s.st.DB().Exec(`INSERT INTO movies (tmdb_id, title, year, runtime, quality_profile, monitored, has_file, movie_file_path, media_json, source_release)
			VALUES (?, ?, 1995, 120, ?, ?, 1, ?, ?, ?)`,
			i+1, f.title, f.ref, f.monitored, "/m/"+f.title+".mkv", media, f.title+".1995.1080p.BluRay.x264-GRP"); err != nil {
			t.Fatal(err)
		}
	}
	return s, sp, mgr
}

func TestQualityImpactCountsTheProfilesFiles(t *testing.T) {
	s, sp, mgr := impactServer(t)
	edited := sp
	edited.Ideal = &quality.IdealFile{Codec: map[string]string{"hevc": quality.PrefMust}}
	body, _ := json.Marshal(map[string]any{"profile": edited})
	rec := s.doJSON("POST", "/api/v1/quality/impact", mgr, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var got quality.Impact
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Replace.Files != 3 || got.Replace.Bytes != (10+8+6)<<30 {
		t.Errorf("replace = %+v, want Heat, Ronin and Thief (24 GiB)", got.Replace)
	}
	if len(got.Replace.Examples) != 3 || got.Files != 3 {
		t.Errorf("examples %v, judged %d", got.Replace.Examples, got.Files)
	}
	for _, ex := range got.Replace.Examples {
		if ex == "Collateral (1995)" || ex == "Manhunter (1995)" {
			t.Errorf("%s counted: it's unmonitored or on another profile", ex)
		}
	}

	// Nothing was saved.
	stored, _ := s.deps.Quality.GetStored(context.Background(), fmt.Sprintf("custom:%d", sp.ID))
	if stored.Ideal != nil {
		t.Errorf("the dry run saved the edit: %+v", stored.Ideal)
	}

	// The profile as saved changes nothing.
	body, _ = json.Marshal(map[string]any{"profile": stored})
	rec = s.doJSON("POST", "/api/v1/quality/impact", mgr, string(body))
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Replace.Files+got.Search.Files != 0 {
		t.Errorf("unchanged profile: %s", rec.Body)
	}
}

func TestQualityImpactRefusesUnsavedProfiles(t *testing.T) {
	s, _, mgr := impactServer(t)
	if rec := s.doJSON("POST", "/api/v1/quality/impact", mgr, `{"profile":{"id":0,"name":"new"}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unsaved: HTTP %d", rec.Code)
	}
	if rec := s.doJSON("POST", "/api/v1/quality/impact", mgr, `{"profile":{"id":999,"name":"gone"}}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: HTTP %d", rec.Code)
	}
}

// "Save — keep existing files": holding the files an edit affects takes them out of the
// next dry run, shows on the profile card, and Resume on a movie lets it go again.
func TestHoldExistingAndResume(t *testing.T) {
	s, sp, mgr := impactServer(t)
	ctx := context.Background()
	edited := sp
	edited.Ideal = &quality.IdealFile{Codec: map[string]string{"hevc": quality.PrefMust}}
	body, _ := json.Marshal(map[string]any{"only_affected": true, "profile": edited})
	rec := s.doJSON("POST", fmt.Sprintf("/api/v1/quality/profiles/%d/hold-existing", sp.ID), mgr, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var held map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &held); err != nil || held["movies"] != 3 || held["held"] != 3 {
		t.Fatalf("held %s, want the 3 affected films (not the unmonitored one)", rec.Body)
	}

	// The dry run now finds nothing worse: the affected files are kept as they are.
	body, _ = json.Marshal(map[string]any{"profile": edited})
	rec = s.doJSON("POST", "/api/v1/quality/impact", mgr, string(body))
	var got quality.Impact
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Replace.Files != 0 {
		t.Errorf("impact after holding: %s", rec.Body)
	}

	// The profile card counts them.
	list, err := s.deps.Quality.List(ctx, quality.MediaMovie)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.Key == fmt.Sprintf("custom:%d", sp.ID) && p.Kept != 3 {
			t.Errorf("profile card kept = %d, want 3", p.Kept)
		}
	}

	// Resume on one film.
	var heatID int64
	_ = s.st.DB().QueryRow(`SELECT id FROM movies WHERE title = 'Heat'`).Scan(&heatID)
	rec = s.doJSON("POST", fmt.Sprintf("/api/v1/movies/%d/resume-upgrades", heatID), mgr, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: HTTP %d: %s", rec.Code, rec.Body)
	}
	var n int
	_ = s.st.DB().QueryRow(`SELECT COUNT(*) FROM movies WHERE upgrade_hold = 1`).Scan(&n)
	if n != 2 {
		t.Errorf("%d films still held after resuming one, want 2", n)
	}
	if rec := s.doJSON("POST", "/api/v1/movies/999/resume-upgrades", mgr, ""); rec.Code != http.StatusNotFound {
		t.Errorf("resume unknown movie: HTTP %d", rec.Code)
	}

	// Without only_affected every file on the profile is held, the unmonitored one too.
	rec = s.doJSON("POST", fmt.Sprintf("/api/v1/quality/profiles/%d/hold-existing", sp.ID), mgr, `{}`)
	_ = s.st.DB().QueryRow(`SELECT COUNT(*) FROM movies WHERE upgrade_hold = 1`).Scan(&n)
	if rec.Code != http.StatusOK || n != 4 {
		t.Errorf("hold all: HTTP %d, %d held, want 4", rec.Code, n)
	}
	if rec := s.doJSON("POST", fmt.Sprintf("/api/v1/quality/profiles/%d/hold-existing", sp.ID), mgr, `{"only_affected":true}`); rec.Code != http.StatusBadRequest {
		t.Errorf("only_affected without the profile: HTTP %d", rec.Code)
	}
}
