package quality

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/parser"
	_ "modernc.org/sqlite"
)

// --- Fix 1: source enters the quality score ---------------------------------

// A 1080p HDCAM must lose to a 1080p WEB-DL even when the cam file is bigger.
// Before sourceBonus was wired into qualityScore, both scored the bare 1080p
// base and the bigger cam won the higher-bitrate tiebreak.
func TestHDCAMLosesToWebDLDespiteSize(t *testing.T) {
	cands := []Candidate{
		NewCandidate("Movie.2024.1080p.HDCAM.x264-BAD", 9, 500),
		NewCandidate("Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GOOD", 8, 500),
	}
	d := NewDefaultEngine().Decide(fallbackProfile(), cands)
	if d.Winner == nil || !strings.Contains(d.Winner.Candidate.Name, "WEB-DL") {
		t.Fatalf("expected the WEB-DL to beat the bigger HDCAM, winner = %+v", d.Winner)
	}
}

// TestSourceBonusInvariants pins the ordering the bonus table promises and the
// cap that keeps source from ever outweighing a resolution tier.
func TestSourceBonusInvariants(t *testing.T) {
	order := []parser.Source{
		parser.SourceRemux, parser.SourceBluray, parser.SourceWebDL,
		parser.SourceWebRip, parser.SourceHDTV, parser.SourceDVD,
		parser.SourceUnknown, parser.SourceCAM,
	}
	for i := 1; i < len(order); i++ {
		if sourceBonus[order[i-1]] <= sourceBonus[order[i]] {
			t.Errorf("sourceBonus ordering broken: %s (%d) should out-rank %s (%d)",
				order[i-1], sourceBonus[order[i-1]], order[i], sourceBonus[order[i]])
		}
	}
	// The identified-source spread must stay below the smallest resolution gap
	// (576p→480p = 20) so a better source never leapfrogs a resolution tier.
	if spread := sourceBonus[parser.SourceRemux] - sourceBonus[parser.SourceDVD]; spread >= 20 {
		t.Errorf("identified-source spread %d must stay below the smallest resolution gap (20)", spread)
	}
	// And CAM must sit far enough down that a 1080p cam can't beat a 720p Remux.
	if resBase[parser.Res1080p]+sourceBonus[parser.SourceCAM] >= resBase[parser.Res720p]+sourceBonus[parser.SourceRemux] {
		t.Error("a 1080p CAM should not out-score a 720p Remux")
	}
}

// --- Fix 2: SmallBias below 4 must not be inert -----------------------------

// A mild SmallBias (the fallback profile's 0.15) must actually nudge: the
// per-GB penalty applies and score ties break toward the smaller file.
func TestMildSmallBiasNudges(t *testing.T) {
	p := Profile{Name: "mild", SmallBias: 0.15}
	e := NewDefaultEngine()

	// Penalty: a 20 GB file loses points a 8 GB file doesn't.
	big := e.Evaluate(p, NewCandidate("Movie.2024.1080p.WEB-DL.H.264-BIG", 20, 100))
	small := e.Evaluate(p, NewCandidate("Movie.2024.1080p.WEB-DL.H.264-SML", 8, 100))
	if big.SizeScore >= 0 {
		t.Errorf("SizeScore at bias 0.15 for 20 GB = %d, want negative", big.SizeScore)
	}
	if big.Total >= small.Total {
		t.Errorf("20 GB (total %d) should score below 8 GB (total %d) at bias 0.15", big.Total, small.Total)
	}

	// Tiebreak: identical scores (int truncation eats the tiny penalty at these
	// sizes) → the smaller file wins instead of the bigger one.
	d := e.Decide(p, []Candidate{
		NewCandidate("Movie.2024.1080p.WEB-DL.H.264-LRG", 4.5, 100),
		NewCandidate("Movie.2024.1080p.WEB-DL.H.264-TDY", 4.0, 100),
	})
	if d.Winner == nil || d.Winner.Candidate.SizeGB != 4.0 {
		t.Fatalf("bias 0.15 tie should go to the smaller file, winner = %+v", d.Winner)
	}

	// SmallBias == 0 keeps today's bigger-wins tiebreak.
	d = e.Decide(Profile{Name: "plain"}, []Candidate{
		NewCandidate("Movie.2024.1080p.WEB-DL.H.264-LRG", 4.5, 100),
		NewCandidate("Movie.2024.1080p.WEB-DL.H.264-TDY", 4.0, 100),
	})
	if d.Winner == nil || d.Winner.Candidate.SizeGB != 4.5 {
		t.Fatalf("bias 0 tie should go to the bigger file, winner = %+v", d.Winner)
	}
}

