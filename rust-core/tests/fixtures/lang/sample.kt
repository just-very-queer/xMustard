package com.example.app

import kotlin.math.max
import com.example.util.Helper as H

// COMMENT_ONLY_WORD
/** Doc comment. */
class Widget(val n: Int) : Base(), Thing {
    private val hidden = 1

    fun compute(a: Int, b: Int): Int {
        val s = "STRING_ONLY_WORD"
        return helper(a) + b
    }

    private fun secret() {}

    companion object {
        fun make(): Widget {
            return Widget(1)
        }
    }
}

interface Thing {
    fun run()
}

object Registry {
    fun lookup(key: String) = key
}

fun top(x: Int) = max(x, 1)

typealias Name = String
