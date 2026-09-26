package calc

import "testing"

func TestHiddenMedian(t *testing.T) {
	cases := []struct {
		in   []int
		want float64
	}{
		{[]int{5}, 5},
		{[]int{3, 1, 2}, 2},
		{[]int{4, 1, 3, 2}, 2.5},
		{nil, 0},
	}
	for _, c := range cases {
		if got := Median(c.in); got != c.want {
			t.Fatalf("Median(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestHiddenMedianKeepsInput(t *testing.T) {
	in := []int{3, 1, 2}
	_ = Median(in)
	if in[0] != 3 || in[1] != 1 || in[2] != 2 {
		t.Fatalf("Median modified its input: %v", in)
	}
}
