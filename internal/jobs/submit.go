package jobs

import (
	"context"
	"log/slog"

	"github.com/tristenlammi/arrmada/internal/safego"
)

// Submitter starts jobs: the Runner, or a fake in tests. Modules that start work
// (request approval, the books re-match) take one instead of the Runner itself.
type Submitter interface {
	Submit(ctx context.Context, spec Spec) (id int64, existing bool, err error)
}

// Start submits spec to sub. With no runner wired (tests, tools) the work still runs —
// on a panic-safe goroutine under ctx and the spec's timeout, with no record and no
// single-flight — and Start returns 0, false.
func Start(ctx context.Context, sub Submitter, log *slog.Logger, spec Spec) (int64, bool, error) {
	if sub != nil {
		return sub.Submit(ctx, spec)
	}
	name := spec.Kind
	if spec.Target != "" {
		name += " " + spec.Target
	}
	safego.Go(log, name, func() {
		c, cancel := ctx, context.CancelFunc(func() {})
		if spec.Timeout > 0 {
			c, cancel = context.WithTimeout(ctx, spec.Timeout)
		}
		defer cancel()
		if _, err := spec.Fn(c, nil); err != nil && log != nil {
			log.Warn(spec.Kind+" failed", "target", spec.Target, "err", err)
		}
	})
	return 0, false, nil
}
