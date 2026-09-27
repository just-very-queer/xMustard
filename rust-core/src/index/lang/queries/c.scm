; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.

(function_definition
  declarator: (function_declarator
    declarator: (identifier) @name
    parameters: (parameter_list) @params)) @definition.function

(function_definition
  declarator: (pointer_declarator
    declarator: (function_declarator
      declarator: (identifier) @name
      parameters: (parameter_list) @params))) @definition.function

(struct_specifier
  name: (type_identifier) @name
  body: (_)) @definition.struct

(union_specifier
  name: (type_identifier) @name
  body: (_)) @definition.struct

(enum_specifier
  name: (type_identifier) @name
  body: (_)) @definition.enum

(enumerator
  name: (identifier) @name) @definition.enum_member

(type_definition
  declarator: (type_identifier) @name) @definition.type

(preproc_def
  name: (identifier) @name) @definition.macro

(preproc_function_def
  name: (identifier) @name
  parameters: (preproc_params) @params) @definition.macro

(field_declaration_list
  (field_declaration
    declarator: (field_identifier) @name) @definition.field)

(call_expression
  function: (identifier) @reference.call)

(call_expression
  function: (field_expression
    field: (field_identifier) @reference.member_call))

(field_expression
  field: (field_identifier) @reference.member)

(assignment_expression
  left: (identifier) @reference.write)

(assignment_expression
  left: (field_expression
    field: (field_identifier) @reference.write))

(parameter_declaration
  declarator: (identifier) @reference.bind)

(type_identifier) @reference.type

(preproc_include
  path: (_) @module) @import.include
