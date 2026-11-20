package aio

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Prowlarr and SABnzbd ship switched off: only a clear "on" starts one. A blank value (a
// template field the owner cleared) and anything unreadable leave it off.
func TestParseOptIn(t *testing.T) {
	for _, tc := range []struct {
		v      string
		on, ok bool
	}{
		{"", false, true}, {"   ", false, true},
		{"true", true, true}, {"1", true, true}, {" Yes ", true, true}, {"on", true, true},
		{"false", false, true}, {"0", false, true}, {"off", false, true}, {"no", false, true},
		{"ture", false, false}, {"false|true", false, false},
	} {
		on, ok := parseOptIn(tc.v)
		if on != tc.on || ok != tc.ok {
			t.Errorf("parseOptIn(%q) = %v, %v; want %v, %v", tc.v, on, ok, tc.on, tc.ok)
		}
	}
}

func TestReadOptionsOptInSwitches(t *testing.T) {
	// Nothing set (an install from before these switches): both off, no note but the summary.
	o, notes := ReadOptions(envOf(nil), true)
	if o.Prowlarr || o.SABnzbd {
		t.Fatalf("an unset switch started one: %+v", o)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "bundled Prowlarr off, bundled SABnzbd off") {
		t.Errorf("notes = %q", notes)
	}
	// Cleared in the template: still off.
	if o, _ := ReadOptions(envOf(map[string]string{"ARRMADA_BUNDLE_PROWLARR": "", "ARRMADA_BUNDLE_SABNZBD": " "}), true); o.Prowlarr || o.SABnzbd {
		t.Errorf("a blank switch started one: %+v", o)
	}
	o, notes = ReadOptions(envOf(map[string]string{"ARRMADA_BUNDLE_PROWLARR": "true", "ARRMADA_BUNDLE_SABNZBD": "TRUE"}), true)
	if !o.Prowlarr || !o.SABnzbd || !o.Qbittorrent || !o.FlareSolverr {
		t.Errorf("both on: %+v", o)
	}
	if !strings.Contains(notes[len(notes)-1], "bundled Prowlarr on, bundled SABnzbd on") {
		t.Errorf("notes = %q", notes)
	}
	// A typo leaves an opt-in companion off (and says so), where it leaves an opt-out one on.
	o, notes = ReadOptions(envOf(map[string]string{"ARRMADA_BUNDLE_SABNZBD": "ture"}), true)
	if o.SABnzbd {
		t.Error("a typo started SABnzbd")
	}
	if !strings.Contains(strings.Join(notes, "\n"), `ARRMADA_BUNDLE_SABNZBD="ture" isn't true or false; leaving it off`) {
		t.Errorf("notes = %q", notes)
	}
	// The ARM build has both (only FlareSolverr is x86-64 only).
	if o, _ := ReadOptions(envOf(map[string]string{"ARRMADA_BUNDLE_PROWLARR": "true", "ARRMADA_BUNDLE_SABNZBD": "true"}), false); !o.Prowlarr || !o.SABnzbd || o.FlareSolverr {
		t.Errorf("ARM build: %+v", o)
	}
	if o.ProwlarrDir() != "/data/prowlarr" || o.SABnzbdDir() != "/data/sabnzbd" || o.SABnzbdConfig() != "/data/sabnzbd/sabnzbd.ini" {
		t.Errorf("folders %q, %q, %q", o.ProwlarrDir(), o.SABnzbdDir(), o.SABnzbdConfig())
	}
}

// What Arrmada's environment ends up as, for every combination of the two switches (and
// the template's own values): a SABnzbd that's off is never registered, even from a URL
// left in the template, while Arrmada always learns where the built-in one would be, so
// it can switch its client off; a Prowlarr address the owner set wins whether the
// built-in one is on or off; the built-in Prowlarr counts as Arrmada's own only while
// it's on.
func TestAppEnvOptInCombinations(t *testing.T) {
	const ownProwlarr = "http://192.168.1.10:9696"
	for _, prowlarr := range []bool{false, true} {
		for _, sab := range []bool{false, true} {
			for _, userURLs := range []bool{false, true} {
				name := fmt.Sprintf("prowlarr=%v sabnzbd=%v template-urls=%v", prowlarr, sab, userURLs)
				environ := []string{"PUID=99", "ARRMADA_AIO_PROWLARR_URL=http://stale:1"}
				tmpl := map[string]string{}
				if userURLs {
					environ = append(environ, "ARRMADA_PROWLARR_URL="+ownProwlarr, "ARRMADA_SABNZBD_URL=http://10.0.0.5:8080")
					tmpl["ARRMADA_PROWLARR_URL"] = ownProwlarr
					tmpl["ARRMADA_SABNZBD_URL"] = "http://10.0.0.5:8080"
				}
				o := Options{Qbittorrent: true, DataDir: "/data", Prowlarr: prowlarr, SABnzbd: sab}
				m := map[string]string{}
				for _, kv := range MergeEnv(environ, AppEnv(o, envOf(tmpl))) {
					k, v, _ := strings.Cut(kv, "=")
					m[k] = v
				}

				if m["ARRMADA_AIO_SABNZBD_URL"] != SABnzbdURL {
					t.Errorf("%s: ARRMADA_AIO_SABNZBD_URL = %q", name, m["ARRMADA_AIO_SABNZBD_URL"])
				}
				wantSAB, wantConf := "", ""
				if sab {
					wantSAB, wantConf = SABnzbdURL, "/data/sabnzbd/sabnzbd.ini"
				}
				if m["ARRMADA_SABNZBD_URL"] != wantSAB || m["ARRMADA_SABNZBD_CONFIG"] != wantConf {
					t.Errorf("%s: SABnzbd URL %q, config %q; want %q, %q", name, m["ARRMADA_SABNZBD_URL"], m["ARRMADA_SABNZBD_CONFIG"], wantSAB, wantConf)
				}

				wantProwlarr := ""
				switch {
				case userURLs:
					wantProwlarr = ownProwlarr // the template wins, on or off
				case prowlarr:
					wantProwlarr = ProwlarrURL
				}
				if m["ARRMADA_PROWLARR_URL"] != wantProwlarr {
					t.Errorf("%s: ARRMADA_PROWLARR_URL = %q, want %q", name, m["ARRMADA_PROWLARR_URL"], wantProwlarr)
				}
				wantAIO := ""
				if prowlarr {
					wantAIO = ProwlarrURL
				}
				if m["ARRMADA_AIO_PROWLARR_URL"] != wantAIO {
					t.Errorf("%s: ARRMADA_AIO_PROWLARR_URL = %q, want %q", name, m["ARRMADA_AIO_PROWLARR_URL"], wantAIO)
				}
				if m["PUID"] != "99" || m["ARRMADA_QBITTORRENT_URL"] != LocalURL {
					t.Errorf("%s: the rest of the environment changed: %v", name, m)
				}
			}
		}
	}
}

func TestPlanOptIn(t *testing.T) {
	o := Options{UID: 99, GID: 100, DataDir: "/data", Prowlarr: true, SABnzbd: true}
	environ := []string{"ARRMADA_TMDB_API_KEY=secret", "PUID=99"}
	svcs := Plan(o, environ, envOf(nil))
	byName := map[string]Service{}
	for _, s := range svcs {
		byName[s.Name] = s
	}
	if len(svcs) != 3 || !svcs[0].Main {
		t.Fatalf("services = %+v", svcs)
	}
	p, ok := byName["prowlarr"]
	if !ok || p.Path != ProwlarrBin || strings.Join(p.Args, " ") != "-nobrowser -data=/data/prowlarr" || p.Dir != "/data/prowlarr" {
		t.Errorf("prowlarr = %+v", p)
	}
	s, ok := byName["sabnzbd"]
	if !ok || s.Path != SABnzbdPython || s.Dir != "/data/sabnzbd" ||
		strings.Join(s.Args, " ") != SABnzbdApp+" --browser 0 --config-file /data/sabnzbd/sabnzbd.ini --server 0.0.0.0:8085" {
		t.Errorf("sabnzbd = %+v", s)
	}
	for _, svc := range []Service{p, s} {
		if svc.UID == nil || svc.GID == nil || *svc.UID != 99 || *svc.GID != 100 || svc.OwnUser || svc.Main {
			t.Errorf("%s doesn't run as PUID:PGID", svc.Name)
			continue
		}
		env := strings.Join(svc.Env, "\n")
		if strings.Contains(env, "secret") {
			t.Errorf("%s was given Arrmada's keys", svc.Name)
		}
		if !strings.Contains(env, "HOME="+svc.Dir) || !strings.Contains(env, "LANG=C.UTF-8") {
			t.Errorf("%s env = %q", svc.Name, env)
		}
	}
	if env := strings.Join(p.Env, "\n"); !strings.Contains(env, "PROWLARR__SERVER__PORT=9696") || !strings.Contains(env, "PROWLARR__SERVER__BINDADDRESS=*") {
		t.Errorf("prowlarr env = %q", env)
	}
	// The addresses Arrmada is given are the ports these listen on.
	if ProwlarrURL != fmt.Sprintf("http://127.0.0.1:%d", ProwlarrPort) || SABnzbdURL != fmt.Sprintf("http://127.0.0.1:%d", SABnzbdPort) {
		t.Error("a URL and its port disagree")
	}
	if SABnzbdPort == WebUIPort || ProwlarrPort == WebUIPort || SABnzbdPort == FlareSolverrPort {
		t.Error("two companions share a port inside the container")
	}
	if got := Plan(Options{DataDir: "/data", SABnzbd: true}, nil, envOf(nil)); len(got) != 2 || got[1].Name != "sabnzbd" {
		t.Errorf("SABnzbd alone = %+v", got)
	}
}

func TestSeedSABnzbd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sabnzbd")
	conf := filepath.Join(dir, "sabnzbd.ini")
	seeded, err := SeedSABnzbd(SABSeedOptions{Config: conf, Downloads: "/storage/downloads/", Hostname: "9F3A2B1C0D4E", UID: os.Getuid(), GID: os.Getgid()})
	if err != nil || !seeded {
		t.Fatalf("seed: %v, %v", seeded, err)
	}
	raw, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	ini := string(raw)
	for _, want := range []string{
		"[misc]\nlanguage = en\n",
		"username = arrmada\n",
		// DNS-rebinding protection stays on: the whitelist is set, never switched off.
		"host_whitelist = 9f3a2b1c0d4e,\n",
		// The page answers the home network and Tailscale-style addresses, nothing wider.
		"local_ranges = 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, fc00::/7, fe80::/10, 100.64.0.0/10\n",
		"download_dir = /storage/downloads/usenet/incomplete\n",
		"complete_dir = /storage/downloads/usenet/complete\n",
		"auto_browser = 0\n",
		"[[*]]\nname = *\norder = 0\npp = 3\n",
		"[[arrmada]]\nname = arrmada\norder = 1\npp = 3\nscript = Default\ndir = movies\n",
		"[[arrmada-tv]]\nname = arrmada-tv\norder = 2\npp = 3\nscript = Default\ndir = tv\n",
		"[[arrmada-books]]\nname = arrmada-books\norder = 3\npp = 3\nscript = Default\ndir = books\n",
		"[[arrmada-music]]\nname = arrmada-music\norder = 4\npp = 3\nscript = Default\ndir = music\n",
	} {
		if !strings.Contains(ini, want) {
			t.Errorf("config lacks %q:\n%s", want, ini)
		}
	}
	if strings.Contains(ini, "host_whitelist_enabled") || strings.Contains(ini, "host_whitelist = ,") {
		t.Errorf("the host whitelist is switched off or empty:\n%s", ini)
	}
	keys := map[string]string{}
	for _, k := range []string{"api_key", "nzb_key", "password"} {
		m := regexp.MustCompile(`(?m)^` + k + ` = ([0-9a-f]+)$`).FindStringSubmatch(ini)
		if m == nil {
			t.Fatalf("no %s in:\n%s", k, ini)
		}
		keys[k] = m[1]
	}
	if len(keys["api_key"]) != 32 || len(keys["nzb_key"]) != 32 || len(keys["password"]) != 36 || keys["api_key"] == keys["nzb_key"] {
		t.Errorf("keys %v", keys)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(conf); fi.Mode().Perm() != 0o600 {
			t.Errorf("config mode %v, want 0600 (it holds the API key)", fi.Mode().Perm())
		}
	}

	// A second start leaves SABnzbd's config alone (its servers, a changed login, its key).
	if err := os.WriteFile(conf, []byte("changed by SABnzbd"), 0o600); err != nil {
		t.Fatal(err)
	}
	seeded, err = SeedSABnzbd(SABSeedOptions{Config: conf, Downloads: "/elsewhere"})
	if err != nil || seeded {
		t.Fatalf("second seed: %v, %v", seeded, err)
	}
	if raw, _ := os.ReadFile(conf); string(raw) != "changed by SABnzbd" {
		t.Error("an existing config was rewritten")
	}

	// Two seeds never share a key.
	other := filepath.Join(t.TempDir(), "sabnzbd.ini")
	if _, err := SeedSABnzbd(SABSeedOptions{Config: other, Downloads: "/storage/downloads"}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(other); strings.Contains(string(raw), keys["api_key"]) {
		t.Error("a second config got the same API key")
	}
}

// Without a usable hostname the line is left out, and SABnzbd adds its own name on first
// start: the whitelist is on either way, and nothing odd ever lands in the config.
func TestSeedSABnzbdHostname(t *testing.T) {
	for _, host := range []string{"", "bad host", "evil\npassword = x", "a,b"} {
		ini := sabnzbdINI(SABSeedOptions{Downloads: "/d", Hostname: host}, "k", "n", "p")
		if strings.Contains(ini, "host_whitelist") {
			t.Errorf("hostname %q: %q", host, ini)
		}
		if strings.Count(ini, "password = ") != 1 {
			t.Errorf("hostname %q changed the config's keys:\n%s", host, ini)
		}
	}
	if ini := sabnzbdINI(SABSeedOptions{Downloads: "/d", Hostname: "Tower-Arrmada.local"}, "k", "n", "p"); !strings.Contains(ini, "host_whitelist = tower-arrmada.local,\n") {
		t.Errorf("hostname not whitelisted:\n%s", ini)
	}
}
