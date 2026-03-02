package codegen

import (
	"fmt"
	"strings"

	"github.com/yousuf/runbyte/internal/strutil"
)

// PythonSchemaConverter converts JSON Schema to Python type hints
type PythonSchemaConverter struct {
	generatedTypes map[string]*PyClass // Track generated types to avoid duplicates
}

// NewPythonSchemaConverter creates a new Python schema converter
func NewPythonSchemaConverter() *PythonSchemaConverter {
	return &PythonSchemaConverter{
		generatedTypes: make(map[string]*PyClass),
	}
}

// ConvertSchema converts a JSON Schema to a Python TypedDict
func (sc *PythonSchemaConverter) ConvertSchema(schema map[string]interface{}, typeName string) (*PyClass, error) {
	if schema == nil {
		return nil, nil
	}

	// Check if already generated
	if existing, ok := sc.generatedTypes[typeName]; ok {
		return existing, nil
	}

	pyClass := &PyClass{
		Name: typeName,
	}

	// Get description if available
	if desc, ok := schema["description"].(string); ok {
		pyClass.Description = desc
	}

	// Handle type
	schemaType, hasType := schema["type"]
	if !hasType {
		// Check for oneOf, anyOf
		if oneOf, ok := schema["oneOf"].([]interface{}); ok {
			return sc.convertUnion(oneOf, typeName)
		}
		if anyOf, ok := schema["anyOf"].([]interface{}); ok {
			return sc.convertUnion(anyOf, typeName)
		}

		// Default to Any
		return nil, nil
	}

	// For object type, convert to TypedDict
	if typeStr, ok := schemaType.(string); ok && typeStr == "object" {
		return sc.convertObject(schema, typeName)
	}

	// For non-object types, return nil (will be handled as simple type)
	return nil, nil
}

// convertObject converts an object schema to a TypedDict
func (sc *PythonSchemaConverter) convertObject(schema map[string]interface{}, typeName string) (*PyClass, error) {
	pyClass := &PyClass{
		Name:       typeName,
		Properties: []PyProperty{},
	}

	if desc, ok := schema["description"].(string); ok {
		pyClass.Description = desc
	}

	// Get properties
	properties, hasProps := schema["properties"].(map[string]interface{})
	if !hasProps || len(properties) == 0 {
		pyClass.Properties = []PyProperty{}
		sc.generatedTypes[typeName] = pyClass
		return pyClass, nil
	}

	// Get required fields
	required := make(map[string]bool)
	if reqArray, ok := schema["required"].([]interface{}); ok {
		for _, req := range reqArray {
			if reqStr, ok := req.(string); ok {
				required[reqStr] = true
			}
		}
	}

	// Convert each property
	for propName, propSchemaI := range properties {
		propSchema, ok := propSchemaI.(map[string]interface{})
		if !ok {
			continue
		}

		typeHint := sc.schemaToTypeHint(propSchema, strutil.ToPascalCase(propName))

		// Make optional if not in required list
		if !required[propName] {
			if !strings.HasPrefix(typeHint, "Optional[") {
				typeHint = fmt.Sprintf("Optional[%s]", typeHint)
			}
		}

		prop := PyProperty{
			Name:     propName,
			TypeHint: typeHint,
		}

		if desc, ok := propSchema["description"].(string); ok {
			prop.Description = desc
		}

		pyClass.Properties = append(pyClass.Properties, prop)
	}

	sc.generatedTypes[typeName] = pyClass
	return pyClass, nil
}

// schemaToTypeHint converts a JSON Schema to a Python type hint string
func (sc *PythonSchemaConverter) schemaToTypeHint(schema map[string]interface{}, suggestedName string) string {
	if schema == nil {
		return "Any"
	}

	// Check for oneOf, anyOf (union)
	if oneOf, ok := schema["oneOf"].([]interface{}); ok {
		return sc.unionToTypeHint(oneOf)
	}
	if anyOf, ok := schema["anyOf"].([]interface{}); ok {
		return sc.unionToTypeHint(anyOf)
	}

	// Get type
	schemaType, hasType := schema["type"]
	if !hasType {
		return "Any"
	}

	// Handle type as string or array of strings
	switch t := schemaType.(type) {
	case string:
		return sc.singleTypeToTypeHint(schema, t, suggestedName)
	case []interface{}:
		// Union type like ["string", "null"]
		return sc.typeArrayToTypeHint(t)
	default:
		return "Any"
	}
}

// singleTypeToTypeHint handles a single type string
func (sc *PythonSchemaConverter) singleTypeToTypeHint(schema map[string]interface{}, typeStr string, suggestedName string) string {
	switch typeStr {
	case "string":
		// Check for enum
		if enum, ok := schema["enum"].([]interface{}); ok {
			return sc.enumToTypeHint(enum)
		}
		return "str"

	case "number":
		return "float"

	case "integer":
		return "int"

	case "boolean":
		return "bool"

	case "null":
		return "None"

	case "array":
		items, ok := schema["items"].(map[string]interface{})
		if !ok {
			return "List[Any]"
		}
		elementType := sc.schemaToTypeHint(items, suggestedName+"Item")
		return fmt.Sprintf("List[%s]", elementType)

	case "object":
		// Check if this should be a nested TypedDict
		if properties, ok := schema["properties"].(map[string]interface{}); ok && len(properties) > 0 {
			// Generate a nested TypedDict
			nestedClass, err := sc.convertObject(schema, suggestedName)
			if err == nil && nestedClass != nil {
				return nestedClass.Name
			}
		}
		return "Dict[str, Any]"

	default:
		return "Any"
	}
}

// typeArrayToTypeHint handles type as array (union)
func (sc *PythonSchemaConverter) typeArrayToTypeHint(types []interface{}) string {
	typeHints := make([]string, 0, len(types))

	for _, t := range types {
		typeStr, ok := t.(string)
		if !ok {
			continue
		}

		hint := sc.singleTypeToTypeHint(nil, typeStr, "")
		typeHints = append(typeHints, hint)
	}

	if len(typeHints) == 0 {
		return "Any"
	}

	if len(typeHints) == 1 {
		return typeHints[0]
	}

	return fmt.Sprintf("Union[%s]", strings.Join(typeHints, ", "))
}

// unionToTypeHint converts a oneOf/anyOf to a Union type hint
func (sc *PythonSchemaConverter) unionToTypeHint(schemas []interface{}) string {
	typeHints := make([]string, 0, len(schemas))

	for i, schemaI := range schemas {
		schema, ok := schemaI.(map[string]interface{})
		if !ok {
			continue
		}

		hint := sc.schemaToTypeHint(schema, fmt.Sprintf("Option%d", i))
		typeHints = append(typeHints, hint)
	}

	if len(typeHints) == 0 {
		return "Any"
	}

	if len(typeHints) == 1 {
		return typeHints[0]
	}

	return fmt.Sprintf("Union[%s]", strings.Join(typeHints, ", "))
}

// enumToTypeHint converts an enum to a Union of literals
func (sc *PythonSchemaConverter) enumToTypeHint(enumValues []interface{}) string {
	// For now, just use str since we don't have Literal imports
	// In a more complete implementation, we'd return: Literal["value1", "value2", ...]
	return "str"
}

// convertUnion converts a oneOf/anyOf schema (not currently used, but kept for completeness)
func (sc *PythonSchemaConverter) convertUnion(schemas []interface{}, typeName string) (*PyClass, error) {
	// For unions, we don't generate a TypedDict, just return nil
	// The type hint will be generated inline as Union[...]
	return nil, nil
}
