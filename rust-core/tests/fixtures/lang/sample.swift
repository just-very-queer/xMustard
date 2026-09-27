import Foundation
import struct Swift.Array

// COMMENT_ONLY_WORD
/// Doc comment.
public class Widget: Base, Thing {
    private var count = 0

    init(n: Int) {
        count = n
    }

    func compute(a: Int, b: Int) -> Int {
        let s = "STRING_ONLY_WORD"
        return helper(a) + b
    }

    fileprivate func secret() {}
}

struct Point {
    var x: Int
}

enum Mode {
    case fast
}

protocol Thing {
    func run()
}

extension Widget {
    func extra() {}
}

func top(_ x: Int) -> Int {
    return x
}
