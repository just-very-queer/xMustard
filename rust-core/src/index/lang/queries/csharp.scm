; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.

(class_declaration
  name: (identifier) @name) @definition.class

(record_declaration
  name: (identifier) @name) @definition.class

(struct_declaration
  name: (identifier) @name) @definition.struct

(interface_declaration
  name: (identifier) @name) @definition.interface

(enum_declaration
  name: (identifier) @name) @definition.enum

(enum_member_declaration
  name: (identifier) @name) @definition.enum_member

(namespace_declaration
  name: (_) @name) @definition.namespace

(file_scoped_namespace_declaration
  name: (_) @name) @definition.namespace

(method_declaration
  name: (identifier) @name
  parameters: (parameter_list) @params) @definition.method

(constructor_declaration
  name: (identifier) @name
  parameters: (parameter_list) @params) @definition.constructor

(property_declaration
  name: (identifier) @name) @definition.field

(field_declaration
  (variable_declaration
    (variable_declarator
      name: (identifier) @name))) @definition.field

(delegate_declaration
  name: (identifier) @name) @definition.type

(invocation_expression
  function: (identifier) @reference.call)

(invocation_expression
  function: (member_access_expression
    name: (identifier) @reference.member_call))

(object_creation_expression
  type: (identifier) @reference.call)

(member_access_expression
  name: (identifier) @reference.member)

(base_list (identifier) @reference.extends)
(base_list (generic_name (identifier) @reference.extends))

(assignment_expression
  left: (identifier) @reference.write)

(parameter
  name: (identifier) @reference.bind)

(variable_declaration
  type: (identifier) @reference.type)

(using_directive
  name: (identifier) @alias
  (_) @module) @import.namespace

(using_directive
  !name
  .
  [(identifier) (qualified_name)] @module) @import.namespace
