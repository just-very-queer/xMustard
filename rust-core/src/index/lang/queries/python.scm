; Original xMustard query (WS-16). Capture vocabulary follows the MIT tree-sitter tags.scm
; convention; no pattern is copied from an upstream query.

(class_definition
  name: (identifier) @name
  superclasses: (argument_list (identifier) @reference.extends)?) @definition.class

(class_definition
  superclasses: (argument_list (attribute attribute: (identifier) @reference.extends)))

(class_definition
  body: (block
    (function_definition
      name: (identifier) @name
      parameters: (parameters) @params) @definition.method))

(class_definition
  body: (block
    (decorated_definition
      definition: (function_definition
        name: (identifier) @name
        parameters: (parameters) @params) @definition.method)))

(function_definition
  name: (identifier) @name
  parameters: (parameters) @params) @definition.function

(module
  (expression_statement
    (assignment
      left: (identifier) @name) @definition.variable))

(call
  function: (identifier) @reference.call)

(call
  function: (attribute
    attribute: (identifier) @reference.member_call))

(attribute
  attribute: (identifier) @reference.member)

(assignment
  left: (identifier) @reference.write)

(assignment
  left: (attribute
    attribute: (identifier) @reference.write))

(parameters (identifier) @reference.bind)
(default_parameter name: (identifier) @reference.bind)
(typed_parameter (identifier) @reference.bind)
(typed_default_parameter name: (identifier) @reference.bind)

(type (identifier) @reference.type)

(import_statement
  name: (dotted_name) @module) @import.import

(import_statement
  name: (aliased_import
    name: (dotted_name) @module
    alias: (identifier) @alias)) @import.import

(import_from_statement
  module_name: (_) @module
  name: (dotted_name) @imported) @import.import

(import_from_statement
  module_name: (_) @module
  name: (aliased_import
    name: (dotted_name) @imported
    alias: (identifier) @alias)) @import.import

(import_from_statement
  module_name: (_) @module
  (wildcard_import)) @import.wildcard
