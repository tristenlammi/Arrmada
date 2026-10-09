package scheduler

import (
	"context"
	"errors"
	"testing"
)

// Failures in a row count up and a success resets them; the health panel's
// failing-tasks check reads this.
func TestConsecutiveFailures(t *testing.T) {
	s := New(quietLogger())
	fail := true
	s.Register("flaky", 1<<40, false, func(context.Context) error {
		if fail {
			return errors.New("indexer down")
		}
		return nil
	})
	task := s.tasks[0]
	ctx := context.Background()
	consecutive := func() uint64 { return s.Tasks()[0].ConsecutiveFailures }

	s.exec(ctx, task)
	s.exec(ctx, task)
	if got := consecutive(); got != 2 {
		t.Fatalf("after two failures: %d", got)
	}
	fail = false
	s.exec(ctx, task)
	if got := consecutive(); got != 0 {
		t.Fatalf("after a success: %d", got)
	}
	if got := s.Tasks()[0].Failures; got != 2 {
		t.Errorf("total failures %d, want 2", got)
	}
}
