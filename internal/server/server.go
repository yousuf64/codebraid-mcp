package server

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/codebraid-mcp/internal/bundler"
	"github.com/yousuf/codebraid-mcp/internal/codegen"
	"github.com/yousuf/codebraid-mcp/internal/sandbox"
	"github.com/yousuf/codebraid-mcp/internal/session"
)

// ExecuteCodeArgs represents the arguments for the execute_code tool
type ExecuteCodeArgs struct {
	Code string `json:"code" jsonschema:"TypeScript code to execute in sandbox"`
}

// ListLibFilesArgs represents the arguments for the list_lib_files tool
type ListLibFilesArgs struct {
	WithDescriptions bool `json:"withDescriptions,omitempty" jsonschema:"Include descriptions of what each library does (default: false)"`
	WithFunctions    bool `json:"withFunctions,omitempty" jsonschema:"Include list of exported functions in each library (default: false)"`
}

// ReadLibFileArgs represents the arguments for the read_lib_file tool
type ReadLibFileArgs struct {
	FileName string `json:"fileName" jsonschema:"Required. Name of the .ts file in ./lib directory (e.g., 'github.ts' or just 'github')"`
}

// InspectFunctionArgs represents the arguments for the inspect_function tool
type InspectFunctionArgs struct {
	FileName     string `json:"fileName" jsonschema:"Required. Library file name (e.g., 'github.ts')"`
	FunctionName string `json:"functionName" jsonschema:"Required. Function to inspect in camelCase (e.g., 'listRepos'). Also accepts snake_case or PascalCase - will be normalized automatically."`
}

