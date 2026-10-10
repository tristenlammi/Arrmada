package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Conn is one way to reach a Plex server, as plex.tv lists it.
type Conn struct {
	URI      string `json:"uri"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Local    bool   `json:"local"`
	Relay    bool   `json:"relay"`
}

// URL is the base URL to try for this connection. A plain-HTTP LAN connection is built
// from its address (what a container on the same network reaches best); anything else
// uses plex.tv's URI (the plex.direct name its certificate matches).
func (c Conn) URL() string {
	if c.Local && c.Protocol == "http" && c.Address != "" && c.Port > 0 {
		return fmt.Sprintf("http://%s:%d", c.Address, c.Port)
	}
	return c.URI
}

// ServerCandidate is a Plex Media Server the token can reach, with the connections worth
// trying in order.
type ServerCandidate struct {
	Name      string
	MachineID string
	Owned     bool
	Conns     []Conn
}

// DiscoverServers lists the Plex Media Servers the token OWNS — never a friend's server
// shared with it — each with its connections ordered local http, local https, then
// remote. Relayed connections are left out: they're bandwidth-capped and meant for
// players, not for a monitoring poller.
func DiscoverServers(ctx context.Context, clientID, token string) ([]ServerCandidate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, plexTV()+"/api/v2/resources?includeHttps=1", nil)
	if err != nil {
		return nil, err
	}
	plexHeaders(req, clientID, "")
	req.Header.Set("X-Plex-Token", token)
	resp, err := plexHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("plex: resources HTTP %d", resp.StatusCode)
	}
	var resources []struct {
		Name             string `json:"name"`
		Provides         string `json:"provides"`
		ClientIdentifier string `json:"clientIdentifier"`
		Owned            bool   `json:"owned"`
		Connections      []Conn `json:"connections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resources); err != nil {
		return nil, err
	}
	var out []ServerCandidate
	for _, res := range resources {
		if !strings.Contains(res.Provides, "server") || !res.Owned || res.ClientIdentifier == "" {
			continue
		}
		c := ServerCandidate{Name: res.Name, MachineID: res.ClientIdentifier, Owned: true}
		for _, conn := range res.Connections {
			if conn.Relay || conn.URL() == "" {
				continue
			}
			c.Conns = append(c.Conns, conn)
		}
		sort.SliceStable(c.Conns, func(i, j int) bool { return connRank(c.Conns[i]) < connRank(c.Conns[j]) })
		out = append(out, c)
	}
	return out, nil
}

// connRank orders connections: local http, local https, then remote.
func connRank(c Conn) int {
	switch {
	case c.Local && c.Protocol == "http":
		return 0
	case c.Local:
		return 1
	default:
		return 2
	}
}
