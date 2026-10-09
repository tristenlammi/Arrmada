package books

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/jobs"
)

// recSub records submitted jobs; existing makes it answer that one is already running.
type recSub struct {
	specs    []jobs.Spec
	existing bool
}

func (r *recSub) Submit(_ context.Context, s jobs.Spec) (int64, bool, error) {
	if r.existing {
		return 41, true, nil
	}
	r.specs = append(r.specs, s)
	return int64(len(r.specs)), false, nil
}

// The re-match is a books.upgrade job. The claim is taken before the job starts, so the
// Books page's status read straight after the click already says it's running, and a
// second click while it runs starts nothing.
func TestSubmitUpgradeIsAJobWithTheSameStatus(t *testing.T) {
	s, _, ctx, _, _ := upgradeFixture(t)
	rec := &recSub{}
	id, started, err := s.SubmitUpgrade(ctx, rec, "user:1")
	if err != nil || !started || id != 1 {
		t.Fatalf("SubmitUpgrade = %d %v %v", id, started, err)
	}
	if !s.UpgradeStatus().Running {
		t.Fatal("status doesn't say running once submitted")
	}
	sp := rec.specs[0]
	if sp.Kind != "books.upgrade" || sp.Target != "all" || sp.Trigger != "user:1" {
		t.Fatalf("spec = %+v", sp)
	}
	if _, again, _ := s.SubmitUpgrade(ctx, rec, "user:1"); again || len(rec.specs) != 1 {
		t.Fatal("a second click started another re-match")
	}
	res, err := sp.Fn(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ur, ok := res.(UpgradeResult)
	if !ok || ur.Flagged != 1 {
		t.Fatalf("result = %#v", res)
	}
	if st := s.UpgradeStatus(); st.Running || st.Flagged != 1 || st.EndedAt == 0 {
		t.Fatalf("status after the run = %+v", st)
	}

	// A runner that already has one gives the claim straight back.
	if _, started, _ := s.SubmitUpgrade(ctx, &recSub{existing: true}, "system"); started || s.UpgradeStatus().Running {
		t.Fatal("an existing job left the claim held")
	}
}
