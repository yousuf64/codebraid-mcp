package codegen

import (
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/runbyte/internal/strutil"
)

// PythonGenerator generates Python files from tool definitions
type PythonGenerator struct {
	converter *PythonSchemaConverter
}

// NewPythonGenerator creates a new Python generator
func NewPythonGenerator() *PythonGenerator {
	return &PythonGenerator{
		converter: NewPythonSchemaConverter(),
	}
}

// GenerateFunctionFile generates a single Python file for one function with inline types
func (g *PythonGenerator) GenerateFunctionFile(serverName string, tool *mcp.Tool) (string, error) {
	if tool == nil {
		return "", fmt.Errorf("no tool provided for server %q", serverName)
	}

	// Reset converter for each file
	g.converter = NewPythonSchemaConverter()

	file := &PyFile{
		ServerName: serverName,
		Imports: []string{
			"from typing import Any, Dict, List, Optional, Union, TypedDict",
		},
		Classes:   []*PyClass{},
		Functions: []*PyFunction{},
	}

	// Generate args TypedDict if inputSchema exists
	argsTypeName := ""
	if tool.InputSchema != nil {
		if inputSchema, ok := tool.InputSchema.(map[string]interface{}); ok && len(inputSchema) > 0 {
			argsTypeName = strutil.ToPascalCase(tool.Name) + "Args"
			argsType, err := g.converter.ConvertSchema(inputSchema, argsTypeName)
			if err != nil {
				return "", fmt.Errorf("failed to convert input schema for %q: %w", tool.Name, err)
			}
			file.Classes = append(file.Classes, argsType)
		}
	}

	// Generate result TypedDict if outputSchema exists
	returnType := strutil.ToPascalCase(tool.Name) + "Result"
	if tool.OutputSchema != nil {
		if outputSchema, ok := tool.OutputSchema.(map[string]interface{}); ok && len(outputSchema) > 0 {
			resultType, err := g.converter.ConvertSchema(outputSchema, returnType)
			if err != nil {
				return "", fmt.Errorf("failed to convert output schema for %q: %w", tool.Name, err)
			}
			file.Classes = append(file.Classes, resultType)
		} else {
			// Empty outputSchema - use Any
			returnType = "Any"
		}
	} else {
		// No outputSchema - use Any
		returnType = "Any"
	}

	// Generate function
	function := &PyFunction{
		Name:         strutil.ToSnakeCase(tool.Name),
		Description:  tool.Description,
		ServerName:   serverName,
		ToolName:     tool.Name,
		ArgsTypeName: argsTypeName,
		ReturnType:   returnType,
		HasArgs:      argsTypeName != "",
	}
	file.Functions = append(file.Functions, function)

	// Collect all generated types (including nested ones)
	g.collectNestedTypes(file)

	return g.renderFile(file), nil
}

// collectNestedTypes collects all nested types and orders them so dependencies come first
func (g *PythonGenerator) collectNestedTypes(file *PyFile) {
	// Build a new ordered list of classes
	orderedClasses := make([]*PyClass, 0, len(file.Classes))
	seen := make(map[string]bool)

	// Process each top-level class
	for _, class := range file.Classes {
		// Add dependencies first (recursively)
		g.addTypeWithDependencies(class, &orderedClasses, seen)
	}

	// Replace with ordered list
	file.Classes = orderedClasses
}

// addTypeWithDependencies adds a type and all its dependencies in the correct order
func (g *PythonGenerator) addTypeWithDependencies(pyClass *PyClass, result *[]*PyClass, seen map[string]bool) {
	if pyClass == nil || pyClass.Name == "" || seen[pyClass.Name] {
		return
	}

	// First, add all dependencies
	g.collectDependencies(pyClass, result, seen)

	// Then add this type
	if !seen[pyClass.Name] {
		*result = append(*result, pyClass)
		seen[pyClass.Name] = true
	}
}

