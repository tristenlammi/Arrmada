// Package audioserver serves Arrmada's audiobooks to listening apps over the
// Audiobookshelf API, so apps built for Audiobookshelf (Lissen first) work unchanged.
// It runs on its own port — nothing of Arrmada's own interface is reachable there —
// and translates requests into the listening package, which owns progress and sync.
package audioserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tristenlammi/arrmada/internal/applog"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/listening"
	"github.com/tristenlammi/arrmada/internal/netutil"
	"github.com/tristenlammi/arrmada/internal/settings"
)

// Settings keys.
const (
	KeyEnabled   = "audioserver_enabled"
	KeyPublicURL = "audioserver_public_url"
	KeyDenied    = "audioserver_denied_users" // JSON list of user ids not allowed to connect
)

// Server is the Audiobookshelf-compatible API.
type Server struct {
	db       *sql.DB
	books    *books.Service
	listen   *listening.Store
	users    *auth.Service
	settings *settings.Service
	log      *slog.Logger
	probe    *prober
	images   *imageCache
	coverDir string
	Accounts *Accounts
	limiter  *loginLimiter
	catalog  catalogCache
	warming  atomic.Bool // stops overlapping Warm runs (the schedule, switching on, an import)

	traceUntil atomic.Int64     // unix ms; see trace.go
	now        func() time.Time // the clock (tests set it); nil means time.Now
}

// Options configure a Server.
type Options struct {
	DB       *sql.DB
	Books    *books.Service
	Listen   *listening.Store
	Users    *auth.Service
	Settings *settings.Service
	Log      *slog.Logger
	FFprobe  string
	DataDir  string
}

// New builds the server.
func New(o Options) *Server {
	s := &Server{
		db: o.DB, books: o.Books, listen: o.Listen, users: o.Users, settings: o.Settings, log: o.Log,
		probe:    &prober{db: o.DB, ffprobe: o.FFprobe},
		images:   newImageCache(filepath.Join(o.DataDir, "audioserver", "images")),
		coverDir: filepath.Join(o.DataDir, "covers"),
		limiter:  newLoginLimiter(),
	}
	s.Accounts = newAccounts(o.DB, o.Users, s.Allowed)
	s.loadTrace(context.Background())
	return s
}

// Allowed reports whether a user may use the audiobook server: requester or above, not
// disabled, and not switched off by an admin.
func (s *Server) Allowed(ctx context.Context, u *auth.User) bool {
	if u == nil || u.Disabled || !u.Role.AtLeast(auth.RoleRequester) {
		return false
	}
	for _, id := range s.DeniedUsers(ctx) {
		if id == u.ID {
			return false
		}
	}
	return true
}

// DeniedUsers lists users an admin has switched off.
func (s *Server) DeniedUsers(ctx context.Context) []int64 {
	var ids []int64
	_ = json.Unmarshal([]byte(s.settings.Get(ctx, KeyDenied, "[]")), &ids)
	return ids
}

// SetAllowed switches one user on or off.
func (s *Server) SetAllowed(ctx context.Context, userID int64, allowed bool) error {
	ids := s.DeniedUsers(ctx)
	out := ids[:0]
	for _, id := range ids {
		if id != userID {
			out = append(out, id)
		}
	}
	if !allowed {
		out = append(out, userID)
	}
	b, _ := json.Marshal(out)
	return s.settings.Set(ctx, KeyDenied, string(b))
}

// Listen exposes the listening store (for Arrmada's own pages).
func (s *Server) Listen() *listening.Store { return s.listen }

type ctxKey int

const (
	userKey ctxKey = iota
	familyKey
	tokenKey
)

func userOf(r *http.Request) *auth.User {
	u, _ := r.Context().Value(userKey).(*auth.User)
	return u
}

func familyOf(r *http.Request) string {
	f, _ := r.Context().Value(familyKey).(string)
	return f
}

