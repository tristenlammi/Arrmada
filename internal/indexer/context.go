package indexer

import "context"

type interactiveKey struct{}

// WithInteractive marks ctx as a person's own search: a release modal, a Search button,
// a quality-profile test. Such searches still ask an indexer that background work is
// leaving alone after repeated failures — that is how a fixed indexer gets noticed, and
// one success clears its backoff.
func WithInteractive(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveKey{}, true)
}

// IsInteractive reports whether ctx was marked by WithInteractive.
func IsInteractive(ctx context.Context) bool {
	v, _ := ctx.Value(interactiveKey{}).(bool)
	return v
}
