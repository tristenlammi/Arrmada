package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// composeService is what these tests read from one service of a compose file.
type composeService struct {
	image         string
	containerName string
	env           map[string]string
	volumes       []string
	// rawVolumes are the volume lines as written, before any ${VAR:-default} is resolved.
	rawVolumes []string
	ports      []string
	// optional: the service is under a compose profile (Prowlarr, SABnzbd), started only
	// when asked for.
	optional bool
}

// interpDefault matches a ${VAR:-default} with a plain default.
var interpDefault = regexp.MustCompile(`\$\{[A-Z0-9_]+:-([^${}]*)\}`)

// withDefaults is s as docker-compose.yml reads with nothing in .env.
func withDefaults(s string) string { return interpDefault.ReplaceAllString(s, "$1") }

// readCompose reads the services of a compose file this repository writes. It is not a
// YAML parser: it knows the two-space layout both files use, `x-name: &anchor` maps
// merged with `<<: *anchor` (or used whole as `environment: *anchor`), and `&name` /
// `*name` on a volume line. Enough to compare the two files' essentials without a
// YAML dependency in the module.
func readCompose(t *testing.T, path string) map[string]*composeService {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	unquote := func(s string) string {
		s = strings.TrimSpace(s)
		if i := strings.Index(s, " #"); i >= 0 && !strings.HasPrefix(s, `"`) {
			s = strings.TrimSpace(s[:i])
		}
		if strings.HasPrefix(s, `"`) {
			if j := strings.LastIndex(s, `"`); j > 0 {
				s = s[1:j]
			}
		}
		return s
	}
	anchors := map[string]map[string]string{} // x- maps
	scalars := map[string]string{}            // &name on a list item
	services := map[string]*composeService{}
	var topKey, svc, section, anchor string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case indent == 0:
			topKey, svc, section, anchor = strings.SplitN(trimmed, ":", 2)[0], "", "", ""
			if strings.HasPrefix(topKey, "x-") {
				if i := strings.Index(trimmed, "&"); i >= 0 {
					anchor = strings.TrimSpace(trimmed[i+1:])
					anchors[anchor] = map[string]string{}
				}
			}
		case topKey == "services" && indent == 2:
			svc, section = strings.TrimSuffix(trimmed, ":"), ""
			services[svc] = &composeService{env: map[string]string{}}
		case anchor != "" && indent == 2:
			k, v, _ := strings.Cut(trimmed, ":")
			anchors[anchor][k] = withDefaults(unquote(v))
		case svc != "" && indent == 4:
			k, v, _ := strings.Cut(trimmed, ":")
			v = strings.TrimSpace(v)
			section = k
			s := services[svc]
			switch k {
			case "image":
				s.image = unquote(v)
			case "container_name":
				s.containerName = unquote(v)
			case "profiles":
				s.optional = true
			case "environment":
				if strings.HasPrefix(v, "*") {
					for ek, ev := range anchors[v[1:]] {
						s.env[ek] = ev
					}
				}
			}
		case svc != "" && indent == 6:
			s := services[svc]
			switch section {
			case "environment":
				k, v, _ := strings.Cut(trimmed, ":")
				if k == "<<" {
					for ek, ev := range anchors[strings.TrimPrefix(strings.TrimSpace(v), "*")] {
						s.env[ek] = ev
					}
					continue
				}
				s.env[k] = withDefaults(unquote(v))
			case "volumes", "ports":
				item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				if strings.HasPrefix(item, "&") {
					name, rest, _ := strings.Cut(item[1:], " ")
					scalars[name] = unquote(rest)
					item = rest
				} else if strings.HasPrefix(item, "*") {
					item = scalars[item[1:]]
				}
				raw := unquote(item)
				item = withDefaults(raw)
				if section == "volumes" {
					s.volumes = append(s.volumes, item)
					s.rawVolumes = append(s.rawVolumes, raw)
				} else {
					s.ports = append(s.ports, item)
				}
			}
		}
	}
	return services
}

