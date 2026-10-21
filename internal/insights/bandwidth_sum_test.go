package insights

import "testing"

func TestBandwidthSumRejectsOverflow(t *testing.T) {
	const max = int64(1<<63 - 1)
	const min = -max - 1
	for _, tt := range []struct {
		a, b, want int64
		ok         bool
	}{
		{100, 200, 300, true},
		{max, 0, max, true},
		{min, 0, min, true},
		{max, 1, min, false},
		{min, -1, max, false},
		{max, -1, max - 1, true},
		{min, 1, min + 1, true},
	} {
		if got, ok := bandwidthSum(tt.a, tt.b); got != tt.want || ok != tt.ok {
			t.Errorf("%d + %d = %d/%v, want %d/%v", tt.a, tt.b, got, ok, tt.want, tt.ok)
		}
	}
}
