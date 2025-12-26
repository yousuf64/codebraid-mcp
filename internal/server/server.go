package server

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/codebraid-mcp/internal/bundler"
	"github.com/yousuf/codebraid-mcp/internal/sandbox"
	"github.com/yousuf/codebraid-mcp/internal/session"
)

// ExecuteCodeArgs represents the arguments for the execute_code tool
type ExecuteCodeArgs struct {
	Code string `json:"code" jsonschema:"TypeScript code to execute in sandbox"`
}

// ListDirectoryArgs represents the arguments for the list_directory tool
type ListDirectoryArgs struct {
	Path             string `json:"path" jsonschema:"Path to directory (e.g., '/', '/servers', '/servers/github'). Defaults to '/' if not provided."`
	WithDescriptions bool   `json:"withDescriptions,omitempty" jsonschema:"Include descriptions for functions (default: false)"`
}

// ReadFileArgs represents the arguments for the read_file tool
type ReadFileArgs struct {
	Path string `json:"path" jsonschema:"Required. Path to file in virtual filesystem (e.g., '/servers/github/listRepos.ts', '/servers/github/index.ts', '/servers/mcp-types.ts')"`
}

// NewMcpServer creates and configures the MCP server
func NewMcpServer(sessionMgr *session.Manager) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "codebraid-mcp",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: `
TypeScript Code Execution with Virtual Filesystem

CodeBraid provides a virtual filesystem where MCP servers are translated into TypeScript libraries.
All files are accessible via absolute paths starting with '/'.

Virtual Filesystem Structure:
/
├── servers/
│   ├── github/              (MCP server → TypeScript)
│   │   ├── listRepos.ts
│   │   ├── getIssues.ts
│   │   └── index.ts
│   ├── filesystem/
│   │   ├── readFile.ts
│   │   ├── writeFile.ts
│   │   └── index.ts
│   └── mcp-types.ts
└── (future: workspace/, config/, etc.)

RECOMMENDED IMPORT PATTERN:
Use namespace imports with absolute paths:

    import * as github from './servers/github';
    import * as gdrive from './servers/google-drive';
    import * as slack from './servers/slack';
    import * as filesystem from './servers/filesystem';
    
    export async function exec() {
        const repos = await github.listRepos({ owner: "octocat" });
        const file = await filesystem.readFile({ path: "./config.json" });
        await sendMessage({ channel: "#dev", text: "Found " + repos.length + " repos" });
		return { repos, file };
    }

Available Tools:
1. "list_directory" - List contents of any directory in the virtual filesystem
2. "read_file" - Read any file by absolute path
3. "execute_code" - Execute TypeScript code with automatic bundling

Recommended Workflow:
1. Call list_directory({ path: "/servers" }) to see available MCP servers
2. Read server index: read_file({ path: "/servers/github/index.ts" })
3. Or list specific server functions: list_directory({ path: "/servers/github" })
4. Read specific functions: read_file({ path: "/servers/github/listRepos.ts" })
5. Write your TypeScript code using namespace imports
6. Your code must have an "exec()" function as the entry point
7. Call execute_code with your complete code

Notes:
- All paths are absolute and start with '/'
- Use namespace imports (import * as) for best experience
- Each function file has inline types for arguments and return values
- All imports are automatically bundled before execution
- The exec() function is required and serves as your code's entry point
- Execution timeout: 30 seconds
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
    import * as github from './servers/github';
    import * as filesystem from './servers/filesystem';
    
    interface ExecResult {
        totalRepos: number;
        readmeLength: number;
    }
    
    async function exec(): Promise<ExecResult> {
        const repos = await github.listRepos({ owner: "octocat" });
        const readme = await filesystem.readFile({ path: "./README.md" });
        
        return {
            totalRepos: repos.length,
            readmeLength: readme.length
        };
    }

Runtime Environment:
- Imports from './servers/*' are bundled automatically
- Use namespace imports (import * as) for best experience
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

	// Register list_directory tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_directory",
		Description: "List contents of a directory in the virtual filesystem. Returns directories and files with their types.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListDirectoryArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		var output bytes.Buffer

		// Normalize path
		path := strings.TrimPrefix(args.Path, "/")
		path = strings.TrimSuffix(path, "/")

		if path == "" {
			// Root directory
			output.WriteString("/\n")
			output.WriteString("└── servers/ (MCP servers)\n")
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: output.String()},
				},
			}, nil, nil
		}

		if path == "servers" {
			// List all MCP servers
			allTools := sessionCtx.ClientHub.Tools()
			output.WriteString("/servers/\n")

			serverCount := 0
			for svr, toolList := range allTools {
				serverCount++
				prefix := "├──"
				if serverCount == len(allTools) {
					prefix = "├──"
				}
				output.WriteString(fmt.Sprintf("%s %s/ (%d functions)\n", prefix, svr, len(toolList)))
			}
			output.WriteString("└── mcp-types.ts\n")

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: output.String()},
				},
			}, nil, nil
		}

		if strings.HasPrefix(path, "servers/") {
			// List specific server directory
			serverName := strings.TrimPrefix(path, "servers/")
			tools, ok := sessionCtx.ClientHub.ServerTools(serverName)
			if !ok {
				availableServers := sessionCtx.ClientHub.Servers()
				return nil, nil, fmt.Errorf("directory '/servers/%s/' not found. Available servers: %v",
					serverName, availableServers)
			}

			output.WriteString(fmt.Sprintf("/servers/%s/\n", serverName))
			for i, tool := range tools {
				prefix := "├──"
				if i == len(tools)-1 {
					prefix = "├──"
				}

				funcName := toCamelCase(tool.Name)
				if args.WithDescriptions && tool.Description != "" {
					output.WriteString(fmt.Sprintf("%s %s.ts - %s\n", prefix, funcName, tool.Description))
				} else {
					output.WriteString(fmt.Sprintf("%s %s.ts\n", prefix, funcName))
				}
			}
			output.WriteString("└── index.ts\n")
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: output.String()},
				},
			}, nil, nil
		}

		return nil, nil, fmt.Errorf("directory '/%s' not found", path)
	})

	// Register read_file tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_file",
		Description: "Read a file from the virtual filesystem using absolute paths (e.g., '/servers/github/listRepos.ts', '/servers/github/index.ts', '/servers/mcp-types.ts').",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ReadFileArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		// Normalize path - remove leading slash
		path := strings.TrimPrefix(args.Path, "/")

		// Construct the actual file path in the bundle directory
		filePath := fmt.Sprintf("%s/%s", sessionCtx.BundleDir, path)

		// Read the file from disk
		content, err := os.ReadFile(filePath)
		if err != nil {
			return nil, nil, fmt.Errorf("file '/%s' not found", path)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(content)},
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