// Handler returns the HTTP handler with every route.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Open routes.
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, obj{"success": true}) })
	mux.HandleFunc("GET /healthcheck", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /auth/refresh", s.handleRefresh)
	mux.HandleFunc("POST /logout", s.handleLogout)

	// Signed-in routes.
	a := s.requireAuth
	mux.HandleFunc("POST /api/authorize", a(s.handleAuthorize))
	mux.HandleFunc("GET /api/me", a(s.handleMe))
	mux.HandleFunc("GET /api/me/progress/{id}", a(s.handleGetProgress))
	mux.HandleFunc("PATCH /api/me/progress/{id}", a(s.handlePatchProgress))
	mux.HandleFunc("DELETE /api/me/progress/{id}", a(s.handleDeleteProgress))
	mux.HandleFunc("PATCH /api/me/progress/batch/update", a(s.handleBatchProgress))
	mux.HandleFunc("GET /api/me/items-in-progress", a(s.handleItemsInProgress))
	mux.HandleFunc("GET /api/me/progress/{id}/remove-from-continue-listening", a(s.handleHideProgress))
	mux.HandleFunc("POST /api/me/item/{id}/bookmark", a(s.handleAddBookmark))
	mux.HandleFunc("PATCH /api/me/item/{id}/bookmark", a(s.handleAddBookmark))
	mux.HandleFunc("DELETE /api/me/item/{id}/bookmark/{time}", a(s.handleDeleteBookmark))
	mux.HandleFunc("GET /api/me/listening-stats", a(s.handleMyStats))
	mux.HandleFunc("GET /api/me/progress", a(s.handleAllProgress))
	mux.HandleFunc("GET /api/me/progress/{id}/{episode}", a(s.handleNoPodcasts))
	mux.HandleFunc("GET /api/me/bookmarks", a(s.handleBookmarks))
	mux.HandleFunc("GET /api/me/bookmarks/{id}", a(s.handleBookmarks))
	mux.HandleFunc("GET /api/me/listening-sessions", a(s.handleNoSessions))
	mux.HandleFunc("GET /api/me/item/listening-sessions/{id}", a(s.handleNoSessions))
	mux.HandleFunc("GET /api/me/item/listening-sessions/{id}/{episode}", a(s.handleNoSessions))
	mux.HandleFunc("GET /api/me/sessions", a(s.handleNoSessions))
	mux.HandleFunc("GET /api/me/series/{id}/remove-from-continue-listening", a(s.handleSeriesContinue))
	mux.HandleFunc("GET /api/me/series/{id}/readd-to-continue-listening", a(s.handleSeriesContinue))

	mux.HandleFunc("GET /api/libraries", a(s.handleLibraries))
	mux.HandleFunc("GET /api/libraries/{lib}", a(s.handleLibrary))
	mux.HandleFunc("GET /api/libraries/{lib}/items", a(s.handleLibraryItems))
	mux.HandleFunc("GET /api/libraries/{lib}/personalized", a(s.handlePersonalized))
	mux.HandleFunc("GET /api/libraries/{lib}/authors", a(s.handleAuthors))
	mux.HandleFunc("GET /api/libraries/{lib}/series", a(s.handleSeriesList))
	mux.HandleFunc("GET /api/libraries/{lib}/search", a(s.handleSearch))
	mux.HandleFunc("GET /api/libraries/{lib}/filterdata", a(s.handleFilterData))
	mux.HandleFunc("GET /api/libraries/{lib}/series/{sid}", a(s.handleSeries))
	mux.HandleFunc("GET /api/libraries/{lib}/narrators", a(s.handleNarrators))
	mux.HandleFunc("GET /api/libraries/{lib}/stats", a(s.handleLibraryStats))
	mux.HandleFunc("GET /api/libraries/{lib}/collections", a(s.handleEmptyPaged))
	mux.HandleFunc("GET /api/libraries/{lib}/playlists", a(s.handleEmptyPaged))
	mux.HandleFunc("GET /api/libraries/{lib}/recent-episodes", a(s.handleEmptyPaged))
	mux.HandleFunc("GET /api/collections", a(s.handleNoCollections))
	mux.HandleFunc("GET /api/playlists", a(s.handleNoPlaylists))

	mux.HandleFunc("GET /api/items/{id}", a(s.handleItem))
	mux.HandleFunc("POST /api/items/batch/get", a(s.handleBatchGet))
	mux.HandleFunc("GET /api/items/{id}/cover", a(s.handleCover))
	mux.HandleFunc("GET /api/items/{id}/file/{ino}", a(s.handleFile))
	mux.HandleFunc("GET /api/items/{id}/file/{ino}/download", a(s.handleFileDownload))
	mux.HandleFunc("GET /api/items/{id}/download", a(s.handleItemDownload))
	mux.HandleFunc("POST /api/items/{id}/play", a(s.handlePlay))
	mux.HandleFunc("POST /api/items/{id}/play/{episode}", a(s.handleNoPodcasts))

	mux.HandleFunc("GET /api/session/{sid}", a(s.handleGetSession))
	mux.HandleFunc("POST /api/session/{sid}/sync", a(s.handleSync))
	mux.HandleFunc("POST /api/session/{sid}/close", a(s.handleClose))
	mux.HandleFunc("POST /api/session/local", a(s.handleLocalSession))
	mux.HandleFunc("POST /api/session/local-all", a(s.handleLocalAll))

	mux.HandleFunc("GET /api/authors/{aid}", a(s.handleAuthor))
	mux.HandleFunc("GET /api/authors/{aid}/image", a(s.handleAuthorImage))
	mux.HandleFunc("GET /api/series/{sid}", a(s.handleSeries))
	// Anything else is answered 404 and logged, so an app's unmet calls show up.
	mux.HandleFunc("/", s.handleUnsupported)

	return s.withCommon(mux)
}

