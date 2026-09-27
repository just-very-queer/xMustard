; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.

(class_declaration
  name: (name) @name) @definition.class

(interface_declaration
  name: (name) @name) @definition.interface

(trait_declaration
  name: (name) @name) @definition.trait

(enum_declaration
  name: (name) @name) @definition.enum

(namespace_definition
  name: (namespace_name) @name) @definition.namespace

(function_definition
  name: (name) @name
  parameters: (formal_parameters) @params) @definition.function

(method_declaration
  name: (name) @name
  parameters: (formal_parameters) @params) @definition.method

(property_declaration
  (property_element
    name: (variable_name (name) @name))) @definition.field

(const_declaration
  (const_element (name) @name)) @definition.constant

(function_call_expression
  function: (name) @reference.call)

(function_call_expression
  function: (qualified_name (name) @reference.call))

(member_call_expression
  name: (name) @reference.member_call)

(scoped_call_expression
  name: (name) @reference.member_call)

(object_creation_expression
  (name) @reference.call)

(member_access_expression
  name: (name) @reference.member)

(base_clause (name) @reference.extends)
(base_clause (qualified_name (name) @reference.extends))
(class_interface_clause (name) @reference.implements)
(class_interface_clause (qualified_name (name) @reference.implements))

(assignment_expression
  left: (variable_name (name) @reference.write))

(assignment_expression
  left: (member_access_expression
    name: (name) @reference.write))

(simple_parameter
  name: (variable_name (name) @reference.bind))

(namespace_use_declaration
  (namespace_use_clause
    .
    [(name) (qualified_name)] @module
    alias: (name)? @alias)) @import.import

[
  (require_expression (string (string_content) @module))
  (require_once_expression (string (string_content) @module))
  (include_expression (string (string_content) @module))
  (include_once_expression (string (string_content) @module))
] @import.require
