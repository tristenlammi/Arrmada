package download

import (
	"errors"
	"net"
)

// notSentError marks a failure that happened before a request reached the client, so the
// client certainly didn't act on it.
type notSentError struct{ err error }

func (e *notSentError) Error() string { return e.err.Error() }
func (e *notSentError) Unwrap() error { return e.err }

// NeverReached reports whether err means the client never received the request: it
// couldn't be connected to (refused, no route, DNS failure, a dial timeout), or its login
// failed before the request was sent. Only then is it safe to give the same torrent to
// another client. A timeout or a dropped connection after the request went out is not
// this — the client may well have added the torrent — and neither is any HTTP answer.
func NeverReached(err error) bool {
	var ns *notSentError
	return errors.As(err, &ns)
}

// notSent wraps err as NeverReached.
func notSent(err error) error {
	if err == nil {
		return nil
	}
	return &notSentError{err: err}
}

// dialFailed reports whether a transport error happened while connecting, before any of
// the request was written.
func dialFailed(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && (opErr.Op == "dial" || opErr.Op == "proxyconnect")
}
