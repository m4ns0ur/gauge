// Package calc provides small numeric helpers.
package calc

// Average returns the arithmetic mean of values, or 0 for an empty slice.
func Average(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0
	for _, value := range values {
		sum += value
	}
	return float64(sum / len(values))
}
