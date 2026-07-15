package geoip

import (
	"net"

	"github.com/oschwald/geoip2-golang"
)

// maxmindDB adapts a MaxMind GeoLite2-City reader to the cityDB interface.
type maxmindDB struct{ r *geoip2.Reader }

// openCityDB opens a GeoLite2-City.mmdb file.
func openCityDB(path string) (cityDB, error) {
	r, err := geoip2.Open(path)
	if err != nil {
		return nil, err
	}
	return &maxmindDB{r: r}, nil
}

func (m *maxmindDB) lookup(ip net.IP) (city, country, code string, lat, lon float64, ok bool) {
	rec, err := m.r.City(ip)
	if err != nil || rec == nil {
		return "", "", "", 0, 0, false
	}
	city = rec.City.Names["en"]
	country = rec.Country.Names["en"]
	code = rec.Country.IsoCode
	lat, lon = rec.Location.Latitude, rec.Location.Longitude
	return city, country, code, lat, lon, city != "" || country != ""
}

func (m *maxmindDB) close() error { return m.r.Close() }
