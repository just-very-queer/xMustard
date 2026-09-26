package calc

import "testing"

func TestHiddenMeanFraction(t *testing.T) {
	if got := Mean([]int{1, 2}); got != 1.5 {
		t.Fatalf("Mean([1 2]) = %v, want 1.5", got)
	}
	if got := Mean([]int{-1, -2}); got != -1.5 {
		t.Fatalf("Mean([-1 -2]) = %v, want -1.5", got)
	}
}

func TestHiddenMeanEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Mean(nil) panicked: %v", r)
		}
	}()
	if got := Mean(nil); got != 0 {
		t.Fatalf("Mean(nil) = %v, want 0", got)
	}
}