// readText is a file's text with LF line ends.
func readText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// serviceScript is the `- |` shell script in a service's command, its lines unindented.
// commented reads it from a commented-out service ("  # name:"), as the example has it.
func serviceScript(text, name string, commented bool) []string {
	prefix := func(s string) (string, bool) {
		if !commented {
			return s, true
		}
		t := strings.TrimLeft(s, " ")
		if !strings.HasPrefix(t, "#") {
			return "", false
		}
		return "  " + strings.TrimPrefix(strings.TrimPrefix(t, "#"), " "), true
	}
	var out []string
	in, script, indent := false, false, 0
	for _, line := range strings.Split(text, "\n") {
		l, ok := prefix(line)
		if !ok {
			if in {
				break
			}
			continue
		}
		if !in {
			in = strings.TrimSpace(l) == name+":" && strings.HasPrefix(l, "  "+name)
			continue
		}
		if !script {
			if strings.TrimSpace(l) == "- |" {
				script, indent = true, -1
			}
			continue
		}
		cur := len(l) - len(strings.TrimLeft(l, " "))
		if indent < 0 {
			indent = cur
		}
		if strings.TrimSpace(l) != "" && cur < indent {
			break
		}
		out = append(out, strings.TrimPrefix(l, strings.Repeat(" ", indent)))
	}
	return out
}

// TestComposeExampleSABnzbdInit: the example's commented-out SABnzbd init writes the same
// first settings as docker-compose.yml's — the same key generation, folders, categories and
// pp=3 — so uncommenting it gives the same SABnzbd an install.sh --with-sabnzbd does.
func TestComposeExampleSABnzbdInit(t *testing.T) {
	root := repoRoot(t)
	main := serviceScript(readText(t, filepath.Join(root, "docker-compose.yml")), "arrmada-sabnzbd-init", false)
	ex := serviceScript(readText(t, filepath.Join(root, "docker-compose.example.yml")), "arrmada-sabnzbd-init", true)
	if len(main) < 10 {
		t.Fatalf("docker-compose.yml's arrmada-sabnzbd-init script not found (%d lines)", len(main))
	}
	if strings.Join(main, "\n") != strings.Join(ex, "\n") {
		t.Errorf("the example's arrmada-sabnzbd-init script differs from docker-compose.yml's:\n--- docker-compose.yml\n%s\n--- example\n%s",
			strings.Join(main, "\n"), strings.Join(ex, "\n"))
	}
	joined := strings.Join(main, "\n")
	for _, want := range []string{"api_key = $$key", "language = en", "host_whitelist = arrmada-sabnzbd,", "local_ranges = 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, fc00::/7, fe80::/10, 100.64.0.0/10", "pp = 3", "[[arrmada-tv]]", "[[arrmada-books]]", "[[arrmada-music]]", "[[arrmada]]"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the SABnzbd init script has no %q", want)
		}
	}
}

