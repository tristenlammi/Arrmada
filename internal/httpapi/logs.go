package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/applog"
)

// handleLogs returns recent application logs for the in-app Logs viewer.
//
//	GET /api/v1/logs?limit=500&level=info&q=ben10&hide=torznab,indexer+search
func (a *api) handleLogs(w http.ResponseWriter, r *http.Request) {
	if a.deps.Logs == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"entries": []applog.Entry{}})
		return
	}
	limit := 500
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		if v > 5000 {
			v = 5000
		}
		limit = v
	}
	entries := a.deps.Logs.Snapshot(applog.Filter{
		Limit: limit,
		Min:   parseLevel(r.URL.Query().Get("level")),
		Query: r.URL.Query().Get("q"),
		Hide:  r.URL.Query().Get("hide"),
	})
	if entries == nil {
		entries = []applog.Entry{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
