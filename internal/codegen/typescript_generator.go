package codegen

import (
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TypeScriptGenerator generates TypeScript files from tool definitions
type TypeScriptGenerator struct {
	converter *SchemaConverter
}

// NewTypeScriptGenerator creates a new TypeScript generator
func NewTypeScriptGenerator() *TypeScriptGenerator {
	return &TypeScriptGenerator{
		converter: NewSchemaConverter(),
	}
}

// GenerateFile generates a complete TypeScript file for a server's tools
func (g *TypeScriptGenerator) GenerateFile(serverName string, tools []*mcp.Tool) (string, error) {
	if len(tools) == 0 {
		return "", fmt.Errorf("no tools provided for server %q", serverName)
	}

	// Reset converter for each file to avoid type name collisions across files
	g.converter = NewSchemaConverter()

	file := &TSFile{
		ServerName: serverName,
		Imports:    []string{},
		Interfaces: []*TSType{},
		Functions:  []*TSFunction{},
	}

	// Track if we need to import MCP types
	needsMCPTypes := false

	// Process each tool
	for _, tool := range tools {
		// Generate args interface if inputSchema exists
		argsTypeName := ""
		if tool.InputSchema != nil {
			// Type assert to map[string]interface{} for schema conversion
			if inputSchema, ok := tool.InputSchema.(map[string]interface{}); ok && len(inputSchema) > 0 {
				argsTypeName = toPascalCase(tool.Name) + "Args"
				argsType, err := g.converter.ConvertSchema(inputSchema, argsTypeName)
				if err != nil {
					return "", fmt.Errorf("failed to convert input schema for %q: %w", tool.Name, err)
				}
				file.Interfaces = append(file.Interfaces, argsType)
			}
		}

		// Generate result interface if outputSchema exists
		returnType := "CallToolResult"
		if tool.OutputSchema != nil {
			// Type assert to map[string]interface{} for schema conversion
			if outputSchema, ok := tool.OutputSchema.(map[string]interface{}); ok && len(outputSchema) > 0 {
				resultTypeName := toPascalCase(tool.Name) + "Result"
				resultType, err := g.converter.ConvertSchema(outputSchema, resultTypeName)
				if err != nil {
					return "", fmt.Errorf("failed to convert output schema for %q: %w", tool.Name, err)
				}
				file.Interfaces = append(file.Interfaces, resultType)
				returnType = resultTypeName
			} else {
				// OutputSchema present but not a valid map, use default
				needsMCPTypes = true
			}
		} else {
			// No outputSchema, use default MCP type
			needsMCPTypes = true
		}

		// Generate function
		function := &TSFunction{
			Name:         toCamelCase(tool.Name),
			Description:  tool.Description,
			ServerName:   serverName,
			ToolName:     tool.Name,
			ArgsTypeName: argsTypeName,
			ReturnType:   returnType,
			HasArgs:      argsTypeName != "",
		}
		file.Functions = append(file.Functions, function)
	}

	// Collect all generated types (including nested ones)
	g.collectNestedTypes(file)

	// Add imports if needed
	if needsMCPTypes {
		file.Imports = append(file.Imports, "import type { CallToolResult } from './mcp-types';")
	}

	return g.renderFile(file), nil
}

// collectNestedTypes collects all nested types and orders them so dependencies come first
func (g *TypeScriptGenerator) collectNestedTypes(file *TSFile) {
	// Build a new ordered list of interfaces
	orderedInterfaces := make([]*TSType, 0, len(file.Interfaces))
	seen := make(map[string]bool)

	// Process each top-level interface
	for _, iface := range file.Interfaces {
		// Add dependencies first (recursively)
		g.addTypeWithDependencies(iface, &orderedInterfaces, seen)
	}

	// Replace with ordered list
	file.Interfaces = orderedInterfaces
}

// addTypeWithDependencies adds a type and all its dependencies in the correct order
func (g *TypeScriptGenerator) addTypeWithDependencies(tsType *TSType, result *[]*TSType, seen map[string]bool) {
	if tsType == nil || tsType.Name == "" || seen[tsType.Name] {
		return
	}

	// First, add all dependencies
	g.collectDependencies(tsType, result, seen)

	// Then add this type
	if !seen[tsType.Name] {
		*result = append(*result, tsType)
		seen[tsType.Name] = true
	}
}

// collectDependencies finds and adds all types that this type depends on
func (g *TypeScriptGenerator) collectDependencies(tsType *TSType, result *[]*TSType, seen map[string]bool) {
	if tsType == nil {
		return
	}

	switch tsType.Kind {
	case "interface":
		// Check all properties for type dependencies
		for _, prop := range tsType.Properties {
			g.addDependentType(prop.Type, result, seen)
		}

	case "array":
		// Array element type might be a named type
		g.addDependentType(tsType.ElementType, result, seen)

	case "union":
		// Union members might be named types
		for _, unionType := range tsType.UnionTypes {
			g.addDependentType(unionType, result, seen)
		}
	}
}

// addDependentType adds a dependent type if it's a named type from generatedTypes
func (g *TypeScriptGenerator) addDependentType(tsType *TSType, result *[]*TSType, seen map[string]bool) {
	if tsType == nil {
		return
	}

	// If this is a named type, look it up in generatedTypes and add it
	if tsType.Name != "" && !seen[tsType.Name] {
		if genType, exists := g.converter.generatedTypes[tsType.Name]; exists {
			g.addTypeWithDependencies(genType, result, seen)
		}
	}

	// Recursively check nested types
	g.collectDependencies(tsType, result, seen)
}

// renderFile renders the complete TypeScript file
func (g *TypeScriptGenerator) renderFile(file *TSFile) string {
	var sb strings.Builder

	// File header
	sb.WriteString(fmt.Sprintf("/**\n * Generated MCP tool definitions for: %s\n", file.ServerName))
	sb.WriteString(" * This file is auto-generated. Do not edit manually.\n")
	sb.WriteString(" */\n\n")

	// Imports
	if len(file.Imports) > 0 {
		for _, imp := range file.Imports {
			sb.WriteString(imp)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Interfaces
	for _, iface := range file.Interfaces {
		sb.WriteString(g.renderType(iface))
		sb.WriteString("\n")
	}

	// Functions
	for _, fn := range file.Functions {
		sb.WriteString(g.renderFunction(fn))
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderType renders a TypeScript type/interface
func (g *TypeScriptGenerator) renderType(t *TSType) string {
	var sb strings.Builder

	// JSDoc comment
	if t.Description != "" {
		sb.WriteString("/**\n")
		sb.WriteString(fmt.Sprintf(" * %s\n", sanitizeComment(t.Description)))
		sb.WriteString(" */\n")
	}

	switch t.Kind {
	case "interface":
		sb.WriteString(fmt.Sprintf("export interface %s {\n", t.Name))
		for _, prop := range t.Properties {
			if prop.Description != "" {
				sb.WriteString(fmt.Sprintf("  /** %s */\n", sanitizeComment(prop.Description)))
			}
			optional := ""
			if prop.IsOptional {
				optional = "?"
			}
			sb.WriteString(fmt.Sprintf("  %s%s: %s;\n", prop.Name, optional, g.converter.typeToString(prop.Type)))
		}
		sb.WriteString("}\n")

	case "type":
		sb.WriteString(fmt.Sprintf("export type %s = %s;\n", t.Name, t.RawType))

	case "union":
		parts := make([]string, len(t.UnionTypes))
		for i, ut := range t.UnionTypes {
			parts[i] = g.converter.typeToString(ut)
		}
		sb.WriteString(fmt.Sprintf("export type %s = %s;\n", t.Name, strings.Join(parts, " | ")))
	}

	return sb.String()
}

// renderFunction renders a TypeScript function
func (g *TypeScriptGenerator) renderFunction(fn *TSFunction) string {
	var sb strings.Builder

	// JSDoc comment
	sb.WriteString("/**\n")
	if fn.Description != "" {
		sb.WriteString(fmt.Sprintf(" * %s\n", sanitizeComment(fn.Description)))
	} else {
		sb.WriteString(fmt.Sprintf(" * Call tool: %s\n", fn.ToolName))
	}

	// Add note if using default MCP type
	if fn.ReturnType == "CallToolResult" {
		sb.WriteString(" * \n")
		sb.WriteString(" * Note: Returns CallToolResult because no outputSchema is defined.\n")
		sb.WriteString(" * You may need to parse the content to extract the actual result.\n")
	}

	sb.WriteString(" */\n")

	// Function signature
	params := ""
	if fn.HasArgs {
		params = fmt.Sprintf("args: %s", fn.ArgsTypeName)
	}

	sb.WriteString(fmt.Sprintf("export async function %s(%s): Promise<%s> {\n",
		fn.Name, params, fn.ReturnType))

	// Function body
	argsValue := "{}"
	if fn.HasArgs {
		argsValue = "args"
	}
	sb.WriteString(fmt.Sprintf("  return await callTool(%q, %q, %s);\n",
		fn.ServerName, fn.ToolName, argsValue))

	sb.WriteString("}\n")

	return sb.String()
}

// GenerateIndexFile generates an index.ts that re-exports all servers
func (g *TypeScriptGenerator) GenerateIndexFile(serverNames []string) string {
	var sb strings.Builder

	sb.WriteString("/**\n")
	sb.WriteString(" * Generated MCP tool index\n")
	sb.WriteString(" * This file is auto-generated. Do not edit manually.\n")
	sb.WriteString(" */\n\n")

	// Export MCP types
	sb.WriteString("export * from './mcp-types';\n\n")

	// Export each server
	for _, serverName := range serverNames {
		sb.WriteString(fmt.Sprintf("export * as %s from './%s';\n", serverName, serverName))
	}

	return sb.String()
}

// GenerateMCPTypesFile generates the mcp-types.ts file
func (g *TypeScriptGenerator) GenerateMCPTypesFile() string {
	return `/**
 * MCP Protocol Types
 * 
 * These types represent the standard MCP (Model Context Protocol) response types.
 * They are used as default return types when tools don't specify an outputSchema.
 */

/**
 * Result of a tool call
 */
export interface CallToolResult {
  /**
   * A list of content objects that represent the result of the tool call
   */
  content: Content[];

  /**
   * Optional structured result of the tool call
   */
  structuredContent?: any;

  /**
   * Whether the tool call ended in an error
   */
  isError?: boolean;
}

/**
 * Content types that can be returned by tools
 */
export type Content = TextContent | ImageContent | ResourceContent;

/**
 * Text content
 */
export interface TextContent {
  type: "text";
  text: string;
}

/**
 * Image content (base64 encoded)
 */
export interface ImageContent {
  type: "image";
  data: string;
  mimeType: string;
}

/**
 * Resource reference content
 */
export interface ResourceContent {
  type: "resource";
  resource: {
    uri: string;
    mimeType?: string;
    text?: string;
    blob?: string;
  };
}

/**
 * Helper function to extract text from CallToolResult
 */
export function extractText(result: CallToolResult): string {
  const textContent = result.content.find(c => c.type === "text") as TextContent | undefined;
  return textContent?.text || "";
}

/**
 * Helper function to extract JSON from CallToolResult
 */
export function extractJSON<T = any>(result: CallToolResult): T {
  if (result.structuredContent) {
    return result.structuredContent as T;
  }
  
  const text = extractText(result);
  if (text) {
    try {
      return JSON.parse(text) as T;
    } catch {
      throw new Error("Failed to parse JSON from text content");
    }
  }
  
  throw new Error("No structured content or text content found");
}

/**
 * Helper function to check if result is an error
 */
export function isErrorResult(result: CallToolResult): boolean {
  return result.isError === true;
}
`
}

// sanitizeComment escapes or removes problematic content from JSDoc comments
func sanitizeComment(comment string) string {
	// Replace */ with *\/ to avoid breaking JSDoc comments
	comment = strings.ReplaceAll(comment, "*/", `*\/`)

	// Replace /* with /\* to avoid nested comments
	comment = strings.ReplaceAll(comment, "/*", `/\*`)

	return comment
}
