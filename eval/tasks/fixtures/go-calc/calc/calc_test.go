package calc

import "testing"

func TestSum(t *testing.T) {
	if got := Sum([]int{1, 2, 3}); got != 6 {
		t.Fatalf("Sum = %d, want 6", got)
	}
}

func TestMeanWhole(t *testing.T) {
	if got := Mean([]int{2, 4}); got != 3 {
		t.Fatalf("Mean = %v, want 3", got)
	}
}

func TestMax(t *testing.T) {
	if got, ok := Max([]int{3, 9, 2}); !ok || got != 9 {
		t.Fatalf("Max = %d %v, want 9 true", got, ok)
	}
	if _, ok := Max(nil); ok {
		t.Fatal("Max(nil) should report false")
	}
}
