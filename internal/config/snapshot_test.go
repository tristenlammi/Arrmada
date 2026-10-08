package config

import "testing"

// Skipping the pre-migrate snapshot is the risky choice, so only a clear "yes"
// turns it on; anything else keeps the snapshot.
func TestSkipMigrationSnapshotEnv(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"yse", false},
		{"1", true},
		{"true", true},
	} {
		t.Setenv("ARRMADA_SKIP_MIGRATION_SNAPSHOT", tc.val)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.SkipMigrationSnapshot != tc.want {
			t.Errorf("ARRMADA_SKIP_MIGRATION_SNAPSHOT=%q: got %v, want %v", tc.val, c.SkipMigrationSnapshot, tc.want)
		}
	}
}
