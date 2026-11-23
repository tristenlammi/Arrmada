// Package jellyfinsvc is Arrmada's Jellyfin connection: the saved address and API key, the
// server's identity, how it has been answering, a library index (TMDB/TVDB/IMDb ids and
// folders → item) for Watch links and "is it in Jellyfin yet?", and the adapter that lets
// plexscan send Jellyfin partial scans. It's the Jellyfin side of mediaserver.Server;
// internal/jellyfin is the bare HTTP client underneath.
package jellyfinsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/jellyfin"
	"github.com/tristenlammi/arrmada/internal/netsafe"
	"github.com/tristenlammi/arrmada/internal/plexscan"
)

// Settings keys. The API key is not one of them: it's the hidden apikeys entry "jellyfin"
// (apikeys.Jellyfin), with ARRMADA_JELLYFIN_API_KEY as the install-time fallback.
const (
	KeyURL        = "jellyfin_url"
	KeyPublicURL  = "jellyfin_public_url" // "" = the connection URL
	KeyServerID   = "jellyfin_server_id"
	KeyServerName = "jellyfin_server_name"
	KeyVersion    = "jellyfin_server_version"
	KeyDeviceID   = "jellyfin_device_id" // made once; Jellyfin files Arrmada's sessions under it
	// KeyServerKind is which server this connection is: "jellyfin" (or unset) or "emby".
	// Emby, the server Jellyfin grew out of, speaks nearly the same API, so it uses this
	// same connection, index, scanner and sign-in; only the client's dialect changes.
	KeyServerKind = "jellyfin_server_kind"
	// Partial scans after changes (the plexscan.Scanner built for Jellyfin keeps these).
	KeyScanOnImport = "jellyfin_scan_on_import"
	KeyPathMap      = "jellyfin_path_map"
	// Watch monitoring (recording plays): read here, used by Insights.
	KeyMonitoring  = "jellyfin_monitoring_enabled"
	KeyPollSeconds = "jellyfin_poll_seconds"
)

// ScanKeys are the scanner's settings keys.
var ScanKeys = plexscan.Keys{Enabled: KeyScanOnImport, PathMap: KeyPathMap}

// DefaultPollSeconds is how often Insights asks Jellyfin what's playing unless set.
const DefaultPollSeconds = 10

// Settings is where the connection lives (*settings.Service is one).
type Settings interface {
	Get(ctx context.Context, key, def string) string
	Set(ctx context.Context, key, value string) error
	GetBool(ctx context.Context, key string, def bool) bool
	SetBool(ctx context.Context, key string, value bool) error
}

// Keys holds the API key (*apikeys.Store is one).
type Keys interface {
	Value(ctx context.Context, id string) string
	Set(ctx context.Context, id, value string) error
}

// Options wires a Service.
type Options struct {
	Settings Settings
	Keys     Keys
	// Version is Arrmada's build, sent to Jellyfin with every call.
	Version string
	// HTTP is the client every call goes through (nil = the jellyfin package's default).
	HTTP *http.Client
	// Roots is Arrmada's library folder per plexscan kind (KindMovie, KindShow), read
	// live, and PathMaps the saved Arrmada → Jellyfin path mappings: together they
	// translate a title's folder to Jellyfin's spelling for the path fallback.
	Roots    func(ctx context.Context) map[string]string
	PathMaps func(ctx context.Context) []plexscan.PathMap
	Log      *slog.Logger
}

// Service is the Jellyfin connection.
type Service struct {
	settings Settings
	keys     Keys
	version  string
	hc       *http.Client
	roots    func(ctx context.Context) map[string]string
	pathMaps func(ctx context.Context) []plexscan.PathMap
	log      *slog.Logger
	// extraRoots is the extra Movies and TV folders per kind (SetExtraRoots), read live.
	extraRoots func(ctx context.Context) map[string][]string

	deviceMu sync.Mutex
	health   pollHealth
	idx      indexSchedule
	// polling reports whether something (Insights' poller) talks to Jellyfin every few
	// seconds and records it; until then the health check probes on its own.
	pollingMu sync.RWMutex
	polling   func(ctx context.Context) bool
}

// New builds the service. Run its index worker with RunIndex.
func New(o Options) *Service {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		settings: o.Settings, keys: o.Keys, version: o.Version, hc: o.HTTP,
		roots: o.Roots, pathMaps: o.PathMaps, log: log,
	}
	s.idx.build = s.buildIndex
	s.health.describe = func(err error) string { return s.describe(context.Background(), err) }
	return s
}

