; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.
; Used by the legacy repo-map extractor; per-file facts come from the WS-07 walker.

(function_declaration
  name: (identifier) @name) @definition.function

(class_declaration
  name: (type_identifier) @name) @definition.class

(interface_declaration
  name: (type_identifier) @name) @definition.interface

(type_alias_declaration
  name: (type_identifier) @name) @definition.type

(method_definition
  name: (property_identifier) @name) @definition.method