// --- Fix 3: keyword matching is token-bounded like reject terms -------------

func TestKeywordMatchingIsTokenBounded(t *testing.T) {
	e := NewDefaultEngine()
	p := Profile{
		Name:     "kw",
		Keywords: []Keyword{{Term: "cam", Score: -100}, {Term: "web", Score: 50}},
	}

	// "web" must not match inside "Cobweb"; "cam" must not match "American" or "Camera".
	ev := e.Evaluate(p, NewCandidate("Cobweb.2023.1080p.BluRay.x264-GRP", 8, 100))
	if ev.FormatScore != 0 {
		t.Errorf("keyword 'web' matched inside 'Cobweb': FormatScore = %d, want 0", ev.FormatScore)
	}
	ev = e.Evaluate(p, NewCandidate("The.American.2010.Camera.Cut.1080p.BluRay.x264-GRP", 8, 100))
	if ev.FormatScore != 0 {
		t.Errorf("keyword 'cam' matched inside 'American'/'Camera': FormatScore = %d, want 0", ev.FormatScore)
	}
	// Real tokens still match.
	ev = e.Evaluate(p, NewCandidate("Movie.2024.1080p.WEB.h264-GRP", 8, 100))
	if ev.FormatScore != 50 {
		t.Errorf("keyword 'web' should match the WEB token: FormatScore = %d, want 50", ev.FormatScore)
	}

	// The stored-profile helpers use the same token-bounded matching.
	if got := KeywordScore(p.Keywords, "Cobweb.2023.1080p.BluRay"); got != 0 {
		t.Errorf("KeywordScore matched mid-word: %d, want 0", got)
	}
	if got := KeywordScore(p.Keywords, "Movie.2024.WEB.h264"); got != 50 {
		t.Errorf("KeywordScore = %d, want 50", got)
	}
	if Rejects([]string{"cam"}, "Movie.2024.Camera.Obscura.1080p") {
		t.Error("Rejects matched 'cam' inside 'Camera'")
	}
	if !Rejects([]string{"cam"}, "Movie.2024.CAM.x264") {
		t.Error("Rejects should match the real CAM token")
	}
	if Rejects([]string{"com"}, "Show.Season.1.Complete.720p") {
		t.Error("Rejects matched 'com' inside 'Complete'")
	}
}

// --- Fix 4: PROPER/REPACK strictly win ties ---------------------------------

func TestProperBeatsTheSameRelease(t *testing.T) {
	base := "Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP"
	proper := "Movie.2024.1080p.WEB-DL.DDP5.1.PROPER.H.264-GRP"
	d := NewDefaultEngine().Decide(Profile{Name: "any"}, []Candidate{
		NewCandidate(base, 8, 100),
		NewCandidate(proper, 8, 100),
	})
	if d.Winner == nil || d.Winner.Candidate.Name != proper {
		t.Fatalf("the PROPER should win over the identical non-proper, winner = %+v", d.Winner)
	}

	// But the bonus must stay below every source gap: a PROPER WEBRip still
	// loses to a plain WEB-DL of the same resolution and size.
	d = NewDefaultEngine().Decide(Profile{Name: "any"}, []Candidate{
		NewCandidate("Movie.2024.1080p.WEBRip.PROPER.H.264-GRP", 8, 100),
		NewCandidate("Movie.2024.1080p.WEB-DL.H.264-GRP", 8, 100),
	})
	if d.Winner == nil || !strings.Contains(d.Winner.Candidate.Name, "WEB-DL") {
		t.Fatalf("a PROPER must not jump a source tier, winner = %+v", d.Winner)
	}
}

