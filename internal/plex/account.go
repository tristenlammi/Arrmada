package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Account is a signed-in Plex user's identity (from plex.tv /api/v2/user).
type Account struct {
	ID       int64  `json:"id"`
	UUID     string `json:"uuid"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Thumb    string `json:"thumb"`
}

// GetAccount returns the identity of the user who owns the token — used after a Plex sign-in to
// know who's logging in.
func GetAccount(ctx context.Context, clientID, token string) (Account, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, plexTV()+"/api/v2/user", nil)
	if err != nil {
		return Account{}, err
	}
	plexHeaders(req, clientID, "")
	req.Header.Set("X-Plex-Token", token)
	resp, err := plexHTTP.Do(req)
	if err != nil {
		return Account{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return Account{}, fmt.Errorf("plex: account HTTP %d", resp.StatusCode)
	}
	var a Account
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return Account{}, err
	}
	if a.ID == 0 && a.UUID == "" {
		return Account{}, fmt.Errorf("plex: empty account response")
	}
	return a, nil
}

// serverIDs returns the machine identifiers (clientIdentifier) of the Plex Media Server resources
// a token can reach. ownedOnly restricts to servers the token OWNS (i.e. the account's own server).
func serverIDs(ctx context.Context, clientID, token string, ownedOnly bool) ([]string, error) {
	servers, err := listServers(ctx, clientID, token)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range servers {
		if ownedOnly && !s.owned {
			continue
		}
		ids = append(ids, s.id)
	}
	return ids, nil
}

type serverRef struct {
	id    string
	owned bool
}

// listServers is every Plex Media Server resource a token can reach, and whether it owns it.
func listServers(ctx context.Context, clientID, token string) ([]serverRef, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, plexTV()+"/api/v2/resources", nil)
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
		ClientIdentifier string `json:"clientIdentifier"`
		Provides         string `json:"provides"`
		Owned            bool   `json:"owned"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resources); err != nil {
		return nil, err
	}
	var out []serverRef
	for _, r := range resources {
		if !strings.Contains(r.Provides, "server") || r.ClientIdentifier == "" {
			continue
		}
		out = append(out, serverRef{id: r.ClientIdentifier, owned: r.Owned})
	}
	return out, nil
}

// OwnedServerID returns the machine identifier of the server the admin token owns — the server
// Plex sign-ins are gated against. Empty error string when the account owns no server.
func OwnedServerID(ctx context.Context, clientID, adminToken string) (string, error) {
	ids, err := serverIDs(ctx, clientID, adminToken, true)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("no owned Plex server found for the admin token")
	}
	return ids[0], nil
}

// HasServerAccess reports whether a user's token can reach the server with the given machine
// identifier — i.e. they're a Home member or a friend the owner shared the server with. This is
// the authorization gate: only people with real access to your server may sign in.
func HasServerAccess(ctx context.Context, clientID, userToken, machineID string) (bool, error) {
	access, _, err := ServerAccess(ctx, clientID, userToken, machineID)
	return access, err
}

// ServerAccess is HasServerAccess plus whether the token OWNS that server — the proof that
// the person signing in is the server's owner — from one plex.tv call.
func ServerAccess(ctx context.Context, clientID, userToken, machineID string) (access, owned bool, err error) {
	servers, err := listServers(ctx, clientID, userToken)
	if err != nil {
		return false, false, err
	}
	for _, s := range servers {
		if s.id == machineID {
			access = true
			owned = owned || s.owned
		}
	}
	return access, owned, nil
}
