require 'json'
require_relative "lib/util"

# COMMENT_ONLY_WORD
module Outer
  # Doc comment.
  class Widget < Base
    include Mixin
    LIMIT = 3

    def initialize(a, b = 1)
      @a = helper(a)
      label = "STRING_ONLY_WORD"
    end

    def self.build(*args)
      new(*args)
    end

    def ready?
      true
    end
  end
end

def top_level(x)
  x.to_s
end