// ServerKind is the connected server's kind: jellyfin.ServerJellyfin (the default) or
// jellyfin.ServerEmby.
func (s *Service) ServerKind(ctx context.Context) string {
	return jellyfin.NormalizeServerKind(s.settings.Get(ctx, KeyServerKind, ""))
}

// IsEmby reports whether the connection is to an Emby server.
func (s *Service) IsEmby(ctx context.Context) bool { return s.ServerKind(ctx) == jellyfin.ServerEmby }

// Label is the connected server's product name for people: "Jellyfin" or "Emby".
func (s *Service) Label(ctx context.Context) string { return jellyfin.ServerLabel(s.ServerKind(ctx)) }

// SetPathMaps sets where the path mappings come from (the Jellyfin scanner, built after
// the service).
func (s *Service) SetPathMaps(fn func(ctx context.Context) []plexscan.PathMap) { s.pathMaps = fn }

// SetExtraRoots tells the Watch-link lookup about the extra Movies and TV folders, per
// plexscan kind, read live. Call it at startup.
func (s *Service) SetExtraRoots(fn func(ctx context.Context) map[string][]string) { s.extraRoots = fn }

// SetPolling tells the service whether a poller is recording Jellyfin's answers (Insights,
// once it watches Jellyfin): the health check then stops probing on its own.
func (s *Service) SetPolling(fn func(ctx context.Context) bool) {
	s.pollingMu.Lock()
	s.polling = fn
	s.pollingMu.Unlock()
}

// URL is the saved connection address ("" = none), without a trailing slash.
func (s *Service) URL(ctx context.Context) string {
	return strings.TrimRight(strings.TrimSpace(s.settings.Get(ctx, KeyURL, "")), "/")
}

// PublicURL is where people open Jellyfin from: the saved public address, else the
// connection URL.
func (s *Service) PublicURL(ctx context.Context) string {
	if p := strings.TrimRight(strings.TrimSpace(s.settings.Get(ctx, KeyPublicURL, "")), "/"); p != "" {
		return p
	}
	return s.URL(ctx)
}

func (s *Service) apiKey(ctx context.Context) string {
	if s.keys == nil {
		return ""
	}
	return s.keys.Value(ctx, apikeys.Jellyfin)
}

// Configured reports whether an address and an API key are saved.
func (s *Service) Configured(ctx context.Context) bool {
	return s.URL(ctx) != "" && s.apiKey(ctx) != ""
}

// MonitoringEnabled is the "record what's played on Jellyfin" switch.
func (s *Service) MonitoringEnabled(ctx context.Context) bool {
	return s.settings.GetBool(ctx, KeyMonitoring, false)
}

// PollSeconds is how often a monitoring poller asks what's playing (2–60 s).
func (s *Service) PollSeconds(ctx context.Context) int {
	n, err := strconv.Atoi(s.settings.Get(ctx, KeyPollSeconds, ""))
	if err != nil || n <= 0 {
		return DefaultPollSeconds
	}
	return min(max(n, 2), 60)
}