// NewMcpServer creates and configures the MCP server
func NewMcpServer(sessionMgr *session.Manager) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "codebraid-mcp",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: `
TypeScript Code Execution Environment with MCP Tool Libraries

The "./lib" directory contains pre-written TypeScript libraries that wrap downstream MCP tools.
Each library corresponds to one MCP server (e.g., "./lib/github.ts", "./lib/slack.ts", "./lib/filesystem.ts").

Available Tools:
1. "list_lib_files" - List all available .ts libraries in the ./lib directory (shows function counts)
2. "read_lib_file" - Read the contents of a .ts file from ./lib to see available functions and types
3. "inspect_function" - View a specific function's signature and documentation from a library
4. "execute_code" - Execute TypeScript code with imports from ./lib (code is bundled automatically)

Recommended Workflow:
1. Call "list_lib_files" (optionally with withFunctions: true) to see what libraries are available
2. For libraries with few functions (<15), call "read_lib_file" to see the complete implementation
3. For libraries with many functions (≥15), use "inspect_function" to view specific functions you need
4. Write your TypeScript code importing from "./lib/[name].ts"
5. Your code must have an "exec()" function as the entry point
6. Call "execute_code" with your complete code

Example Code Structure:
    import { listRepos, ListReposArgs } from "./lib/github.ts";
    import { sendMessage } from "./lib/slack.ts";
    
    export async function exec() {
        const repos = await listRepos({ owner: "modelcontextprotocol" });
        await sendMessage({ channel: "#dev", text: "Found " + repos.length + " repos" });
        return { success: true, repoCount: repos.length };
    }

Notes:
- All imports from "./lib/*.ts" are automatically bundled before execution
- The exec() function is required and serves as your code's entry point
- You can import and use multiple libraries in the same code
- TypeScript types provide autocomplete and type safety
- Execution timeout: 30 seconds
- Tip: Use list_lib_files with withFunctions: true to gauge library size before reading
`,
	})

	server.AddReceivingMiddleware(createSessionInjectionMiddleware(sessionMgr))
	server.AddReceivingMiddleware(createLoggingMiddleware())

	// Register execute_code tool
	mcp.AddTool(server, &mcp.Tool{
		Name: "execute_code",
		Description: `Execute TypeScript code in a sandboxed environment.

REQUIRED: Your code must define a function named "exec()" as the entry point.

Basic structure:
    async function exec() {
        // Your code here
        return result;
    }

The exec() function:
- Can be async or sync
- Can return any JSON-serializable value
- Should have a strong return type (highly recommended for type safety)
- Is automatically called when your code executes
- Does not need to be exported

Complete Example with Strong Typing:
    import { listRepos } from "./lib/github.ts";
    import { readFile } from "./lib/filesystem.ts";
    
    interface ExecResult {
        totalRepos: number;
        readmeLength: number;
    }
    
    async function exec(): Promise<ExecResult> {
        const repos = await listRepos({ owner: "octocat" });
        const readme = await readFile({ path: "./README.md" });
        
        return {
            totalRepos: repos.length,
            readmeLength: readme.length
        };
    }

Runtime Environment:
- Imports from "./lib/*.ts" are bundled automatically
- Execution timeout: 30 seconds
- No access to Node.js built-ins or filesystem
- No access to DOM or browser APIs
`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ExecuteCodeArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		// Step 1: Bundle the code using session's bundle directory
		b, err := bundler.New()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create bundler: %w", err)
		}

		codeWithCaller := fmt.Sprintf(`%s
exec();
`, args.Code)
		bundledCode, sourceMap, err := b.BundleWithSession(sessionCtx.BundleDir, codeWithCaller)
		if err != nil {
			return nil, nil, fmt.Errorf("bundling failed: %w", err)
		}

		// Step 2: Create sandbox
		sb, err := sandbox.NewSandbox(ctx, "./wasm/dist/sandbox.wasm", sessionCtx.ClientHub)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create sandbox: %w", err)
		}
		defer sb.Close()

		// Step 3: Execute bundled code
		result, err := sb.ExecuteCode(bundledCode, sourceMap)
		if err != nil {
			return nil, nil, fmt.Errorf("execution failed: %w", err)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: result},
			},
		}, nil, nil
	})

	// Register list_lib_files tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_lib_files",
		Description: "List all available TypeScript library files in the ./lib directory",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListLibFilesArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		var output bytes.Buffer

		allTools := sessionCtx.ClientHub.Tools()

		for svr, toolList := range allTools {
			output.WriteString(fmt.Sprintf("%s.ts (%d functions)\n", svr, len(toolList)))

			for i, tool := range toolList {
				prefix := "├──"
				if i == len(toolList)-1 {
					prefix = "└──"
				}

				funcName := toCamelCase(tool.Name)
				if (args.WithDescriptions || args.WithFunctions) && tool.Description != "" {
					output.WriteString(fmt.Sprintf("%s %s() - %s\n", prefix, funcName, tool.Description))
				} else {
					output.WriteString(fmt.Sprintf("%s %s()\n", prefix, funcName))
				}
			}
			output.WriteString("\n")
		}

		// Add mcp-types.ts at the end
		output.WriteString("mcp-types.ts\n")
		output.WriteString("└── (Common types and utilities)\n")

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: output.String()},
			},
		}, nil, nil
	})

	// Register read_lib_file tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_lib_file",
		Description: "Read the contents of a TypeScript library file from the ./lib directory",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ReadLibFileArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		// Normalize filename - remove .ts extension if present
		fileName := strings.TrimSuffix(args.FileName, ".ts")

		tsgen := codegen.NewTypeScriptGenerator()
		var fileContent string

		if fileName == "mcp-types" {
			fileContent = tsgen.GenerateMCPTypesFile()
		} else {
			// Get tools from the server
			tools, ok := sessionCtx.ClientHub.ServerTools(fileName)
			if !ok {
				// Maintain file metaphor in error message
				availableServers := sessionCtx.ClientHub.Servers()
				availableFiles := make([]string, len(availableServers))
				for i, s := range availableServers {
					availableFiles[i] = s + ".ts"
				}
				return nil, nil, fmt.Errorf("library file '%s.ts' not found in ./lib directory. Available files: %v",
					fileName, append(availableFiles, "mcp-types.ts"))
			}

			fileContent, err = tsgen.GenerateFile(fileName, tools)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to read library file: %w", err)
			}
		}

		// Return TypeScript content directly (not wrapped in JSON)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fileContent},
			},
		}, nil, nil
	})

	// Register inspect_function tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "inspect_function",
		Description: "View a specific function's signature, arguments, and return type from a library file. Function names are automatically normalized (e.g., 'list_repos', 'listRepos', or 'ListRepos' all work).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args InspectFunctionArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		// Normalize filename - remove .ts extension if present
		fileName := strings.TrimSuffix(args.FileName, ".ts")

		// Special case for mcp-types
		if fileName == "mcp-types" {
			return nil, nil, fmt.Errorf("mcp-types.ts contains only type definitions, not callable functions. Use 'read_lib_file' to view its contents")
		}

		// Get tools for this server
		tools, ok := sessionCtx.ClientHub.ServerTools(fileName)
		if !ok {
			// Maintain file metaphor in error message
			availableServers := sessionCtx.ClientHub.Servers()
			availableFiles := make([]string, len(availableServers))
			for i, s := range availableServers {
				availableFiles[i] = s + ".ts"
			}
			return nil, nil, fmt.Errorf("library file '%s.ts' not found in ./lib directory. Available files: %v",
				fileName, availableFiles)
		}

		// Find the specific function
		// Normalize both the requested name and tool names to camelCase for comparison
		// This allows users to provide: "list_repos", "listRepos", or "ListRepos"
		// Tool definitions come from MCP servers in snake_case (e.g., "list_repos")
		requestedFuncName := toCamelCase(args.FunctionName)
		var foundTool *mcp.Tool
		for i, tool := range tools {
			if toCamelCase(tool.Name) == requestedFuncName {
				foundTool = tools[i]
				break
			}
		}

		if foundTool == nil {
			// List available functions
			availableFuncs := make([]string, len(tools))
			for i, t := range tools {
				availableFuncs[i] = toCamelCase(t.Name)
			}
			return nil, nil, fmt.Errorf("function '%s' not found in %s.ts. Available functions: %v",
				requestedFuncName, fileName, availableFuncs)
		}

		// Generate TypeScript for just this one function using the existing generator
		tsgen := codegen.NewTypeScriptGenerator()
		functionContent, err := tsgen.GenerateFile(fileName, []*mcp.Tool{foundTool})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to generate function signature: %w", err)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: functionContent},
			},
		}, nil, nil
	})

	return server
}

// TODO: Extract to util
// toPascalCase converts a string to PascalCase
func toPascalCase(s string) string {
	// Split by underscore, dash, or space
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == '_' || r == '-' || r == ' '
	})

	for i, part := range parts {
		if len(part) > 0 {
			parts[i] = strings.ToUpper(part[0:1]) + part[1:]
		}
	}

	return strings.Join(parts, "")
}

// TODO: Extract to util
// toCamelCase converts a string to camelCase
// Handles: snake_case, kebab-case, space-separated, PascalCase, and already-camelCase
func toCamelCase(s string) string {
	if len(s) == 0 {
		return s
	}

	// If already camelCase or PascalCase (no underscores/dashes/spaces), just ensure first char is lowercase
	if !strings.ContainsAny(s, "_- ") {
		return strings.ToLower(s[0:1]) + s[1:]
	}

	// Otherwise, convert from snake_case/kebab-case/space-separated to camelCase
	pascal := toPascalCase(s)
	if len(pascal) == 0 {
		return pascal
	}
	return strings.ToLower(pascal[0:1]) + pascal[1:]
}
