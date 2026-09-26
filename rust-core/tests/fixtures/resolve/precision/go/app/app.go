package app

import (
	"fmt"
	"strings"

	"example.com/prec/shapes"
)

func Run() float64 {
	c := shapes.NewCircle(1)
	c.Grow()
	sq := shapes.Square{S: 2}
	a := sq.Area()
	fmt.Println(strings.ToUpper(shapes.Describe(sq)))
	return a + c.Area() + shapes.Total(nil) + helper()
}

func helper() float64 {
	area := func() float64 { return 1 }
	return area() + twice(local())
}

func local() float64 {
	var s shapes.Shape = shapes.Square{S: 1}
	return s.Area()
}

func twice(x float64) float64 {
	return x * 2
}
