package math

import "math"

// Round2 rounds a float to 2 decimal places.
func Round2(val float64) float64 {
	return math.Round(val*100) / 100
}
