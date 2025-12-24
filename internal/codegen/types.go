package codegen

// TSType represents a TypeScript type definition
type TSType struct {
	Name        string       // Interface/type name (e.g., "GetMeArgs")
	Kind        string       // "interface" | "type" | "primitive" | "array" | "union"
	Properties  []TSProperty // For objects/interfaces
	ElementType *TSType      // For arrays
	UnionTypes  []*TSType    // For unions
	IsOptional  bool         // Whether this type is optional
	Description string       // JSDoc comment
	RawType     string       // For primitives: "string", "number", "boolean", etc.
}

// TSProperty represents a property in a TypeScript interface
type TSProperty struct {
	Name        string  // Property name
	Type        *TSType // Property type
	IsOptional  bool    // Whether property is optional
	Description string  // JSDoc comment
}

// TSFunction represents a generated TypeScript function
type TSFunction struct {
	Name         string // Function name (camelCase)
	Description  string // JSDoc comment
	ServerName   string // MCP server name
	ToolName     string // Original tool name
	ArgsTypeName string // TypeScript args interface name (or "" if no args)
	ReturnType   string // TypeScript return type
	HasArgs      bool   // Whether function takes arguments
}

// TSFile represents a complete TypeScript file to be generated
type TSFile struct {
	ServerName string        // Name of the MCP server
	Imports    []string      // Import statements
	Interfaces []*TSType     // Type/interface definitions
	Functions  []*TSFunction // Function definitions
}
