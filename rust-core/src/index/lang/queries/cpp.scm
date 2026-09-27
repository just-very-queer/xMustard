; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.

(function_definition
  declarator: (function_declarator
    declarator: [(identifier) (field_identifier) (destructor_name) (operator_name)] @name
    parameters: (parameter_list) @params)) @definition.function

(function_definition
  declarator: (function_declarator
    declarator: (qualified_identifier
      name: [(identifier) (destructor_name) (operator_name)] @name)
    parameters: (parameter_list) @params)) @definition.method

(function_definition
  declarator: (pointer_declarator
    declarator: (function_declarator
      declarator: (identifier) @name
      parameters: (parameter_list) @params))) @definition.function

(function_definition
  declarator: (reference_declarator
    (function_declarator
      declarator: (identifier) @name
      parameters: (parameter_list) @params))) @definition.function

(field_declaration_list
  (declaration
    declarator: (function_declarator
      declarator: [(identifier) (field_identifier)] @name
      parameters: (parameter_list) @params)) @definition.method)

(field_declaration_list
  (field_declaration
    declarator: (function_declarator
      declarator: (field_identifier) @name
      parameters: (parameter_list) @params)) @definition.method)

(class_specifier
  name: (type_identifier) @name
  body: (_)) @definition.class

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

(namespace_definition
  name: (namespace_identifier) @name) @definition.namespace

(type_definition
  declarator: (type_identifier) @name) @definition.type

(alias_declaration
  name: (type_identifier) @name) @definition.type

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
  function: (qualified_identifier
    name: (identifier) @reference.call))

(call_expression
  function: (field_expression
    field: (field_identifier) @reference.member_call))

(field_expression
  field: (field_identifier) @reference.member)

(base_class_clause (type_identifier) @reference.extends)
(base_class_clause (qualified_identifier name: (type_identifier) @reference.extends))

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

(using_declaration
  "namespace"
  (_) @module) @import.namespace
