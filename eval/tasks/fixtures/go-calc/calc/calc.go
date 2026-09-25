// Package calc holds small integer statistics helpers.
package calc

// Sum returns the sum of xs.
func Sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}

// Mean returns the arithmetic mean of xs.
func Mean(xs []int) float64 {
	return float64(Sum(xs) / len(xs))
}

// Max returns the largest element of xs and false when xs is empty.
func Max(xs []int) (int, bool) {
	if len(xs) == 0 {
		return 0, false
	}
	best := xs[0]
	for _, x := range xs[1:] {
		if x > best {
			best = x
		}
	}
	return best, true
}