// DeviceID is Arrmada's stable device id towards Jellyfin, made on first use. Jellyfin
// files Arrmada's sessions (and a signed-in user's token) under it.
func (s *Service) DeviceID(ctx context.Context) string {
	s.deviceMu.Lock()
	defer s.deviceMu.Unlock()
	if id := s.settings.Get(ctx, KeyDeviceID, ""); id != "" {
		return id
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := "arrmada-" + hex.EncodeToString(b)
	if err := s.settings.Set(ctx, KeyDeviceID, id); err != nil {
		s.log.Warn("jellyfin: couldn't save the device id", "err", err)
	}
	return id
}

// Client is a client for the saved server (its calls fail with ErrNotConfigured when
// nothing is saved).
func (s *Service) Client(ctx context.Context) *jellyfin.Client {
	return s.clientFor(ctx, s.ServerKind(ctx), s.URL(ctx), s.apiKey(ctx))
}

func (s *Service) clientFor(ctx context.Context, kind, base, key string) *jellyfin.Client {
	return jellyfin.NewFor(kind, base, key, s.DeviceID(ctx), s.version, s.hc)
}

// Config is GET /api/v1/jellyfin: the saved connection and what the server last said about
// itself. The key is only key_set.
type Config struct {
	URL               string `json:"url"`
	PublicURL         string `json:"public_url"` // "" = the connection URL
	KeySet            bool   `json:"key_set"`
	ServerName        string `json:"server_name"`
	ServerID          string `json:"server_id"`
	Version           string `json:"version"`
	MonitoringEnabled bool   `json:"monitoring_enabled"`
	PollSeconds       int    `json:"poll_seconds"`
}

// Config reads the connection.
func (s *Service) Config(ctx context.Context) Config {
	return Config{
		URL:               s.URL(ctx),
		PublicURL:         strings.TrimRight(strings.TrimSpace(s.settings.Get(ctx, KeyPublicURL, "")), "/"),
		KeySet:            s.apiKey(ctx) != "",
		ServerName:        s.settings.Get(ctx, KeyServerName, ""),
		ServerID:          s.settings.Get(ctx, KeyServerID, ""),
		Version:           s.settings.Get(ctx, KeyVersion, ""),
		MonitoringEnabled: s.MonitoringEnabled(ctx),
		PollSeconds:       s.PollSeconds(ctx),
	}
}

// ErrInvalid is input Save refuses (the API answers 400 with its message). errors.Is
// matches it; the message itself is only the plain sentence for the person.
var ErrInvalid = errors.New("invalid Jellyfin connection")

type inputError string

func (e inputError) Error() string        { return string(e) }
func (e inputError) Is(target error) bool { return target == ErrInvalid }

// ErrKeyForNewAddress is Save's refusal for a changed address with the key left blank
// (it is ErrInvalid too, so the API answers 400 with it). The same words as Prowlarr's and
// the indexers' refusal (httpapi errKeyForNewAddress), so every connection form reads alike.
var ErrKeyForNewAddress error = inputError("Enter the API key again for the new address — the saved key is only sent to the address it was saved for.")

// NormalizeURL checks an address is http(s)://host[:port][/path] and returns it without a
// trailing slash, a query or a fragment.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", inputError(fmt.Sprintf("%q isn't a web address like http://192.168.1.10:8096", raw))
	}
	if u.User != nil {
		return "", inputError("Leave the user name and password out of the address.")
	}
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

// Save stores the address, the public address and (when apiKey is non-empty) a new key.
// A blank public address, or one equal to the connection URL, follows the URL. Changing
// the server or the key forgets what the old one said and rebuilds the index; the new
// identity is read straight away, best effort — a server that doesn't answer yet doesn't
// stop the save.
//
// A blank key keeps the saved one, but only for the address it was saved for: a new
// address with no key typed is ErrKeyForNewAddress, and nothing is saved or sent.
// Otherwise anyone who could save could point the connection at their own server and
// collect the key (the identity check sends it there at once). An install-time key with
// no address saved belongs to no address yet, so the first one needs it typed too.
func (s *Service) Save(ctx context.Context, rawURL, rawPublic, apiKey string) error {
	return s.SaveFor(ctx, "", rawURL, rawPublic, apiKey)
}

// SaveFor is Save that also says which server it is: kind is "jellyfin" or "emby", or ""
// to keep the saved kind. A changed kind counts as a new server (its identity is read
// again and the index rebuilt), but needs no new key: the key goes to the same address.
func (s *Service) SaveFor(ctx context.Context, kind, rawURL, rawPublic, apiKey string) error {
	if strings.TrimSpace(kind) == "" {
		kind = s.ServerKind(ctx)
	} else if k := strings.ToLower(strings.TrimSpace(kind)); k != jellyfin.ServerJellyfin && k != jellyfin.ServerEmby {
		return inputError("Choose Jellyfin or Emby.")
	}
	kind = jellyfin.NormalizeServerKind(kind)
	label := jellyfin.ServerLabel(kind)
	base, err := NormalizeURL(rawURL)
	if err != nil {
		return err
	}
	public := ""
	if strings.TrimSpace(rawPublic) != "" {
		if public, err = NormalizeURL(rawPublic); err != nil {
			return inputError("Public address: " + err.Error())
		}
		if public == base {
			public = ""
		}
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" && s.apiKey(ctx) == "" {
		return inputError("Add the API key made in " + label + " (" + keyMenu(kind) + ").")
	}
	if apiKey == "" && !netsafe.SameEndpoint(base, s.URL(ctx)) {
		return ErrKeyForNewAddress
	}
	changed := base != s.URL(ctx) || (apiKey != "" && apiKey != s.apiKey(ctx)) || kind != s.ServerKind(ctx)
	if kind != s.ServerKind(ctx) {
		// An install that never chose keeps the setting unset (it reads as Jellyfin).
		if err := s.settings.Set(ctx, KeyServerKind, kind); err != nil {
			return err
		}
	}
	if apiKey != "" {
		if s.keys == nil {
			return errors.New("the key store isn't available")
		}
		if err := s.keys.Set(ctx, apikeys.Jellyfin, apiKey); err != nil {
			return err
		}
	}
	for k, v := range map[string]string{KeyURL: base, KeyPublicURL: public} {
		if err := s.settings.Set(ctx, k, v); err != nil {
			return err
		}
	}
	if changed {
		for _, k := range []string{KeyServerID, KeyServerName, KeyVersion} {
			_ = s.settings.Set(ctx, k, "")
		}
		s.health.reset()
		s.idx.drop()
		ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, _ = s.RefreshIdentity(ictx)
		cancel()
	}
	s.idx.nudge()
	return nil
}