// collectDependencies finds and adds all types that this type depends on
func (g *PythonGenerator) collectDependencies(pyClass *PyClass, result *[]*PyClass, seen map[string]bool) {
	if pyClass == nil {
		return
	}

	// Check all properties for type dependencies
	for _, prop := range pyClass.Properties {
		g.addDependentType(prop.TypeHint, result, seen)
	}
}

// addDependentType adds a dependent type if it's a named type from generatedTypes
func (g *PythonGenerator) addDependentType(typeHint string, result *[]*PyClass, seen map[string]bool) {
	// Extract base type name from complex type hints like "List[Foo]" or "Optional[Foo]"
	typeName := extractBaseTypeName(typeHint)
	if typeName != "" && !seen[typeName] {
		if genType, exists := g.converter.generatedTypes[typeName]; exists {
			g.addTypeWithDependencies(genType, result, seen)
		}
	}
}

// extractBaseTypeName extracts the base type name from a complex type hint
func extractBaseTypeName(typeHint string) string {
	// Simple extraction - look for custom types (PascalCase names)
	// Skip built-in types like str, int, bool, Any, List, Dict, etc.
	parts := strings.FieldsFunc(typeHint, func(r rune) bool {
		return r == '[' || r == ']' || r == ',' || r == ' '
	})

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len(part) > 0 && isCustomType(part) {
			return part
		}
	}
	return ""
}

// isCustomType checks if a type name is a custom type (not a built-in)
func isCustomType(typeName string) bool {
	builtins := map[string]bool{
		"str": true, "int": true, "float": true, "bool": true,
		"Any": true, "Dict": true, "List": true, "Optional": true,
		"Union": true, "None": true,
	}
	return !builtins[typeName]
}

