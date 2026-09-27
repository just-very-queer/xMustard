; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.
; Used by the legacy repo-map extractor; per-file facts come from the WS-07 walker.

(function_item
  name: (identifier) @name) @definition.function

(impl_item
  (declaration_list
    (function_item
      name: (identifier) @name) @definition.method))

(struct_item
  name: (type_identifier) @name) @definition.struct

(enum_item
  name: (type_identifier) @name) @definition.enum

(trait_item
  name: (type_identifier) @name) @definition.trait

(impl_item
  type: (type_identifier) @name) @definition.type

(impl_item
  type: (generic_type
    type: (type_identifier) @name)) @definition.impl

(impl_item
  type: (scoped_type_identifier
    name: (type_identifier) @name)) @definition.impl

(mod_item
  name: (identifier) @name) @definition.module
