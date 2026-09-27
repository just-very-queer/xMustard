package shapes

func scale(x float64) float64 {
	return x * 2
}

func label(s string) string {
	return "shape:" + s
}

func (c *Circle) Grow() {
	c.R = scale(c.R)
	c.check()
}

func (c *Circle) check() {
	if c.Area() < 0 {
		panic("negative")
	}
}
