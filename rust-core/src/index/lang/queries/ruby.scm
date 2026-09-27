; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.

(class
  name: [(constant) @name (scope_resolution name: (_) @name)]
  superclass: (superclass (_) @reference.extends)?) @definition.class

(module
  name: [(constant) @name (scope_resolution name: (_) @name)]) @definition.module

(method
  name: (_) @name
  parameters: (method_parameters)? @params) @definition.function

(singleton_method
  name: (_) @name
  parameters: (method_parameters)? @params) @definition.function

(assignment
  left: (constant) @name) @definition.constant

(call
  method: (identifier) @reference.call)

(call
  receiver: (_)
  method: (identifier) @reference.member_call)

(call
  method: (identifier) @_m
  arguments: (argument_list (constant) @reference.extends)
  (#match? @_m "^(include|extend|prepend)$"))

(assignment
  left: (identifier) @reference.write)

(method_parameters (identifier) @reference.bind)
(optional_parameter name: (identifier) @reference.bind)

(call
  method: (identifier) @_req
  arguments: (argument_list
    (string (string_content) @module))
  (#match? @_req "^(require|require_relative|load)$")) @import.require
