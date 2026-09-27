; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.

(class_declaration
  name: (identifier) @name) @definition.class

(record_declaration
  name: (identifier) @name
  parameters: (formal_parameters) @params) @definition.class

(interface_declaration
  name: (identifier) @name) @definition.interface

(enum_declaration
  name: (identifier) @name) @definition.enum

(annotation_type_declaration
  name: (identifier) @name) @definition.interface

(enum_constant
  name: (identifier) @name) @definition.enum_member

(method_declaration
  name: (identifier) @name
  parameters: (formal_parameters) @params) @definition.method

(constructor_declaration
  name: (identifier) @name
  parameters: (formal_parameters) @params) @definition.constructor

(field_declaration
  declarator: (variable_declarator
    name: (identifier) @name)) @definition.field

(method_invocation
  name: (identifier) @reference.call)

(object_creation_expression
  type: (type_identifier) @reference.call)

(field_access
  field: (identifier) @reference.member)

(superclass (type_identifier) @reference.extends)
(superclass (generic_type (type_identifier) @reference.extends))
(super_interfaces (type_list (type_identifier) @reference.implements))
(super_interfaces (type_list (generic_type (type_identifier) @reference.implements)))
(extends_interfaces (type_list (type_identifier) @reference.extends))

(assignment_expression
  left: (identifier) @reference.write)

(assignment_expression
  left: (field_access
    field: (identifier) @reference.write))

(type_identifier) @reference.type

(formal_parameter
  name: (identifier) @reference.bind)

(local_variable_declaration
  declarator: (variable_declarator
    name: (identifier) @reference.bind))

(import_declaration
  (scoped_identifier
    scope: (_) @module
    name: (identifier) @imported)
  .) @import.import

(import_declaration
  (scoped_identifier) @module
  (asterisk)) @import.wildcard
