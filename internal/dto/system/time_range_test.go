package system

import "testing"

func TestValidateTimeRange(t *testing.T) {
	for _, tc := range []struct {
		start, end int64
		valid      bool
	}{
		{0, 0, true}, {100, 0, true}, {0, 200, true}, {100, 100, true}, {100, 200, true},
		{0, MaxUnixSecond, true}, {-1, 0, false}, {0, -1, false}, {200, 100, false},
		{1735689600000, 0, false}, {0, 1735689600000, false}, {MaxUnixSecond + 1, 0, false},
	} {
		if err := ValidateTimeRange(tc.start, tc.end); (err == nil) != tc.valid {
			t.Errorf("range %d-%d: %v", tc.start, tc.end, err)
		}
	}
}
