package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

// Requester-owned Apprise URLs: anyone signed in can save one (from outside the
// network too), and the server then posts approve/decline/ready messages to it. Left
// open, that's a blind request into the Docker network or the LAN — qBittorrent, Plex,
// the router. So for non-staff accounts only push services with a fixed public API, or
// self-hosted ones whose host resolves to a public address, are allowed; the check runs
// on save and again before every send (DNS can change, and URLs saved before this
// existed are covered).
//
// What this can't enforce: apprise resolves the host again itself (a DNS-rebinding
// window between our lookup and its own), and its HTTP client follows redirects, so a
// public server answering with a redirect to an internal address is followed. The
// content sent is fixed ("… is ready to watch") and the trigger is an event, not the
// requester, which is why that residual risk is accepted rather than proxied.

// Resolver looks up a host's addresses. net.DefaultResolver is one; tests stub it.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// blockedPrefixes are the ranges a requester's URL may never reach: loopback, private
// and unique-local, link-local (the cloud metadata address 169.254.169.254 among them),
// carrier-grade NAT, "this network", benchmarking, reserved, and the NAT64 prefix (which
// can wrap any of the above).
var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "240.0.0.0/4",
		"::/128", "::1/128", "64:ff9b::/96", "fc00::/7", "fe80::/10",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// publicAddr reports whether ip is an ordinary public unicast address.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap() // ::ffff:10.0.0.1 is 10.0.0.1
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// errInternalHost is the plain-words refusal for a host that isn't on the internet.
var errInternalHost = errors.New("that address is on your own network, which isn't allowed for your account — use a public service")

// CheckPublicHost refuses a host that is, or resolves to, anything but public addresses.
// Every address must be public: a name with one private answer is refused. A lookup that
// fails is refused too — nothing can be checked.
func CheckPublicHost(ctx context.Context, host string, res Resolver) error {
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if host == "" {
		return errors.New("the link has no server name")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !publicAddr(ip) {
			return errInternalHost
		}
		return nil
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return errInternalHost
	}
	if res == nil {
		res = net.DefaultResolver
	}
	addrs, err := res.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("couldn't look up %s — check the server name", host)
	}
	for _, a := range addrs {
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok || !publicAddr(ip) {
			return errInternalHost
		}
	}
	return nil
}

// fixedHostSchemes post to the service's own public API whatever the URL says, so where
// they go needs no checking. hostedSchemes name a server in the URL, which must be public.
var (
	fixedHostSchemes = map[string]bool{
		"discord": true, "telegram": true, "tgram": true, "slack": true,
		"pover": true, "pushover": true, "pbul": true, "pushbullet": true,
	}
	hostedSchemes = map[string]bool{
		"ntfy": true, "ntfys": true, "gotify": true, "gotifys": true,
		"matrix": true, "matrixs": true, "mailto": true, "mailtos": true,
	}
	// ntfyTopic is what a bare ntfy://<topic> (a topic on ntfy.sh) looks like.
	ntfyTopic = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ErrSchemeNotAllowed is the refusal for a kind of link only staff may use.
var ErrSchemeNotAllowed = errors.New("this kind of link isn't allowed for your account — use a Discord, Telegram, ntfy, Pushover, Pushbullet, Slack, Gotify, Matrix or email link")

// ValidateUserAppriseURL checks a personal Apprise URL. Staff get the ordinary
// ValidateAppriseURL rules; everyone else is held to push services that can't be
// pointed at the local network (see the top of this file).
func ValidateUserAppriseURL(ctx context.Context, raw string, staff bool, res Resolver) error {
	if err := ValidateAppriseURL(raw); err != nil {
		return err
	}
	if staff {
		return nil
	}
	raw = strings.TrimSpace(raw)
	scheme, _, _ := strings.Cut(raw, "://")
	scheme = strings.ToLower(scheme)
	if fixedHostSchemes[scheme] {
		return nil
	}
	if !hostedSchemes[scheme] {
		return ErrSchemeNotAllowed
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("that link couldn't be read — check it and try again")
	}
	// apprise reads its options from the query. Refuse anything that could re-point
	// the message: a different SMTP server, ntfy's mode switch, ';'-joined options this
	// parser and apprise's might read differently.
	if strings.Contains(u.RawQuery, ";") {
		return errors.New("that link has options that aren't allowed for your account")
	}
	for key := range u.Query() {
		switch strings.ToLower(key) {
		case "smtp", "mode":
			return fmt.Errorf("the %q option isn't allowed for your account", key)
		}
	}
	host := u.Hostname()
	if (scheme == "ntfy" || scheme == "ntfys") && strings.Trim(u.Path, "/") == "" {
		// ntfy://mytopic is a topic on the public ntfy.sh: nothing to resolve, as long as
		// it really is just a topic name (no port, no credentials, no dots).
		if u.User != nil || u.Port() != "" || !ntfyTopic.MatchString(host) {
			return errors.New("an ntfy link is ntfy://topic, or ntfys://your-server/topic")
		}
		return nil
	}
	return CheckPublicHost(ctx, host, res)
}