// renderFile renders the complete Python file
func (g *PythonGenerator) renderFile(file *PyFile) string {
	var sb strings.Builder

	// File header
	sb.WriteString(fmt.Sprintf("\"\"\"Generated MCP tool definitions for: %s\n\n", file.ServerName))
	sb.WriteString("This file is auto-generated. Do not edit manually.\n")
	sb.WriteString("\"\"\"\n\n")

	// Imports
	if len(file.Imports) > 0 {
		for _, imp := range file.Imports {
			sb.WriteString(imp)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// TypedDict classes
	for _, class := range file.Classes {
		sb.WriteString(g.renderClass(class))
		sb.WriteString("\n")
	}

	// Functions
	for _, fn := range file.Functions {
		sb.WriteString(g.renderFunction(fn))
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderClass renders a Python TypedDict class
func (g *PythonGenerator) renderClass(c *PyClass) string {
	var sb strings.Builder

	// Class docstring
	if c.Description != "" {
		sb.WriteString(fmt.Sprintf("class %s(TypedDict):\n", c.Name))
		sb.WriteString(fmt.Sprintf("    \"\"\"%s\"\"\"\n", sanitizePythonComment(c.Description)))
	} else {
		sb.WriteString(fmt.Sprintf("class %s(TypedDict):\n", c.Name))
	}

	// Properties
	if len(c.Properties) == 0 {
		sb.WriteString("    pass\n")
	} else {
		for _, prop := range c.Properties {
			if prop.Description != "" {
				sb.WriteString(fmt.Sprintf("    # %s\n", sanitizePythonComment(prop.Description)))
			}
			sb.WriteString(fmt.Sprintf("    %s: %s\n", prop.Name, prop.TypeHint))
		}
	}

	return sb.String()
}

// renderFunction renders a Python async function
func (g *PythonGenerator) renderFunction(fn *PyFunction) string {
	var sb strings.Builder

	// Function signature
	params := ""
	if fn.HasArgs {
		params = fmt.Sprintf("args: %s", fn.ArgsTypeName)
	}

	sb.WriteString(fmt.Sprintf("async def %s(%s) -> %s:\n", fn.Name, params, fn.ReturnType))

	// Docstring
	sb.WriteString("    \"\"\"\n")
	if fn.Description != "" {
		sb.WriteString(fmt.Sprintf("    %s\n", sanitizePythonComment(fn.Description)))
		sb.WriteString("    \n")
	} else {
		sb.WriteString(fmt.Sprintf("    Call tool: %s\n", fn.ToolName))
		sb.WriteString("    \n")
	}

	sb.WriteString("    Returns parsed response - structure depends on tool implementation.\n")
	sb.WriteString("    \"\"\"\n")

	// Function body
	argsValue := "{}"
	if fn.HasArgs {
		argsValue = "args"
	}
	sb.WriteString(fmt.Sprintf("    return await __call_mcp_tool(%q, %q, %s)\n",
		fn.ServerName, fn.ToolName, argsValue))

	return sb.String()
}

// sanitizePythonComment sanitizes a comment string for Python
// It handles multi-line descriptions by prefixing each line with "# "
func sanitizePythonComment(comment string) string {
	// Split on newlines
	lines := strings.Split(comment, "\n")

	// If single line, just return it
	if len(lines) == 1 {
		return comment
	}

	// Multi-line: prefix each line with "# " and join
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}

	return strings.Join(result, "\n    # ")
}

// GenerateServerInitFile generates an __init__.py for a server directory that re-exports all functions
func (g *PythonGenerator) GenerateServerInitFile(serverName string, tools []*mcp.Tool) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("\"\"\"MCP Server Tools: %s\n\n", serverName))
	sb.WriteString(fmt.Sprintf("Generated from MCP server: %s\n", serverName))
	sb.WriteString("This file is auto-generated. Do not edit manually.\n")
	sb.WriteString("\"\"\"\n\n")

	// Import and re-export each function along with its type classes
	for _, tool := range tools {
		moduleName := strutil.ToSnakeCase(tool.Name)
		funcName := strutil.ToSnakeCase(tool.Name)
		argsTypeName := strutil.ToPascalCase(tool.Name) + "Args"
		resultTypeName := strutil.ToPascalCase(tool.Name) + "Result"

		// Build import list for this tool
		imports := []string{funcName}

		// Add Args type if input schema exists
		if tool.InputSchema != nil {
			if inputSchema, ok := tool.InputSchema.(map[string]interface{}); ok && len(inputSchema) > 0 {
				imports = append(imports, argsTypeName)
			}
		}

		// Add Result type if output schema exists
		if tool.OutputSchema != nil {
			if outputSchema, ok := tool.OutputSchema.(map[string]interface{}); ok && len(outputSchema) > 0 {
				imports = append(imports, resultTypeName)
			}
		}

		// Generate import statement
		sb.WriteString(fmt.Sprintf("from .%s import %s\n", moduleName, strings.Join(imports, ", ")))
	}

	// Generate __all__ list with functions and types
	sb.WriteString("\n__all__ = [\n")
	allExports := []string{}
	for _, tool := range tools {
		funcName := strutil.ToSnakeCase(tool.Name)
		argsTypeName := strutil.ToPascalCase(tool.Name) + "Args"
		resultTypeName := strutil.ToPascalCase(tool.Name) + "Result"

		// Always export the function
		allExports = append(allExports, funcName)

		// Export Args type if input schema exists
		if tool.InputSchema != nil {
			if inputSchema, ok := tool.InputSchema.(map[string]interface{}); ok && len(inputSchema) > 0 {
				allExports = append(allExports, argsTypeName)
			}
		}

		// Export Result type if output schema exists
		if tool.OutputSchema != nil {
			if outputSchema, ok := tool.OutputSchema.(map[string]interface{}); ok && len(outputSchema) > 0 {
				allExports = append(allExports, resultTypeName)
			}
		}
	}

	for i, exportName := range allExports {
		sb.WriteString(fmt.Sprintf("    %q", exportName))
		if i < len(allExports)-1 {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("]\n")

	return sb.String()
}
