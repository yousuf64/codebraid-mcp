package codegen

// PyClass represents a Python TypedDict class definition
type PyClass struct {
	Name        string       // Class name (e.g., "GetMeArgs")
	Properties  []PyProperty // TypedDict properties
	Description string       // Docstring comment
}

// PyProperty represents a property in a Python TypedDict
type PyProperty struct {
	Name        string // Property name
	TypeHint    string // Python type hint (e.g., "str", "int", "Optional[str]", "List[int]")
	Description string // Comment
}

// PyFunction represents a generated Python async function
type PyFunction struct {
	Name         string // Function name (snake_case)
	Description  string // Docstring
	ServerName   string // MCP server name
	ToolName     string // Original tool name
	ArgsTypeName string // Python TypedDict name (or "" if no args)
	ReturnType   string // Python return type hint
	HasArgs      bool   // Whether function takes arguments
}

// PyFile represents a complete Python file to be generated
type PyFile struct {
	ServerName string        // Name of the MCP server
	Imports    []string      // Import statements
	Classes    []*PyClass    // TypedDict definitions
	Functions  []*PyFunction // Function definitions
}
