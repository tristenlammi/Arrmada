package notify

import (
	"strings"
	"testing"
)

// The "--" separator must sit between the options and the URLs so a stored URL can
// never be parsed as an apprise CLI option.
func TestAppriseArgsSeparatorBeforeURLs(t *testing.T) {
	args := appriseArgs("Title", "Body", []string{"discord://web/token", "-config=/etc/passwd"})
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep == -1 {
		t.Fatalf("no -- separator in args: %v", args)
	}
	rest := args[sep+1:]
	if len(rest) != 2 || rest[0] != "discord://web/token" || rest[1] != "-config=/etc/passwd" {
		t.Errorf("URLs must all follow the -- separator, got %v", args)
	}
	for _, a := range args[:sep] {
		if strings.HasPrefix(a, "discord://") {
			t.Errorf("URL appeared before the -- separator: %v", args)
		}
	}
}

func TestValidateAppriseURL(t *testing.T) {
	cases := []struct {
		raw string
		ok  bool
	}{
		{"discord://webhook_id/webhook_token", true},
		{"tgram://bottoken/ChatID", true},
		{"mailto://user:pass@gmail.com", true},
		{"ntfys://ntfy.example.com/topic", true},
		{"json://internal-host/path", true}, // allowed, documented SSRF surface
		{"  discord://id/token  ", true},    // surrounding whitespace is trimmed
		{"", false},
		{"   ", false},
		{"-discord://id/token", false}, // leading dash could read as a CLI option
		{"--config=/etc/apprise.yml", false},
		{"gopher://old", false}, // not an apprise scheme
		{"file:///etc/passwd", false},
		{"http://example.com/hook", false}, // raw http is not an apprise notification scheme
		{"no-scheme-at-all", false},
		{"://missing", false},
	}
	for _, c := range cases {
		err := ValidateAppriseURL(c.raw)
		if c.ok && err != nil {
			t.Errorf("ValidateAppriseURL(%q) = %v, want ok", c.raw, err)
		}
		if !c.ok && err == nil {
			t.Errorf("ValidateAppriseURL(%q) accepted, want error", c.raw)
		}
	}
}
