// Package shapes is a resolver precision fixture: receivers, constructors,
// interfaces and methods declared away from their type.
package shapes

// Shape has an Area. Circle and Square satisfy it implicitly.
type Shape interface {
	Area() float64
}

type Circle struct {
	R float64
}

type Square struct {
	S float64
}

// NewCircle builds a Circle; "scale" in this comment is not a call.
func NewCircle(r float64) *Circle {
	return &Circle{R: scale(r) / 2}
}

func (c Circle) Area() float64 {
	return 3 * c.R * c.R
}

func (s Square) Area() float64 {
	return s.S * s.S
}

// Total sums areas through the interface.
func Total(shapes []Shape) float64 {
	t := 0.0
	for _, s := range shapes {
		t += s.Area()
	}
	return t
}

func Describe(s Shape) string {
	if s.Area() > 1 {
		return "big"
	}
	return label("small")
}
