#include <stdio.h>
#include "local.h"

#define LIMIT 10
#define SQUARE(x) ((x) * (x))

/* COMMENT_ONLY_WORD */
struct Point {
    int x;
    int y;
};

typedef struct Point point_t;

enum Color { RED, GREEN };

/* Doc comment. */
static int helper(int a) {
    return a + LIMIT;
}

int *make(struct Point p, int n) {
    const char *s = "STRING_ONLY_WORD";
    p.x = helper(n);
    return 0;
}
