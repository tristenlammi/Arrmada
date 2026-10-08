package download

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Only one real torrent hash may reach the client. "all" and "a|b" are qBittorrent's
// own syntax for "every torrent" and "several", which is how a delete wiped the client.
func TestValidHash(t *testing.T) {
	v1 := strings.Repeat("ab12", 10)
	v2 := strings.Repeat("CD34", 16)
	for h, want := range map[string]bool{
		v1:                            true,
		v2:                            true,
		strings.ToUpper(v1):           true,
		"all":                         false,
		"":                            false,
		"a|b":                         false,
		v1 + "|" + v1:                 false,
		v1[:39]:                       false,
		v1 + "0":                      false,
		strings.Repeat("g", 40):       false,
		strings.Repeat("a", 39) + "%": false,
	} {
		if got := ValidHash(h); got != want {
			t.Errorf("ValidHash(%q) = %v, want %v", h, got, want)
		}
	}
}

// Service.Remove refuses a bad hash before it looks at any client, so even an internal
// caller can't send "all" through.
func TestRemoveRefusesInvalidHash(t *testing.T) {
	s := &Service{} // no repo: reaching the client list would panic
	for _, h := range []string{"all", "", "a|b"} {
		if err := s.Remove(context.Background(), h, true); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("Remove(%q) err = %v, want ErrInvalidHash", h, err)
		}
	}
}