// --- Service-level pins (DB-backed): PROPER upgrade + WouldReject runtime ---

// testService builds a quality Service over a throwaway SQLite database with
// the columns the repo reads/writes (mirroring the migrations).
func testService(t *testing.T) (*Service, context.Context) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/q.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE quality_profiles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		media_type TEXT NOT NULL DEFAULT 'movie',
		name TEXT NOT NULL,
		base TEXT NOT NULL DEFAULT '',
		allowed_resolutions TEXT NOT NULL DEFAULT '[]',
		min_source TEXT NOT NULL DEFAULT '',
		bitrate_cap_mbps REAL NOT NULL DEFAULT 0,
		small_bias REAL NOT NULL DEFAULT 0,
		min_format_score INTEGER NOT NULL DEFAULT 0,
		format_scores TEXT NOT NULL DEFAULT '{}',
		custom_formats TEXT NOT NULL DEFAULT '[]',
		keywords TEXT NOT NULL DEFAULT '[]',
		rejected TEXT NOT NULL DEFAULT '[]',
		min_seeders INTEGER NOT NULL DEFAULT 0,
		stall_minutes INTEGER NOT NULL DEFAULT 0,
		max_source TEXT NOT NULL DEFAULT '',
		upgrades_enabled INTEGER NOT NULL DEFAULT 1,
		upgrade_min_percent REAL NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	return NewService(db), context.Background()
}

// A PROPER of the very release we imported must be accepted as an upgrade —
// that's the whole point of a proper (the original was broken).
func TestProperIsAnUpgradeOfTheSameRelease(t *testing.T) {
	svc, ctx := testService(t)
	sp, err := svc.Create(ctx, StoredProfile{MediaType: MediaMovie, Name: "up", UpgradesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(sp.ID, 10)

	current := "Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GRP"
	proper := NewCandidate("Movie.2024.1080p.WEB-DL.DDP5.1.PROPER.H.264-GRP", 8, 50)

	got, ok := svc.UpgradeCandidate(ctx, ref, current, 8, 0, []Candidate{proper})
	if !ok || got.Name != proper.Name {
		t.Fatalf("the PROPER of the imported release should be an upgrade, got ok=%v cand=%+v", ok, got)
	}

	// Sanity: the identical non-proper release is NOT an upgrade.
	same := NewCandidate(current, 8, 50)
	if _, ok := svc.UpgradeCandidate(ctx, ref, current, 8, 0, []Candidate{same}); ok {
		t.Error("the release we already have must not count as an upgrade")
	}
}

// WouldReject must apply the profile's bitrate ceiling when the caller can
// supply the media runtime — with runtime 0 the cap stays inert (unknowable).
func TestWouldRejectAppliesBitrateCap(t *testing.T) {
	svc, ctx := testService(t)
	sp, err := svc.Create(ctx, StoredProfile{MediaType: MediaMovie, Name: "capped", BitrateCapMbps: 20})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(sp.ID, 10)

	// 20 GiB over 60 minutes ≈ 48 Mbps H.264 — way over a 20 Mbps ceiling.
	name := "Movie.2024.1080p.BluRay.x264-GRP"
	if !svc.WouldReject(ctx, ref, name, 20, 60) {
		t.Error("a ~48 Mbps file must be rejected by a 20 Mbps ceiling when the runtime is known")
	}
	// Without a runtime the size can't become a bitrate: the cap is skipped.
	if svc.WouldReject(ctx, ref, name, 20, 0) {
		t.Error("with no runtime the bitrate cap cannot apply")
	}
	// A file comfortably under the ceiling passes with runtime known.
	if svc.WouldReject(ctx, ref, name, 5, 60) {
		t.Error("a ~12 Mbps file fits under a 20 Mbps ceiling")
	}
}
