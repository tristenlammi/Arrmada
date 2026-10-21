package store

import "testing"

func TestSupportedPostgresVersion(t *testing.T) {
	for _, tt := range []struct {
		version int
		want    bool
	}{
		{0, false}, {150019, false}, {159999, false},
		{160000, true}, {160015, true}, {170011, true}, {180006, true},
		{189999, true}, {190000, false},
	} {
		if got := supportedPostgresVersion(tt.version); got != tt.want {
			t.Errorf("version %d supported = %v, want %v", tt.version, got, tt.want)
		}
	}
}
