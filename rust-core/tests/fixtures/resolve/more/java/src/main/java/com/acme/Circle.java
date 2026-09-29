package com.acme;

public class Circle extends Base {
    private final double r = 1.0;

    public double area() {
        return r * r;
    }

    public String describe() {
        return greet() + area() + scale(2);
    }

    public static Circle unit() {
        return new Circle();
    }

    int scale(int x) {
        return x * 2;
    }

    int scale(double x) {
        return (int) (x * 2);
    }
}
