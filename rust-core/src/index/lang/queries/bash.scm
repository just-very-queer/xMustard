; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.

(function_definition
  name: (word) @name) @definition.function

(command
  name: (command_name (word) @reference.call))

(variable_assignment
  name: (variable_name) @reference.write)

(command
  name: (command_name (word) @_cmd)
  argument: [(word) (string) (raw_string)] @module
  (#match? @_cmd "^(source|\\.)$")) @import.require
