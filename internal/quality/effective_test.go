package quality

import (
	"strconv"
	"testing"
)

// The hidden fallback only runs when no profile of the media type exists, and even
// then it must never make a cam eligible.
func TestFallbackProfileRejectsPreRelease(t *testing.T) {
	s, ctx := testService(t)
	d := s.Decide(ctx, "custom:999", []Candidate{
		NewCandidate("Movie.2024.HDCAM.x264-GRP", 1.4, 500),
		NewCandidate("Movie.2024.TELESYNC.x264-GRP", 1.6, 300),
	})
	if d.Winner != nil || len(d.Eligible) != 0 {
		t.Errorf("fallback made a pre-release eligible: winner=%+v eligible=%d", d.Winner, len(d.Eligible))
	}
	d = s.Decide(ctx, "custom:999", []Candidate{NewCandidate("Movie.2024.1080p.WEB-DL.x264-GRP", 6, 50)})
	if d.Winner == nil {
		t.Error("the fallback should still take a normal release")
	}
}

// Effective is the one resolver: a real ref stays, "n/a", empty and deleted refs go
// to the default of the media type, and with no profile at all the ref comes back.
func TestEffectiveResolvesToDefault(t *testing.T) {
	s, ctx := testService(t)
	if got := s.Effective(ctx, "custom:999", MediaMovie); got != "custom:999" {
		t.Errorf("no profiles: got %q, want the ref unchanged", got)
	}
	sp, err := s.Create(ctx, StoredProfile{MediaType: MediaMovie, Name: "1080p"})
	if err != nil {
		t.Fatal(err)
	}
	def := "custom:" + strconv.FormatInt(sp.ID, 10)
	for _, ref := range []string{"custom:999", "n/a", "", "junk"} {
		if got := s.Effective(ctx, ref, MediaMovie); got != def {
			t.Errorf("Effective(%q) = %q, want the default %q", ref, got, def)
		}
	}
	if got := s.Effective(ctx, def, MediaMovie); got != def {
		t.Errorf("a real ref changed to %q", got)
	}
	if got := s.Effective(ctx, "custom:999", MediaSeries); got != "custom:999" {
		t.Errorf("a movie default leaked to series: %q", got)
	}
}

// "n/a" is never a default: SetDefaultProfile refuses it, and one already saved is
// passed over, so Effective always hands back a real profile.
func TestDefaultProfileIsNeverNA(t *testing.T) {
	s, ctx := testService(t)
	// The shared fixture has no settings table; the saved default lives there.
	if _, err := s.repo.db.ExecContext(ctx, `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	sp, err := s.Create(ctx, StoredProfile{MediaType: MediaMovie, Name: "1080p"})
	if err != nil {
		t.Fatal(err)
	}
	def := "custom:" + strconv.FormatInt(sp.ID, 10)
	if err := s.SetDefaultProfile(ctx, MediaMovie, "n/a"); err == nil {
		t.Error(`SetDefaultProfile accepted "n/a"`)
	}
	if err := s.repo.setSetting(ctx, "default_profile:"+MediaMovie, "n/a"); err != nil {
		t.Fatal(err)
	}
	if got := s.DefaultProfile(ctx, MediaMovie); got != def {
		t.Errorf(`DefaultProfile with "n/a" saved = %q, want %q`, got, def)
	}
	if got := s.Effective(ctx, "custom:999", MediaMovie); got != def {
		t.Errorf("Effective = %q, want %q", got, def)
	}
}
