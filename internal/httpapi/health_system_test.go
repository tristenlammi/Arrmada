package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/health"
)

// The panel answers from the registry's cache: a plain GET runs nothing, even when a
// check would hang; ?refresh=1 re-runs the checks, but not twice inside refreshGap.
func TestHandlerServesCachedResults(t *testing.T) {
	reg := health.NewRegistry(nil, nil)
	var runs atomic.Int32
	reg.Register(health.Check{Key: "c", Name: "C", Run: func(context.Context) []health.Finding {
		runs.Add(1)
		return []health.Finding{{Key: "c.bad", Level: health.LevelError, Message: "broken", Fix: health.FixIndexers}}
	}})
	a := &api{deps: Deps{Health: reg}, start: time.Now()}

	get := func(target string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		start := time.Now()
		a.handleSystemHealth(w, httptest.NewRequest(http.MethodGet, target, nil))
		if target == "/" && time.Since(start) > 100*time.Millisecond {
			t.Errorf("a cached GET took %v", time.Since(start))
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding %q: %v", w.Body.String(), err)
		}
		return got
	}

	if got := get("/"); runs.Load() != 0 || got["status"] != "ok" {
		t.Fatalf("plain GET ran a check (runs=%d) or reported %v", runs.Load(), got["status"])
	}
	got := get("/?refresh=1")
	if runs.Load() != 1 || got["status"] != "error" {
		t.Fatalf("refresh: runs=%d status=%v", runs.Load(), got["status"])
	}
	ws, _ := got["warnings"].([]any)
	if len(ws) != 1 {
		t.Fatalf("warnings: %v", got["warnings"])
	}
	w := ws[0].(map[string]any)
	if w["key"] != "c.bad" || w["link"] != "/indexers" || w["link_label"] != "Add an indexer" || w["since"] == nil {
		t.Errorf("warning fields: %v", w)
	}
	if cs, _ := got["checks"].([]any); len(cs) != 1 {
		t.Errorf("checks: %v", got["checks"])
	}
	get("/?refresh=1")
	get("/")
	if runs.Load() != 1 {
		t.Errorf("a second refresh inside the gap re-ran the checks (runs=%d)", runs.Load())
	}
}

// No registry wired (tools, old tests): an empty, healthy report rather than a crash.
func TestHandlerWithoutRegistry(t *testing.T) {
	a := &api{deps: Deps{}}
	w := httptest.NewRecorder()
	a.handleSystemHealth(w, httptest.NewRequest(http.MethodGet, "/", nil))
	var got struct {
		Status   string `json:"status"`
		Warnings []any  `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Status != "ok" || got.Warnings == nil {
		t.Errorf("got %s (%v)", w.Body.String(), err)
	}
}
