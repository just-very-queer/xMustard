pub trait Shape {
    fn area(&self) -> f64;

    fn describe(&self) -> String {
        self.area().to_string()
    }
}

pub struct Circle {
    pub r: f64,
}

impl Circle {
    pub fn new(r: f64) -> Circle {
        Circle { r }
    }

    pub fn grow(&mut self) {
        self.r += 1.0;
    }
}

impl Shape for Circle {
    fn area(&self) -> f64 {
        self.r * self.r
    }
}

pub struct Square {
    pub s: f64,
}

impl Shape for Square {
    fn area(&self) -> f64 {
        self.s * self.s
    }
}
