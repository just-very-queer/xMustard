package com.acme.app;

import com.acme.Circle;
import com.acme.Shape;
import com.acme.util.*;
import static com.acme.util.Strings.twice;

public class Main {
    void run() {
        Circle c = new Circle();
        c.describe();
        c.area();
        Shape s = c;
        s.area();
        Strings.pad("x");
        twice("y");
        Circle.unit();
        c.greet();
    }
}
