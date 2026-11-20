package aio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A settings file from before local_ranges was seeded gets the seeded ranges; one where
// someone set their own, or that this can't read, is left exactly as it is.
func TestAdmitTailnet(t *testing.T) {
	want := "local_ranges = " + sabLocalRanges
	cases := []struct {
		name, in string
		changed  bool
		check    func(t *testing.T, out string)
	}{
		{"the empty list SABnzbd writes", "[misc]\nlanguage = en\nlocal_ranges = ,\ninet_exposure = 0\n[servers]\n", true, func(t *testing.T, out string) {
			if out != "[misc]\nlanguage = en\n"+want+"\ninet_exposure = 0\n[servers]\n" {
				t.Errorf("got:\n%s", out)
			}
		}},
		{"an empty value", "[misc]\nlocal_ranges =\n", true, func(t *testing.T, out string) {
			if out != "[misc]\n"+want+"\n" {
				t.Errorf("got:\n%s", out)
			}
		}},
		{"no line at all (never started)", "__version__ = 19\n[misc]\nlanguage = en\n[categories]\n", true, func(t *testing.T, out string) {
			if out != "__version__ = 19\n[misc]\n"+want+"\nlanguage = en\n[categories]\n" {
				t.Errorf("got:\n%s", out)
			}
		}},
		{"the owner's own ranges", "[misc]\nlocal_ranges = 192.168.1.0/24,\n", false, nil},
		{"the owner's old-style ranges", "[misc]\nlocal_ranges = 192.168.1., 10.\n", false, nil},
		{"already seeded", "[misc]\n" + want + "\n", false, nil},
		{"not a settings file", "hello\n", false, nil},
	}
	for _, c := range cases {
		path := filepath.Join(t.TempDir(), "sabnzbd.ini")
		if err := os.WriteFile(path, []byte(c.in), 0o600); err != nil {
			t.Fatal(err)
		}
		changed, err := admitTailnet(path)
		if err != nil || changed != c.changed {
			t.Errorf("%s: changed=%v err=%v, want changed=%v", c.name, changed, err, c.changed)
			continue
		}
		raw, _ := os.ReadFile(path)
		if !c.changed && string(raw) != c.in {
			t.Errorf("%s: the file was changed:\n%s", c.name, raw)
		}
		if c.check != nil {
			c.check(t, string(raw))
		}
		// A second run never changes it again.
		if again, err := admitTailnet(path); err != nil || again {
			t.Errorf("%s: a second run changed=%v err=%v", c.name, again, err)
		}
	}
	// What the seed writes needs nothing more.
	if ini := sabnzbdINI(SABSeedOptions{Downloads: "/d"}, "k", "n", "p"); !strings.Contains(ini, "\n"+want+"\n") {
		t.Errorf("the seed has no %q", want)
	}
}

// Seeding leaves an existing config alone except for that one line.
func TestSeedSABnzbdAdmitsTailnetOnAnExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sabnzbd", "sabnzbd.ini")
	_ = os.MkdirAll(filepath.Dir(path), 0o750)
	before := "[misc]\napi_key = keep-me\nusername = mine\nlocal_ranges = ,\n[servers]\n[[news.example]]\nhost = news.example\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	seeded, err := SeedSABnzbd(SABSeedOptions{Config: path, Downloads: "/d", UID: os.Getuid(), GID: os.Getgid()})
	if err != nil || seeded {
		t.Fatalf("seeded=%v err=%v", seeded, err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != strings.Replace(before, "local_ranges = ,", "local_ranges = "+sabLocalRanges, 1) {
		t.Errorf("got:\n%s", raw)
	}
}