// withCommon adds CORS (web clients) and recovers from panics.
func (s *Server) withCommon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("audiobook server: panic", "route", applog.RouteLabel(r), "err", rec)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-refresh-token, x-return-tokens, Range")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		h.Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		s.logRequest(r, sw.status, sw.bytes)
	})
}

// requireAuth checks the bearer token (header, or ?token= for players that can't set
// headers on a stream URL).
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		u, family, err := s.Accounts.Validate(r.Context(), tok)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		ctx = context.WithValue(ctx, familyKey, family)
		ctx = context.WithValue(ctx, tokenKey, tok)
		h(w, r.WithContext(ctx))
	}
}

func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
			return strings.TrimSpace(h[7:])
		}
	}
	return r.URL.Query().Get("token")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, obj{
		"app": "audiobookshelf", "serverVersion": ServerVersion, "isInit": true, "language": "en-us",
		"authMethods":  []string{"local"},
		"authFormData": obj{"authOpenIDButtonText": nil, "authOpenIDAutoLaunch": false, "authLoginCustomMessage": ""},
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := netutil.ClientIP(r)
	if !s.limiter.allow("ip:" + ip) {
		writeError(w, http.StatusTooManyRequests, "Too many login attempts, try again in a minute")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	// Audiobookshelf takes a form post as well as JSON.
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		body.Username, body.Password = r.PostForm.Get("username"), r.PostForm.Get("password")
	} else if !readJSON(w, r, &body) {
		return
	}
	// A second limit per account, so many addresses can't take turns guessing one
	// person's password.
	userKey := "user:" + strings.ToLower(strings.TrimSpace(body.Username))
	if !s.limiter.allow(userKey) {
		writeError(w, http.StatusTooManyRequests, "Too many login attempts, try again in a minute")
		return
	}
	u, t, err := s.Accounts.Login(r.Context(), body.Username, body.Password, clientName(r))
	if err != nil {
		if errors.Is(err, errNoAccess) {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		s.log.Info("audiobook server: failed sign-in", "username", body.Username, "ip", ip)
		writeError(w, http.StatusUnauthorized, "Invalid username or password")
		return
	}
	s.limiter.reset("ip:" + ip)
	s.limiter.reset(userKey)
	s.log.Info("audiobook server: signed in", "user", u.Username, "client", clientName(r))
	// Apps that don't ask for the refresh token in the reply get it as a cookie, as
	// Audiobookshelf does, and refresh with that.
	if r.Header.Get("x-return-tokens") != "true" {
		setRefreshCookie(w, r, t.Refresh)
	}
	writeJSON(w, http.StatusOK, s.loginJSON(r.Context(), u, &t))
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	rt := r.Header.Get("x-refresh-token")
	if rt == "" {
		var body struct {
			RefreshToken string `json:"refreshToken"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
		rt = body.RefreshToken
	}
	if rt == "" {
		if c, err := r.Cookie("refresh_token"); err == nil {
			rt = c.Value
		}
	}
	u, t, err := s.Accounts.Refresh(r.Context(), rt)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Invalid refresh token")
		return
	}
	writeJSON(w, http.StatusOK, s.loginJSON(r.Context(), u, &t))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := bearer(r); tok != "" {
		s.Accounts.Logout(r.Context(), tok)
	}
	writeJSON(w, http.StatusOK, obj{})
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, obj{
		"user": s.meJSON(r), "userDefaultLibraryId": libraryID,
		"serverSettings": s.serverSettings(), "ereaderDevices": []obj{}, "Source": "docker",
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.meJSON(r))
}

// meJSON is the signed-in user, for /api/me, /api/authorize and the replies that echo
// the user. Audiobookshelf's user carries the long-lived "token"; this server keeps only
// a hash of it, so it can only hand it back to a device that signed in with it (ShelfPlayer
// does, and can't read the user without it). Anyone else gets no token key at all — the
// official app signs itself out if "token" equals the access token it sent.
func (s *Server) meJSON(r *http.Request) obj {
	o := s.userJSON(r.Context(), userOf(r), nil)
	if tok, _ := r.Context().Value(tokenKey).(string); tok != "" && s.Accounts.IsLegacy(r.Context(), tok) {
		o["token"] = tok
	}
	return o
}

func (s *Server) handleNoPodcasts(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "Podcasts aren't served here")
}

// --- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeOK is Audiobookshelf's bare success (res.sendStatus(200)): the text "OK".
func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// writeError answers the way Audiobookshelf does: a plain-text message.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return false
	}
	return true
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// clientName makes a short label from the app's User-Agent ("Lissen/1.8 …" → "Lissen").
func clientName(r *http.Request) string {
	ua := strings.TrimSpace(r.UserAgent())
	if ua == "" {
		return ""
	}
	name := strings.Fields(ua)[0]
	if i := strings.IndexByte(name, '/'); i > 0 {
		name = name[:i]
	}
	if strings.EqualFold(name, "okhttp") || strings.EqualFold(name, "Dalvik") {
		return "Android app"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	return name
}

// loginLimiter allows 10 attempts a minute per key (an address, or an account).
type loginLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{hits: map[string][]time.Time{}} }

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cut := now.Add(-time.Minute)
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 10 {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	if len(l.hits) > 10000 { // bound memory under a flood of distinct keys
		for k, v := range l.hits {
			if len(v) == 0 || v[len(v)-1].Before(cut) {
				delete(l.hits, k)
			}
		}
	}
	return true
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}

// --- listener --------------------------------------------------------------

// Manager starts and stops the listener as the admin switches the server on and off.
type Manager struct {
	srv  *Server
	addr string
	log  *slog.Logger

	mu      sync.Mutex
	http    *http.Server
	running bool
	lastErr string
}

// NewManager makes a manager listening on addr (e.g. ":13378") when enabled.
func NewManager(srv *Server, addr string, log *slog.Logger) *Manager {
	return &Manager{srv: srv, addr: addr, log: log}
}

// Addr is the address the listener binds.
func (m *Manager) Addr() string { return m.addr }

// Running reports whether the listener is up, and the last start error if not.
func (m *Manager) Running() (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running, m.lastErr
}

// Apply starts or stops the listener to match enabled.
func (m *Manager) Apply(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if enabled == m.running {
		return
	}
	if !enabled {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.http.Shutdown(ctx)
		m.http, m.running = nil, false
		m.log.Info("audiobook server stopped")
		return
	}
	ln, err := net.Listen("tcp", m.addr)
	if err != nil {
		m.lastErr = err.Error()
		m.log.Warn("audiobook server: couldn't listen", "addr", m.addr, "err", err)
		return
	}
	m.http = &http.Server{Handler: m.srv.Handler(), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
	m.running, m.lastErr = true, ""
	srv := m.http
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("audiobook server failed", "err", err)
			m.mu.Lock()
			m.running, m.lastErr = false, err.Error()
			m.mu.Unlock()
		}
	}()
	m.log.Info("audiobook server listening", "addr", m.addr)
}

// Stop shuts the listener down (app shutdown).
func (m *Manager) Stop() { m.Apply(false) }

// statusWriter remembers the status a handler answered with. It passes ReadFrom through
// so file streams keep the fast path, and Unwrap for http.ResponseController.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := io.Copy(w.ResponseWriter, src)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// signInPaths are the steps of connecting an app, logged whatever the answer.
var signInPaths = map[string]bool{"/status": true, "/ping": true, "/login": true, "/auth/refresh": true, "/logout": true, "/api/authorize": true}

// logRequest logs what explains an app that won't connect or shows nothing: every request
// an app makes while signing in and browsing, with its answer and size and the app's name.
// It names the kind of call, never the thing: the route pattern ("POST /api/items/{id}/play")
// and the names of the query parameters, never a book, author, series, search term or
// token — admins see how much and when people listen, never what. The steady traffic of
// playing (audio, covers, place syncs) is only logged when it fails, and a book nobody has
// started having no place yet isn't worth a line — unless an admin has switched tracing on
// (trace.go), when those are logged too, the same way, tagged trace=true.
func (s *Server) logRequest(r *http.Request, status int, bytes int64) {
	if status == 0 {
		status = http.StatusOK
	}
	p := r.URL.Path
	quiet := status < 400 && !signInPaths[p] && (strings.Contains(p, "/file/") || strings.HasSuffix(p, "/cover") ||
		strings.HasSuffix(p, "/image") || strings.HasSuffix(p, "/sync") || strings.HasSuffix(p, "/download") ||
		strings.HasPrefix(p, "/api/me/progress") || strings.HasPrefix(p, "/api/session/") && r.Method == http.MethodGet)
	if status == http.StatusNotFound && r.Method == http.MethodGet && strings.HasPrefix(p, "/api/me/progress/") {
		quiet = true
	}
	attrs := []any{"route", applog.RouteLabel(r), "query_keys", applog.QueryKeys(r.URL.Query(), "token"),
		"status", status, "bytes", bytes, "token", bearer(r) != "", "client", r.UserAgent()}
	if quiet {
		if !s.tracing() {
			return
		}
		attrs = append(attrs, "trace", true)
	}
	s.log.Info("audiobook server: request", attrs...)
}

// setRefreshCookie hands the refresh token over as Audiobookshelf's refresh_token cookie.
func setRefreshCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: "refresh_token", Value: token, Path: "/", HttpOnly: true,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		SameSite: http.SameSiteLaxMode, MaxAge: int(refreshTTL / time.Second)})
}
