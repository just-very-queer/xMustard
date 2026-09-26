; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.
; Grammar: tree-sitter-kotlin-ng. Interfaces parse as class_declaration.

(class_declaration
  name: (identifier) @name) @definition.class

(object_declaration
  name: (identifier) @name) @definition.class

(companion_object
  name: (identifier) @name) @definition.class

(function_declaration
  name: (identifier) @name
  (function_value_parameters) @params) @definition.function

(secondary_constructor
  (function_value_parameters) @params) @definition.constructor

(type_alias
  type: (identifier) @name) @definition.type

(enum_entry
  (identifier) @name) @definition.enum_member

(class_body
  (property_declaration
    (variable_declaration
      (identifier) @name)) @definition.field)

(source_file
  (property_declaration
    (variable_declaration
      (identifier) @name)) @definition.variable)

(call_expression
  .
  (identifier) @reference.call)

(call_expression
  .
  (navigation_expression
    (identifier) @reference.member_call
    .))

(navigation_expression
  (identifier) @reference.member
  .)

(delegation_specifier
  (constructor_invocation
    (user_type (identifier) @reference.extends)))

(delegation_specifier
  (user_type (identifier) @reference.implements))

(user_type (identifier) @reference.type)

(assignment
  .
  (identifier) @reference.write)

(parameter
  .
  (identifier) @reference.bind)

(class_parameter
  .
  (identifier) @reference.bind)

(import
  (qualified_identifier) @module
  .
  (identifier)? @alias
  .) @import.import
