package notify

import (
	"context"
	"errors"
	"net"
	"testing"
)

// stubResolver answers from a fixed table; anything else fails to resolve.
type stubResolver map[string][]string

func (r stubResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips, ok := r[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	var out []net.IPAddr
	for _, s := range ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out, nil
}

var testResolver = stubResolver{
	"internal.lan":     {"192.168.1.5"},
	"push.example.com": {"93.184.216.34"},
	"mixed.example":    {"93.184.216.34", "10.0.0.7"},
	"v6.example":       {"2606:2800:220:1:248:1893:25c8:1946"},
	"v6local.example":  {"fd00::1"},
	"mapped.example":   {"::ffff:10.0.0.1"},
	"gmail.com":        {"142.250.72.5"},
	"qbittorrent":      {"172.18.0.4"},
}

func TestValidateUserAppriseURL(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		raw   string
		staff bool
		ok    bool
	}{
		// Fixed-host services are fine for everyone.
		{raw: "discord://123/token", ok: true},
		{raw: "tgram://123456789:AAHdq/987", ok: true},
		{raw: "pover://user@token", ok: true},
		{raw: "ntfy://mytopic", ok: true},
		// Self-hosted services: the host must be public.
		{raw: "ntfys://push.example.com/topic", ok: true},
		{raw: "gotify://push.example.com/token", ok: true},
		{raw: "gotifys://v6.example/token", ok: true},
		{raw: "mailtos://me:pw@gmail.com", ok: true},
		{raw: "gotify://192.168.1.10/token", ok: false},
		{raw: "gotify://internal.lan/token", ok: false},
		{raw: "ntfys://qbittorrent/topic", ok: false},
		{raw: "ntfy://127.0.0.1:8080/topic", ok: false},
		{raw: "ntfy://localhost/topic", ok: false},
		{raw: "matrixs://u:p@169.254.169.254/#room", ok: false},
		{raw: "gotify://[::1]/token", ok: false},
		{raw: "gotify://100.64.1.1/token", ok: false},
		{raw: "gotify://mixed.example/token", ok: false}, // one private answer is enough
		{raw: "gotify://v6local.example/token", ok: false},
		{raw: "gotify://mapped.example/token", ok: false},
		{raw: "gotify://nowhere.example/token", ok: false}, // doesn't resolve
		// Options that could re-point the message.
		{raw: "mailtos://me:pw@gmail.com?smtp=10.0.0.5", ok: false},
		{raw: "mailtos://me:pw@gmail.com?SMTP=10.0.0.5", ok: false},
		{raw: "ntfy://qbittorrent?mode=private", ok: false},
		{raw: "ntfy://topic.with.dots", ok: false},
		{raw: "ntfy://topic:8080", ok: false},
		{raw: "mailtos://me:pw@gmail.com?to=a;smtp=10.0.0.5", ok: false},
		// Generic and relay schemes are staff-only.
		{raw: "json://10.0.0.5/hook", ok: false},
		{raw: "jsons://push.example.com/hook", ok: false},
		{raw: "form://push.example.com/hook", ok: false},
		{raw: "webhook://push.example.com", ok: false},
		{raw: "apprise://push.example.com/token", ok: false},
		{raw: "hassio://internal.lan/token", ok: false},
		{raw: "signal://internal.lan/+1555", ok: false},
		{raw: "twilio://sid:token@+1555/+1666", ok: false},
		// Staff keep the ordinary rules.
		{raw: "json://10.0.0.5/hook", staff: true, ok: true},
		{raw: "gotify://internal.lan/token", staff: true, ok: true},
		{raw: "-config", staff: true, ok: false},
		{raw: "-config", ok: false},
	}
	for _, c := range cases {
		err := ValidateUserAppriseURL(ctx, c.raw, c.staff, testResolver)
		if c.ok && err != nil {
			t.Errorf("staff=%v %q: %v, want ok", c.staff, c.raw, err)
		}
		if !c.ok && err == nil {
			t.Errorf("staff=%v %q accepted, want refused", c.staff, c.raw)
		}
	}
	if err := ValidateUserAppriseURL(ctx, "json://10.0.0.5/hook", false, testResolver); !errors.Is(err, ErrSchemeNotAllowed) {
		t.Errorf("json:// for a requester: %v, want ErrSchemeNotAllowed", err)
	}
}

func TestCheckPublicHost(t *testing.T) {
	ctx := context.Background()
	for host, ok := range map[string]bool{
		"93.184.216.34": true, "push.example.com": true, "push.example.com.": true,
		"10.1.2.3": false, "172.31.0.1": false, "192.168.0.1": false, "127.0.0.2": false,
		"169.254.169.254": false, "0.0.0.0": false, "255.255.255.255": false, "224.0.0.1": false,
		"::": false, "fe80::1": false, "64:ff9b::a00:1": false, "": false, "LOCALHOST": false,
		"internal.lan": false,
	} {
		err := CheckPublicHost(ctx, host, testResolver)
		if ok != (err == nil) {
			t.Errorf("CheckPublicHost(%q) = %v, want ok=%v", host, err, ok)
		}
	}
}
