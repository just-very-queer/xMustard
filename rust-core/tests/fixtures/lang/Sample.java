package com.example;

import java.util.List;
import static java.lang.Math.*;

/** Doc comment with STRING_ONLY_WORD. */
public class Sample extends Base implements Runnable, Comparable<Sample> {
    // COMMENT_ONLY_WORD
    private int count;

    public Sample(int count) {
        this.count = count;
    }

    public int compute(int a, String b) {
        String s = "STRING_ONLY_WORD";
        return helper(a) + count;
    }

    private void hidden() {}

    interface Listener {
        void onEvent(int code);
    }

    enum Mode { FAST, SLOW }
}