// keyClearer is a key store that can forget a key (*apikeys.Store is one). Disconnect
// needs it; a store without it (a test fake) keeps the key, and the cleared address is
// enough to leave Jellyfin unconfigured.
type keyClearer interface {
	Clear(ctx context.Context, id string) error
}

// Disconnect forgets the server: its address, public address, API key and what it said
// about itself, and drops the library index. How Arrmada used it (sign-in switches,
// monitoring, path mappings) stays, so connecting it again picks up where it was; with no
// address saved none of it does anything meanwhile. Linked accounts keep their Jellyfin
// link too. An install-time ARRMADA_JELLYFIN_API_KEY still stands behind the cleared key,
// which is harmless without an address: Save sends it to a new one only once it's typed.
func (s *Service) Disconnect(ctx context.Context) error {
	if kc, ok := s.keys.(keyClearer); ok {
		if err := kc.Clear(ctx, apikeys.Jellyfin); err != nil {
			return err
		}
	}
	for _, k := range []string{KeyURL, KeyPublicURL, KeyServerID, KeyServerName, KeyVersion} {
		if err := s.settings.Set(ctx, k, ""); err != nil {
			return err
		}
	}
	s.health.reset()
	s.idx.drop()
	return nil
}

// TestResult is POST /api/v1/jellyfin/test. Detail is a plain sentence, never the key.
type TestResult struct {
	OK         bool   `json:"ok"`
	Detail     string `json:"detail"`
	ServerName string `json:"server_name,omitempty"`
	Version    string `json:"version,omitempty"`
}

// Test checks an address and key without saving them. Blank values mean the saved ones,
// but the saved key is only ever sent to the saved address: with a different address and
// no key typed, only Jellyfin's public answer is checked. A passing test of the saved
// connection also refreshes the stored identity.
func (s *Service) Test(ctx context.Context, rawURL, apiKey string) TestResult {
	return s.TestFor(ctx, "", rawURL, apiKey)
}

// TestFor is Test for a server kind ("jellyfin" or "emby"; "" = the saved kind). Choosing
// another kind than the saved one tests what's typed as a new connection, so the saved
// connection's health isn't touched.
func (s *Service) TestFor(ctx context.Context, kind, rawURL, apiKey string) TestResult {
	savedKind := s.ServerKind(ctx)
	if strings.TrimSpace(kind) == "" {
		kind = savedKind
	}
	kind = jellyfin.NormalizeServerKind(kind)
	label := jellyfin.ServerLabel(kind)
	saved := s.URL(ctx)
	base := saved
	if strings.TrimSpace(rawURL) != "" {
		var err error
		if base, err = NormalizeURL(rawURL); err != nil {
			return TestResult{Detail: err.Error()}
		}
	}
	if base == "" {
		return TestResult{Detail: "Enter the " + label + " server's address first."}
	}
	key := strings.TrimSpace(apiKey)
	usingSaved := key == "" && netsafe.SameEndpoint(base, saved)
	if usingSaved {
		key = s.apiKey(ctx)
	}
	if usingSaved && saved != "" && kind == savedKind {
		// The saved connection: the answer is its health and its identity.
		si, err := s.RefreshIdentity(ctx)
		if err != nil {
			return TestResult{Detail: DescribeFor(kind, err)}
		}
		return TestResult{OK: true, Detail: "Connected", ServerName: si.ServerName, Version: si.Version}
	}
	c := s.clientFor(ctx, kind, base, "")
	pub, err := c.PublicSystemInfo(ctx)
	if err != nil {
		if errors.Is(err, jellyfin.ErrNotFound) || errors.Is(err, jellyfin.ErrUnauthorized) {
			return TestResult{Detail: "Something answered at this address, but it isn't " + article(label) + " server."}
		}
		return TestResult{Detail: DescribeFor(kind, err)}
	}
	if kind == jellyfin.ServerEmby && strings.Contains(strings.ToLower(pub.ProductName), "jellyfin") {
		// Emby never sends a product name; Jellyfin says "Jellyfin Server".
		return TestResult{Detail: "This is a Jellyfin server, not Emby: choose Jellyfin instead.", ServerName: pub.ServerName, Version: pub.Version}
	}
	if key == "" {
		return TestResult{
			Detail:     label + " answers at this address. Paste the API key to test it too (the saved key is only sent to the saved address).",
			ServerName: pub.ServerName, Version: pub.Version,
		}
	}
	si, err := s.clientFor(ctx, kind, base, key).SystemInfo(ctx)
	if err != nil {
		return TestResult{Detail: DescribeFor(kind, err), ServerName: pub.ServerName, Version: pub.Version}
	}
	return TestResult{OK: true, Detail: "Connected", ServerName: si.ServerName, Version: si.Version}
}

