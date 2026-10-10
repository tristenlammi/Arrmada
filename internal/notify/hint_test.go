package notify

import (
	"strings"
	"testing"
)

func TestURLHint(t *testing.T) {
	cases := []struct {
		raw     string
		want    string
		secrets []string
	}{
		{"discord://1234567890/SECRETWEBHOOKTOKEN", "discord://••••OKEN", []string{"SECRET", "1234567890"}},
		{"tgram://123456:ABCDEFsecret/987654", "tgram://••••7654", []string{"ABCDEF", "secret"}},
		{"mailtos://user:pw@smtp.example.com?to=me@example.com", "mailtos://smtp.example.com/••••", []string{"user", "pw", "to=", "me@"}},
		{"gotify://gotify.example.com/AppTokenSecret", "gotify://gotify.example.com/••••", []string{"AppToken"}},
		{"ntfy://mytopic", "ntfy://••••", []string{"mytopic"}},
		{"ntfys://ntfy.example.com/private-topic", "ntfys://ntfy.example.com/••••", []string{"private"}},
		{"json://hooks.example.com/path?token=SECRET", "json://hooks.example.com/••••", []string{"SECRET", "token", "path"}},
		{"json://user:pass@hooks.example.com:8080/x", "json://hooks.example.com:8080/••••", []string{"user", "pass"}},
		{"pover://userkey@apptokenvalue123?priority=high", "pover://••••e123", []string{"userkey", "priority"}},
		{"slack://T1/B2/C3", "slack://••••", []string{"T1", "B2", "C3"}},
		{"", "", nil},
		{"::not a url", "••••", nil},
	}
	for _, c := range cases {
		got := URLHint(c.raw)
		if got != c.want {
			t.Errorf("URLHint(%q) = %q, want %q", c.raw, got, c.want)
		}
		for _, sec := range c.secrets {
			if strings.Contains(got, sec) {
				t.Errorf("URLHint(%q) = %q leaks %q", c.raw, got, sec)
			}
		}
	}
}

func TestRedactScrubsURLAndCredentials(t *testing.T) {
	url := "mailtos://someone:hunter2@smtp.example.com/?to=boss@example.com"
	msg := "apprise: exit status 1 (Unparseable URL " + url + "; login someone/hunter2 refused by smtp.example.com)"
	got := Redact(msg, url)
	for _, sec := range []string{url, "hunter2", "someone"} {
		if strings.Contains(got, sec) {
			t.Errorf("Redact left %q in %q", sec, got)
		}
	}
	if !strings.Contains(got, "exit status 1") || !strings.Contains(got, "refused") {
		t.Errorf("Redact lost the reason: %q", got)
	}
	got = Redact("Failed to send to discord: 404 for webhook 1234567890/SECRETTOKEN", "discord://1234567890/SECRETTOKEN")
	if strings.Contains(got, "SECRETTOKEN") || strings.Contains(got, "1234567890") {
		t.Errorf("Redact left a token: %q", got)
	}
}