// TestQbittorrentSeedsAgree: docker-compose.yml copies docker/qbittorrent/qBittorrent.conf,
// the example writes it inline; both give the same web UI settings. CSRF protection and the
// localhost login are on from the start. The subnet whitelist is the Docker network's own
// subnet, worked out the same way in both (docker/qbittorrent/subnets.sh), never the private
// ranges that covered every home network: download.SecureBundledQbit recognises that subnet
// and replaces it with a password on Arrmada's first start.
func TestQbittorrentSeedsAgree(t *testing.T) {
	root := repoRoot(t)
	webUI := func(lines []string) []string {
		var out []string
		for _, l := range lines {
			if l = strings.TrimSpace(l); strings.HasPrefix(l, `WebUI\`) {
				out = append(out, l)
			}
		}
		return out
	}
	file := webUI(strings.Split(strings.ReplaceAll(readText(t, filepath.Join(root, "docker", "qbittorrent", "qBittorrent.conf")), "\r\n", "\n"), "\n"))
	inline := webUI(serviceScript(readText(t, filepath.Join(root, "docker-compose.example.yml")), "arrmada-qbittorrent-init", false))
	if strings.Join(file, "\n") != strings.Join(inline, "\n") {
		t.Errorf("the qBittorrent seeds differ:\n--- docker/qbittorrent/qBittorrent.conf\n%s\n--- docker-compose.example.yml\n%s",
			strings.Join(file, "\n"), strings.Join(inline, "\n"))
	}
	for _, want := range []string{`WebUI\LocalHostAuth=true`, `WebUI\CSRFProtection=true`} {
		if !slices.Contains(file, want) {
			t.Errorf("the qBittorrent seed has no %s", want)
		}
	}
	exScript := strings.ReplaceAll(strings.Join(serviceScript(readText(t, filepath.Join(root, "docker-compose.example.yml")), "arrmada-qbittorrent-init", false), "\n"), "$$", "$")
	for name, text := range map[string]string{
		"docker/qbittorrent/qBittorrent.conf": readText(t, filepath.Join(root, "docker", "qbittorrent", "qBittorrent.conf")),
		"docker/qbittorrent/init.sh":          readText(t, filepath.Join(root, "docker", "qbittorrent", "init.sh")),
		"the example's init":                  exScript,
	} {
		for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
			if strings.Contains(l, "192.168.0.0/16") && !strings.HasPrefix(strings.TrimSpace(l), "#") {
				t.Errorf("%s still whitelists every home network: %s", name, l)
			}
		}
	}
	subnets := strings.TrimSpace(readText(t, filepath.Join(root, "docker", "qbittorrent", "subnets.sh")))
	pipeline := subnets[strings.LastIndex(subnets, "\n")+1:]
	if !strings.HasPrefix(pipeline, "ip -4 route") || !strings.Contains(exScript, pipeline) {
		t.Errorf("the example works out the Docker network differently from subnets.sh:\n--- subnets.sh\n%s\n--- example\n%s", pipeline, exScript)
	}
	if !strings.Contains(readText(t, filepath.Join(root, "docker", "qbittorrent", "init.sh")), "sh /seed/subnets.sh") ||
		!strings.Contains(readText(t, filepath.Join(root, "docker-compose.yml")), `command: ["sh", "/seed/init.sh"]`) {
		t.Error("docker-compose.yml's qBittorrent init doesn't seed through init.sh and subnets.sh")
	}
}

// mountTarget is the path inside the container a volume line mounts at.
func mountTarget(v string) string {
	parts := strings.Split(v, ":")
	if len(parts) >= 2 {
		return parts[1]
	}
	return parts[0]
}

// TestComposeExampleMatchesCompose: docker-compose.example.yml is the copy-paste install
// in the README, for people with neither the installer nor a checkout. It has to start
// the same app the repository's docker-compose.yml does — same published image, the
// same companions at the same versions, the same settings with the same defaults — or
// a change made to one quietly breaks the other.
func TestComposeExampleMatchesCompose(t *testing.T) {
	root := repoRoot(t)
	main := readCompose(t, filepath.Join(root, "docker-compose.yml"))
	ex := readCompose(t, filepath.Join(root, "docker-compose.example.yml"))

	const published = "ghcr.io/tristenlammi/arrmada:stable"
	if !strings.HasSuffix(main["arrmada-app"].image, published+"}") {
		t.Errorf("docker-compose.yml's app image %q no longer defaults to %s", main["arrmada-app"].image, published)
	}
	if got := ex["arrmada-app"].image; got != published {
		t.Errorf("docker-compose.example.yml runs %q, want %s", got, published)
	}

	// Every service the app needs to work out of the box. The optional ones (a compose
	// profile: Prowlarr, SABnzbd) are commented out in the example, with the same image and
	// container name.
	exText := readText(t, filepath.Join(root, "docker-compose.example.yml"))
	for name, m := range main {
		if m.optional {
			for _, want := range []string{"# " + name + ":", "#   image: " + m.image, "#   container_name: " + m.containerName} {
				if !strings.Contains(exText, want) {
					t.Errorf("%s is optional in docker-compose.yml; the example should carry it commented out with %q", name, want)
				}
			}
			continue
		}
		e, ok := ex[name]
		if !ok {
			t.Errorf("docker-compose.example.yml has no %s service", name)
			continue
		}
		if e.containerName != m.containerName {
			t.Errorf("%s: container_name %q in the example, %q in docker-compose.yml", name, e.containerName, m.containerName)
		}
		if name != "arrmada-app" && e.image != m.image {
			t.Errorf("%s: image %q in the example, %q in docker-compose.yml", name, e.image, m.image)
		}
	}

	// The app's settings: the example sets the same ones to docker-compose.yml's defaults,
	// except the ones that are API keys (entered in the app), legacy fallbacks, starting
	// library folders (picked in the setup), last-resort switches, the update channel
	// (update.sh's; the example runs the stable release, which the app knows by itself), and
	// the host port and address of qBittorrent's own page (the example publishes none, so its card shows
	// the login without a link).
	notInExample := map[string]bool{
		"ARRMADA_TMDB_API_KEY": true, "ARRMADA_OMDB_API_KEY": true, "ARRMADA_HARDCOVER_API_KEY": true,
		"ARRMADA_PLEX_URL": true, "ARRMADA_PLEX_TOKEN": true,
		"ARRMADA_LIBRARY_DIR": true, "ARRMADA_DOWNLOADS_DIR": true, "ARRMADA_MOVIES_DIR": true,
		"ARRMADA_TV_DIR": true, "ARRMADA_EBOOKS_DIR": true, "ARRMADA_AUDIOBOOKS_DIR": true, "ARRMADA_MUSIC_DIR": true,
		"ARRMADA_SKIP_MIGRATION_SNAPSHOT": true, "ARRMADA_ALLOW_NEWER_SCHEMA": true,
		"ARRMADA_CHANNEL": true, "ARRMADA_QBIT_WEBUI_HOST_PORT": true, "ARRMADA_QBIT_WEBUI_HOST_BIND": true,
	}
	for _, svc := range []string{"arrmada-app", "arrmada-qbittorrent"} {
		for k, mv := range main[svc].env {
			ev, ok := ex[svc].env[k]
			switch {
			case notInExample[k]:
				if ok {
					t.Errorf("%s: the example sets %s, which belongs in the app (or nowhere)", svc, k)
				}
			case !ok:
				t.Errorf("%s: docker-compose.yml sets %s but the example doesn't", svc, k)
			case ev != mv:
				t.Errorf("%s: %s is %q in the example, %q (default) in docker-compose.yml", svc, k, ev, mv)
			}
		}
		for k := range ex[svc].env {
			if _, ok := main[svc].env[k]; !ok {
				t.Errorf("%s: the example sets %s, which docker-compose.yml doesn't", svc, k)
			}
		}
	}

	// Mounts: the database at /data and nothing else there (media at /data once
	// clobbered a database), the media folder at /storage in both the app and
	// qBittorrent (one mount, so imports hardlink), and the shared managed /media.
	for _, svc := range []string{"arrmada-app", "arrmada-qbittorrent"} {
		targets := map[string]string{}
		for _, v := range ex[svc].volumes {
			targets[mountTarget(v)] = v
		}
		if targets["/storage"] == "" || targets["/media"] != "arrmada-media:/media" {
			t.Errorf("%s: the example should mount the media folder at /storage and arrmada-media at /media; has %v", svc, ex[svc].volumes)
		}
		for _, mv := range main[svc].volumes {
			if _, ok := targets[mountTarget(mv)]; !ok {
				t.Errorf("%s: docker-compose.yml mounts %s, the example doesn't", svc, mountTarget(mv))
			}
		}
	}
	if d := ex["arrmada-app"].volumes; len(d) == 0 || mountTarget(d[0]) != "/data" || strings.Contains(d[0], "media") {
		t.Errorf("arrmada-app: the example's first mount should be its own data folder at /data, got %v", d)
	}
	// The example is also a Portainer stack, which has no folder of its own: a relative
	// host path there resolves inside Portainer's data volume, hidden and lost with the
	// stack. Every host folder has to be a full path (named volumes are fine).
	for name, s := range ex {
		for _, v := range s.volumes {
			src, _, _ := strings.Cut(v, ":")
			if strings.HasPrefix(src, ".") || strings.HasPrefix(src, "~") {
				t.Errorf("%s: the example mounts %q, a relative path; use a full one", name, v)
			}
		}
	}
	var appStorage string
	for _, v := range ex["arrmada-app"].volumes {
		if mountTarget(v) == "/storage" {
			appStorage = v
		}
	}
	for _, v := range ex["arrmada-qbittorrent"].volumes {
		if mountTarget(v) == "/storage" && v != appStorage {
			t.Errorf("qBittorrent mounts %q at /storage, the app %q: they must be the same folder", v, appStorage)
		}
	}

	// The same ports inside the containers (the host side is the user's to change).
	for _, svc := range []string{"arrmada-app", "arrmada-qbittorrent"} {
		inside := func(ps []string) map[string]bool {
			out := map[string]bool{}
			for _, p := range ps {
				out[p[strings.LastIndex(p, ":")+1:]] = true
			}
			return out
		}
		m, e := inside(main[svc].ports), inside(ex[svc].ports)
		for p := range m {
			if p == "8080" && svc == "arrmada-qbittorrent" {
				continue // qBittorrent's own web page: optional in the example
			}
			if !e[p] {
				t.Errorf("%s: docker-compose.yml publishes %s, the example doesn't", svc, p)
			}
		}
	}
}

// mediaLine is the one way every service mounts the shared media: the managed volume, or
// the folder ARRMADA_MEDIA_HOST names in .env.
const mediaLine = "${ARRMADA_MEDIA_HOST:-arrmada-media}:/media"

// TestMediaMountsShareOneLine: everything that shares /media (the app, qBittorrent, SABnzbd
// and the init that makes its folders) mounts it from the same line in both compose files,
// so setting ARRMADA_MEDIA_HOST moves all of them together and a file's path stays the same
// in each (imports hardlink). With nothing in .env it's still the arrmada-media volume, as
// it always was. ARRMADA_MEDIA_HOST never mounts anywhere but /media (never /data, the
// database).
func TestMediaMountsShareOneLine(t *testing.T) {
	root := repoRoot(t)
	check := func(file string, want []string) {
		t.Helper()
		got := []string{}
		for name, s := range readCompose(t, filepath.Join(root, file)) {
			for _, v := range s.rawVolumes {
				if strings.Contains(v, "ARRMADA_MEDIA_HOST") && v != mediaLine {
					t.Errorf("%s: %s mounts %q; ARRMADA_MEDIA_HOST goes only in %q", file, name, v, mediaLine)
				}
				if mountTarget(withDefaults(v)) != "/media" {
					continue
				}
				if v != mediaLine {
					t.Errorf("%s: %s mounts %q at /media, want %q", file, name, v, mediaLine)
				}
				got = append(got, name)
			}
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: services mounting /media are %v, want %v", file, got, want)
		}
	}
	check("docker-compose.yml", []string{"arrmada-app", "arrmada-media-init", "arrmada-qbittorrent", "arrmada-sabnzbd"})
	check("docker-compose.example.yml", []string{"arrmada-app", "arrmada-media-init", "arrmada-qbittorrent"})
	// The example's SABnzbd is commented out; uncommented it has to mount the same line.
	if ex := readText(t, filepath.Join(root, "docker-compose.example.yml")); !strings.Contains(ex, `#     - "`+mediaLine+`"`) {
		t.Errorf("the example's commented-out SABnzbd should mount %q", mediaLine)
	}
	if !strings.Contains(readText(t, filepath.Join(root, "docker-compose.yml")), "\n  arrmada-media:\n") {
		t.Error("docker-compose.yml no longer declares the arrmada-media volume, the default for /media")
	}
}

// TestComposeExampleKeepsItsMounts: the example's folders come from .env now, but with
// nothing set they are exactly the ones the file always had. A Portainer stack deployed
// from the repository can't edit the file, so it has run on those; it must keep the same
// database and media on its next redeploy, not start over somewhere else.
func TestComposeExampleKeepsItsMounts(t *testing.T) {
	ex := readCompose(t, filepath.Join(repoRoot(t), "docker-compose.example.yml"))
	for svc, want := range map[string][]string{
		"arrmada-app":         {"/path/to/arrmada/data:/data", "/path/to/your/media:/storage", "arrmada-media:/media"},
		"arrmada-qbittorrent": {"/path/to/your/media:/storage", "arrmada-media:/media"},
	} {
		for _, w := range want {
			if !slices.Contains(ex[svc].volumes, w) {
				t.Errorf("%s: with nothing in .env the example should mount %s; it has %v", svc, w, ex[svc].volumes)
			}
		}
	}
	for svc, want := range map[string]map[string]string{
		"arrmada-app":         {"PUID": "1000", "PGID": "1000", "TZ": "Etc/UTC", "ARRMADA_AUDIOBOOK_HOST_PORT": "13379", "ARRMADA_QBIT_PORT": "6881"},
		"arrmada-qbittorrent": {"PUID": "1000", "PGID": "1000", "TZ": "Etc/UTC"},
	} {
		for k, v := range want {
			if got := ex[svc].env[k]; got != v {
				t.Errorf("%s: %s is %q with nothing in .env, want %q (what the example always had)", svc, k, got, v)
			}
		}
	}
	for svc, want := range map[string][]string{
		"arrmada-app":         {"7878:7878", "13379:13378"},
		"arrmada-qbittorrent": {"6881:6881", "6881:6881/udp"},
	} {
		for _, w := range want {
			if !slices.Contains(ex[svc].ports, w) {
				t.Errorf("%s: with nothing in .env the example should publish %s; it has %v", svc, w, ex[svc].ports)
			}
		}
	}
}

// TestMediaInitScriptsAgree: both compose files make the media folders with the same
// script. In the managed volume (the default) it hands all of /media to the app's user as it
// always did; in a folder of the user's (ARRMADA_MEDIA_HOST) it changes the owner of nothing
// it didn't make, and refuses a folder that overlaps the database's.
func TestMediaInitScriptsAgree(t *testing.T) {
	root := repoRoot(t)
	main := serviceScript(readText(t, filepath.Join(root, "docker-compose.yml")), "arrmada-media-init", false)
	ex := serviceScript(readText(t, filepath.Join(root, "docker-compose.example.yml")), "arrmada-media-init", false)
	if len(main) < 10 {
		t.Fatalf("docker-compose.yml's arrmada-media-init script not found (%d lines)", len(main))
	}
	if strings.Join(main, "\n") != strings.Join(ex, "\n") {
		t.Errorf("the example's arrmada-media-init script differs from docker-compose.yml's:\n--- docker-compose.yml\n%s\n--- example\n%s",
			strings.Join(main, "\n"), strings.Join(ex, "\n"))
	}
	joined := strings.Join(main, "\n")
	// Which branch runs comes from the mount table, never from the variable: a path written
	// into the compose file, an NFS volume or a Windows path is still a folder of the user's.
	if !strings.Contains(joined, "/proc/self/mountinfo") || !strings.Contains(joined, "/volumes/[^/]+/_data$$") {
		t.Errorf("the media init should tell the managed volume from a folder by the mount table:\n%s", joined)
	}
	// The managed volume's branch is the block that exits before the host-folder part.
	const managedStart = "if printf '%s\\n' \"$$mr\" | grep -Eq"
	before, rest, ok := strings.Cut(joined, managedStart)
	if !ok {
		t.Fatalf("the media init script has no managed-volume branch:\n%s", joined)
	}
	managed, hostPart, ok := strings.Cut(rest, "\nfi\n")
	if !ok {
		t.Fatalf("the media init script's managed branch has no end:\n%s", joined)
	}
	if !strings.Contains(managed, `chown -R "$$PUID:$$PGID" /media`) || !strings.Contains(managed, "exit") {
		t.Errorf("the managed volume should still be handed over whole (chown -R), then exit:\n%s", managed)
	}
	if strings.Contains(before, "chown") || strings.Contains(hostPart, "chown -R") {
		t.Errorf("the media init chowns a folder of the user's recursively:\n%s", joined)
	}
	// Refusals come first, so neither branch runs on a relative value or an overlap with the
	// database's folder (mounted read-only to compare by device and folder).
	for _, want := range []string{`. | ./* | .. | ../*)`, `$$5 == "/data"`, "overlap=1", "exit 1"} {
		if !strings.Contains(before, want) {
			t.Errorf("the media init's checks before either branch have no %q:\n%s", want, before)
		}
	}
	// The managed volume starts with a folder per library (the app no longer makes them: on a
	// host folder that isn't mounted yet it would make them on the wrong disk); a folder of
	// the user's never gets any, the libraries there are picked in the app.
	for _, lib := range []string{"movies", "tvshows", "ebooks", "audiobooks", "music"} {
		if !strings.Contains(managed, "/media/library/"+lib) {
			t.Errorf("the managed volume's init should make /media/library/%s:\n%s", lib, managed)
		}
		if strings.Contains(hostPart, "/media/library/"+lib) {
			t.Errorf("the media init makes /media/library/%s in a folder of the user's:\n%s", lib, hostPart)
		}
	}
	for _, want := range []string{"/media/downloads/incomplete", "/media/library"} {
		if !strings.Contains(hostPart, want) {
			t.Errorf("the host-folder part of the media init has no %q:\n%s", want, hostPart)
		}
	}
	// Both files mount the database's folder into the init read-only, the same one the app has.
	for file, want := range map[string]string{
		"docker-compose.yml":         "${ARRMADA_DATA_HOST:-arrmada-data}:/data:ro",
		"docker-compose.example.yml": "${ARRMADA_DATA_HOST:-/path/to/arrmada/data}:/data:ro",
	} {
		if !slices.Contains(readCompose(t, filepath.Join(root, file))["arrmada-media-init"].rawVolumes, want) {
			t.Errorf("%s: arrmada-media-init should mount %s", file, want)
		}
	}
}
