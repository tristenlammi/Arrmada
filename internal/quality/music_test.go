package quality

import (
	"strings"
	"testing"
)

// No music upgrade sweep exists, so no starter profile may promise one. When the sweep is
// built, this test goes with it and the descriptions can say so again.
func TestMusicPresetsDontPromiseUpgrades(t *testing.T) {
	for _, p := range MusicPresets() {
		if strings.Contains(strings.ToLower(p.Description), "upgrade") {
			t.Errorf("preset %q promises upgrades: %q", p.Name, p.Description)
		}
	}
}
