package com.acme.util;

public class Strings {
    public static String pad(String s) {
        return " " + s;
    }

    public static String twice(String s) {
        return pad(s) + pad(s);
    }
}
