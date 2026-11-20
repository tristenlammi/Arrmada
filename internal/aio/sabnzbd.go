package aio

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SABSeedOptions is what the bundled SABnzbd's first config is written from.
type SABSeedOptions struct {
	Config    string // its sabnzbd.ini, in appdata (Options.SABnzbdConfig)
	Downloads string // the downloads folder; its incomplete and complete folders go under usenet/ there
	// Hostname is the container's own name, which SABnzbd would add to its host whitelist
	// by itself. Arrmada reaches it on 127.0.0.1, which needs no entry.
	Hostname string
	UID, GID int // the user SABnzbd and Arrmada run as
}

// SeedSABnzbd writes the bundled SABnzbd's first sabnzbd.ini, the same settings the
// Compose install's init container writes (docker-compose.yml, arrmada-sabnzbd-init):
// English (Arrmada reads its failure reasons), Arrmada's categories with folders of their
// own, every job repaired, unpacked and deleted after (pp = 3), a random API key and NZB
// key, and a login for its own page (user arrmada, a random password), which an admin
// sees on its card in Settings → Connections. Its downloads go under usenet/ in the
// downloads folder, on the same share as the library, so an import is a rename; Arrmada
// moves them under the Downloads folder picked in the app once it's running.
//
// Its host whitelist stays on (DNS-rebinding protection: FlareSolverr's Chromium opens
// tracker pages in the same container). Arrmada uses 127.0.0.1, an address SABnzbd always
// accepts.
//
// It does nothing to a config that already exists: SABnzbd owns it from then on, so its
// servers, a changed login and its key survive updates and a switch off and on again.
// seeded says a new config was written. The keys and the password are never logged.
func SeedSABnzbd(o SABSeedOptions) (seeded bool, err error) {
	if _, err := os.Stat(o.Config); err == nil {
		// The one change made to a config that's there: who its page answers, when that was
		// never set (admitTailnet). SABnzbd isn't running yet, so it reads what's written.
		_, err := admitTailnet(o.Config)
		return false, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	dir := filepath.Dir(o.Config)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return false, err
	}
	var keys [3]string
	for i, n := range []int{16, 16, 18} { // as the Compose seed: 32, 32 and 36 hex characters
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			return false, err
		}
		keys[i] = hex.EncodeToString(b)
	}
	if err := writeFile(o.Config, []byte(sabnzbdINI(o, keys[0], keys[1], keys[2])), 0o600, o.UID, o.GID); err != nil {
		return false, err
	}
	return true, ChownTree(dir, o.UID, o.GID)
}

// sabLocalRanges is download.SABLocalRanges, kept here so this package stays free of the
// app's own packages.
const sabLocalRanges = "10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, fc00::/7, fe80::/10, 100.64.0.0/10"

// admitTailnet gives a settings file from before local_ranges was seeded the seeded
// ranges, when it has none: no local_ranges line, or the empty one SABnzbd writes
// ("local_ranges = ,"). Ranges someone set are theirs and stay. The file is written over
// in place, so its owner and permissions don't change. changed says it was written.
func admitTailnet(path string) (changed bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(raw), "\n")
	misc, found := -1, false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "[misc]" {
			misc = i
			continue
		}
		key, val, ok := strings.Cut(t, "=")
		if !ok || strings.TrimSpace(key) != "local_ranges" {
			continue
		}
		found = true
		if strings.Trim(strings.TrimSpace(val), ",\"' ") != "" {
			return false, nil
		}
		lines[i] = "local_ranges = " + sabLocalRanges
		changed = true
	}
	if !found {
		if misc < 0 {
			return false, nil // not a settings file this knows how to read
		}
		lines = append(lines[:misc+1], append([]string{"local_ranges = " + sabLocalRanges}, lines[misc+1:]...)...)
		changed = true
	}
	if !changed {
		return false, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return false, err
	}
	if _, err := f.WriteString(strings.Join(lines, "\n")); err != nil {
		f.Close()
		return false, err
	}
	return true, f.Close()
}

// sabnzbdINI is the seeded config. The categories, their order and their settings are the
// Compose seed's; only the folders and the whitelist differ.
func sabnzbdINI(o SABSeedOptions, apiKey, nzbKey, password string) string {
	usenet := strings.TrimRight(o.Downloads, "/") + "/usenet"
	var b strings.Builder
	b.WriteString("__version__ = 19\n__encoding__ = utf-8\n[misc]\nlanguage = en\n")
	fmt.Fprintf(&b, "api_key = %s\nnzb_key = %s\n", apiKey, nzbKey)
	fmt.Fprintf(&b, "username = arrmada\npassword = %s\n", password)
	// Without a name SABnzbd adds its own on first start; the whitelist is on either way.
	if h := strings.ToLower(strings.TrimSpace(o.Hostname)); h != "" && strings.Trim(h, "abcdefghijklmnopqrstuvwxyz0123456789.-") == "" {
		fmt.Fprintf(&b, "host_whitelist = %s,\n", h)
	}
	// Who its page answers: the home-network ranges it allows by default, and the range
	// Tailscale uses (download.SABLocalRanges, which a test holds this to).
	b.WriteString("local_ranges = " + sabLocalRanges + "\n")
	fmt.Fprintf(&b, "download_dir = %s/incomplete\ncomplete_dir = %s/complete\n", usenet, usenet)
	b.WriteString("auto_browser = 0\n[categories]\n")
	b.WriteString("[[*]]\nname = *\norder = 0\npp = 3\nscript = None\ndir = \"\"\nnewzbin = \"\"\npriority = 0\n")
	for i, c := range []struct{ name, dir string }{
		{"arrmada", "movies"}, {"arrmada-tv", "tv"}, {"arrmada-books", "books"}, {"arrmada-music", "music"},
	} {
		fmt.Fprintf(&b, "[[%s]]\nname = %s\norder = %d\npp = 3\nscript = Default\ndir = %s\nnewzbin = \"\"\npriority = -100\n", c.name, c.name, i+1, c.dir)
	}
	return b.String()
}
