; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.
; Grammar: tree-sitter-swift. Classes, structs, enums, actors and extensions share
; class_declaration and differ by its declaration_kind keyword.

(class_declaration
  declaration_kind: ["class" "actor"]
  name: (type_identifier) @name) @definition.class

(class_declaration
  declaration_kind: "struct"
  name: (type_identifier) @name) @definition.struct

(class_declaration
  declaration_kind: "enum"
  name: (type_identifier) @name) @definition.enum

(class_declaration
  declaration_kind: "extension"
  name: (user_type (type_identifier) @name)) @definition.impl

(protocol_declaration
  name: (type_identifier) @name) @definition.interface

(function_declaration
  name: (simple_identifier) @name) @definition.function

(protocol_function_declaration
  name: (simple_identifier) @name) @definition.method

(init_declaration
  name: "init" @name) @definition.constructor

(typealias_declaration
  name: (type_identifier) @name) @definition.type

(enum_entry
  name: (simple_identifier) @name) @definition.enum_member

(class_body
  (property_declaration
    name: (pattern
      bound_identifier: (simple_identifier) @name)) @definition.field)

(call_expression
  .
  (simple_identifier) @reference.call)

(call_expression
  .
  (navigation_expression
    suffix: (navigation_suffix
      suffix: (simple_identifier) @reference.member_call)))

(navigation_suffix
  suffix: (simple_identifier) @reference.member)

(inheritance_specifier
  inherits_from: (user_type (type_identifier) @reference.extends))

(user_type (type_identifier) @reference.type)

(assignment
  target: (directly_assignable_expression
    (simple_identifier) @reference.write))

(parameter
  name: (simple_identifier) @reference.bind)

(import_declaration
  (identifier) @module) @import.import