func article(label string) string {
	if label == "Emby" {
		return "an Emby"
	}
	return "a " + label
}

// keyMenu is where each server makes API keys.
func keyMenu(kind string) string {
	if jellyfin.NormalizeServerKind(kind) == jellyfin.ServerEmby {
		return "Dashboard → Advanced → API Keys"
	}
	return "Dashboard → API Keys"
}

// describe is DescribeFor the saved server kind.
func (s *Service) describe(ctx context.Context, err error) string {
	return DescribeFor(s.ServerKind(ctx), err)
}

// DescribeFor is Describe naming the right server: Jellyfin's words for Jellyfin, the same
// sentences about Emby (and where Emby makes API keys) for Emby.
func DescribeFor(kind string, err error) string {
	if jellyfin.NormalizeServerKind(kind) != jellyfin.ServerEmby {
		return Describe(err)
	}
	switch {
	case err == nil:
		return ""
	case errors.Is(err, jellyfin.ErrNotConfigured):
		return "Emby isn't connected: add its address and API key."
	case errors.Is(err, jellyfin.ErrUnauthorized):
		return "Emby rejected the API key. Make a new one in Emby (" + keyMenu(kind) + ") and paste it here."
	case errors.Is(err, jellyfin.ErrForbidden):
		return "Emby refused: the key isn't an administrator's API key."
	case errors.Is(err, jellyfin.ErrUnsupported):
		return "Emby doesn't offer that."
	}
	return strings.Replace(err.Error(), "jellyfin ", "emby ", 1)
}

// Describe puts a client error in plain words for a person. It never holds the key.
func Describe(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, jellyfin.ErrNotConfigured):
		return "Jellyfin isn't connected: add its address and API key."
	case errors.Is(err, jellyfin.ErrUnauthorized):
		return "Jellyfin rejected the API key. Make a new one in Jellyfin (Dashboard → API Keys) and paste it here."
	case errors.Is(err, jellyfin.ErrForbidden):
		return "Jellyfin refused: the key isn't an administrator's API key."
	}
	return err.Error()
}

// RefreshIdentity asks the saved server who it is (System/Info, which also proves the key)
// and keeps its id, name and version. The answer is recorded in the health record either
// way.
func (s *Service) RefreshIdentity(ctx context.Context) (jellyfin.SystemInfo, error) {
	if !s.Configured(ctx) {
		return jellyfin.SystemInfo{}, jellyfin.ErrNotConfigured
	}
	si, err := s.Client(ctx).SystemInfo(ctx)
	if ctx.Err() != nil && err != nil {
		return si, err // our own deadline, not Jellyfin's fault: don't record it
	}
	s.observe(err)
	if err != nil {
		return si, err
	}
	for k, v := range map[string]string{KeyServerID: si.ID, KeyServerName: si.ServerName, KeyVersion: si.Version} {
		if s.settings.Get(ctx, k, "") != v {
			_ = s.settings.Set(ctx, k, v)
		}
	}
	return si, nil
}

// Probe is RefreshIdentity for the health check and the connection card's Test.
func (s *Service) Probe(ctx context.Context) error {
	_, err := s.RefreshIdentity(ctx)
	return err
}

// observe records one exchange with Jellyfin and logs the changes between working and
// failing once.
func (s *Service) observe(err error) {
	failing, recovered := s.health.record(err, time.Now())
	switch {
	case failing:
		s.log.Warn("jellyfin: Jellyfin isn't answering", "err", err)
	case recovered:
		s.log.Info("jellyfin: Jellyfin is answering again")
	case err != nil:
		s.log.Debug("jellyfin: call failed", "err", err)
	}
}

// Record notes one exchange with Jellyfin made elsewhere (Insights' poller), so the health
// panel and the connection card see it.
func (s *Service) Record(err error) { s.observe(err) }
