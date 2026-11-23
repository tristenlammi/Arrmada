package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/buildinfo"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/jellyfinsvc"
	"github.com/tristenlammi/arrmada/internal/mediaserver"
	"github.com/tristenlammi/arrmada/internal/plexscan"
	"github.com/tristenlammi/arrmada/internal/safego"
	"github.com/tristenlammi/arrmada/internal/settings"
)

// mediaServers is every media server Arrmada talks to, wired once: Plex (its connection
// and index live in Insights, its scanner is built in main) and Jellyfin (its own
// service, index and scanner). Producers of library changes call Request, which tells
// each server's scanner; the import outbox's "plex.scan" consumer (the name kept so rows
// already queued still drain) calls it too.
type mediaServers struct {
	Plex         *plexscan.Scanner
	Jellyfin     *jellyfinsvc.Service
	JellyfinScan *plexscan.Scanner
	// Registry is the servers for features that work with whichever is connected
	// (Watch links, "ready", Recently added): Plex first, then Jellyfin.
	Registry *mediaserver.Registry

	insights *insights.Service
}

// mediaServerDeps is what wiring the media servers needs from main.
type mediaServerDeps struct {
	Group    *safego.Group
	Settings *settings.Service
	Keys     *apikeys.Store
	Insights *insights.Service
	// Plex is the Plex scanner main already built.
	Plex *plexscan.Scanner
	// Roots is Arrmada's movie and TV folder per plexscan kind, read live.
	Roots func(ctx context.Context) map[string]string
	// ExtraRoots is the extra Movies and TV folders per plexscan kind, read live.
	ExtraRoots func(ctx context.Context) map[string][]string
	Log        *slog.Logger // tagged "jellyfin"
}

// setupMediaServers builds the Jellyfin connection, its library index and its scanner,
// starts their workers, and puts both servers in one Registry.
func setupMediaServers(d mediaServerDeps) *mediaServers {
	jf := jellyfinsvc.New(jellyfinsvc.Options{
		Settings: d.Settings, Keys: d.Keys, Version: buildinfo.Version, Roots: d.Roots, Log: d.Log,
	})
	// Tell Jellyfin which folder changed, like Plex: one report per burst of changes,
	// translated to Jellyfin's paths with its own mappings. The index looks again once
	// Jellyfin's own wait after the last report is over.
	jf.SetExtraRoots(d.ExtraRoots)
	jfScan := jf.NewScanner(d.Settings, d.Roots, d.Log)
	jfScan.SetExtraRoots(d.ExtraRoots)
	d.Group.Loop("jellyfin: library scans", jfScan.Run)
	// The Jellyfin library index behind its Watch links: rebuilt every 30 minutes and
	// after each scan.
	d.Group.Loop("jellyfin: library index", jf.RunIndex)
	// Insights watches Jellyfin too: its own poller records plays (while Jellyfin's
	// monitoring switch is on) and counts towards Convert's "someone is watching".
	if d.Insights != nil {
		d.Insights.SetJellyfin(jf)
		d.Group.Loop("insights: jellyfin poller", d.Insights.RunJellyfin)
	}
	// An Emby server uses the Jellyfin slot; Watch buttons, notices and status lines then
	// say "Emby".
	mediaserver.SetJellyfinLabel(func() string { return jf.Label(context.Background()) })
	m := &mediaServers{Plex: d.Plex, Jellyfin: jf, JellyfinScan: jfScan, insights: d.Insights}
	m.Registry = mediaserver.NewRegistry(d.Insights.MediaServer(), jf)
	return m
}

// Request queues a scan of dir (a folder as Arrmada sees it) on every server; each
// scanner drops it at once when its server isn't set up or its scans are off.
func (m *mediaServers) Request(kind, dir string) {
	for _, sc := range []*plexscan.Scanner{m.Plex, m.JellyfinScan} {
		if sc != nil {
			sc.Request(kind, dir)
		}
	}
}

// IndexStaleAfterImport asks each server whose scans are switched off to look again a few
// minutes after an import: nothing tells it about the file, so its own watcher adds it in
// its own time. (With scans on, the scan's own listener does this.)
func (m *mediaServers) IndexStaleAfterImport() {
	ctx := context.Background()
	if m.Plex != nil && m.insights != nil && !m.Plex.Enabled(ctx) {
		m.insights.PlexIndexStale(3 * time.Minute)
	}
	if m.JellyfinScan != nil && m.Jellyfin != nil && !m.JellyfinScan.Enabled(ctx) {
		m.Jellyfin.IndexStale(3 * time.Minute)
	}
}
