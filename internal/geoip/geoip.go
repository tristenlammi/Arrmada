// Package geoip resolves an IP address to a coarse location for the Insights activity/history
// views. It is offline-first: private/loopback addresses short-circuit to "Local", and city/
// country resolution uses a local MaxMind GeoLite2-City.mmdb when one is available. With no DB,
// public IPs simply return without a city (raw IP shown) — nothing ever leaves the box.
package geoip

import "net"

// Location is a resolved (or partially resolved) IP location.
type Location struct {
	IP          string `json:"ip"`
	Local       bool   `json:"local"`             // private/LAN/loopback address
	City        string `json:"city,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
	Lat         float64 `json:"lat,omitempty"`
	Lon         float64 `json:"lon,omitempty"`
}

// Resolver looks up locations. It is safe for concurrent use.
type Resolver struct {
	db cityDB // nil until a GeoLite2 DB is opened
}

// cityDB is the minimal surface we need from a GeoLite2 reader (kept as an interface so the
// mmdb dependency stays optional/swappable).
type cityDB interface {
	lookup(ip net.IP) (city, country, code string, lat, lon float64, ok bool)
	close() error
}

// New builds a resolver. dbPath is a GeoLite2-City.mmdb; empty (or unreadable) → city resolution
// is disabled and only Local detection runs.
func New(dbPath string) *Resolver {
	r := &Resolver{}
	if dbPath != "" {
		if db, err := openCityDB(dbPath); err == nil {
			r.db = db
		}
	}
	return r
}

// Enabled reports whether full city/country resolution is available.
func (r *Resolver) Enabled() bool { return r.db != nil }

// Close releases the DB handle.
func (r *Resolver) Close() error {
	if r.db != nil {
		return r.db.close()
	}
	return nil
}

// Lookup resolves an IP. Private/loopback → Local; public → city/country if a DB is loaded.
func (r *Resolver) Lookup(ipStr string) Location {
	loc := Location{IP: ipStr}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return loc
	}
	if isPrivate(ip) {
		loc.Local = true
		return loc
	}
	if r.db != nil {
		if city, country, code, lat, lon, ok := r.db.lookup(ip); ok {
			loc.City, loc.Country, loc.CountryCode, loc.Lat, loc.Lon = city, country, code, lat, lon
		}
	}
	return loc
}

// isPrivate reports whether an IP is loopback, link-local, or in an RFC1918/ULA private range.
func isPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate()
}
