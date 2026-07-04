package math

import (
	"math"
	"testing"
)

func TestRound2(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"zero", 0, 0},
		{"integer", 5.0, 5.0},
		{"already 2dp", 3.14, 3.14},
		{"round down", 1.234, 1.23},
		{"round up", 1.235, 1.24},
		{"round up mid", 2.555, 2.56},
		{"negative round down", -1.234, -1.23},
		{"negative round up", -1.235, -1.24},
		{"one dp", 1.1, 1.1},
		{"many dp truncated", 9.99999, 10.0},
		{"small positive", 0.001, 0.0},
		{"small positive keeps", 0.005, 0.01},
		{"large integer", 1000000.0, 1000000.0},
		{"large with dp", 12345.678, 12345.68},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Round2(tc.in)
			if math.Abs(got-tc.want) > 1e-10 {
				t.Errorf("Round2(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
