package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/codebraid-mcp/internal/codegen"
	"github.com/yousuf/codebraid-mcp/internal/sandbox"
	"github.com/yousuf/codebraid-mcp/internal/session"
)

// ExecuteCodeArgs represents the arguments for the execute_code tool
type ExecuteCodeArgs struct {
	Code string `json:"code" jsonschema:"TypeScript code to execute in sandbox"`
}

// GetMcpToolsArgs represents the arguments for the get_mcp_tools tool
type GetMcpToolsArgs struct {
	WithDescription bool `json:"withDescription" jsonschema:"Include tool descriptions"`
}

// GetMcpToolDetailsArgs represents the arguments for the get_mcp_tool_details tool
type GetMcpToolDetailsArgs struct {
	ServerName string `json:"serverName" jsonschema:"Required server name"`
	ToolName   string `json:"toolName" jsonschema:"Name of the tool to get details for"`
}

// GetMcpFileArgs represents the arguments for the get_mcp_file tool
type GetMcpFileArgs struct {
	FileName string `json:"fileName" jsonschema:"Required file name"`
}

// NewMCPServer creates and configures the MCP server
func NewMCPServer(sessionMgr *session.Manager) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "codebraid-mcp",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		//Instructions: "Supercharging AI agents with a TypeScript code execution environment. Use execute_code` to execute TypeScript code. The execution environment provides access to tool execution through the function API `callTool(serverName: string, toolName: string, args: { [key: string]: any }) => Promise<any>`",
		Instructions: `
Supercharge workflows with a TypeScript sandbox. The sandbox exports capabilities using "*.ts" files in the "./lib" directory. 
Use "get_files" to list files in the "lib" directory.
Use "read_file" to read a file in the "lib" directory.
Use "execute_code" to execute a TypeScript script. The script must contain an "exec(): T | CallToolResult method. You are able to import functions and types from the lib directory.
CallToolResult is a generic interface that is exported from "./lib/mcp-types.ts" when the tool doesn't have a strong return type.

Before calling "execute_code", use "read_file" to get a better idea on the exporting types and functions.
`,
	})

	server.AddReceivingMiddleware(createSessionInjectionMiddleware(sessionMgr))
	server.AddReceivingMiddleware(createLoggingMiddleware())

	// Register execute_code tool
	mcp.AddTool(server, &mcp.Tool{
		Name: "execute_code",
		Description: `Execute TypeScript code in a sandbox with access to libs. Imports must end with the file extension.

		Example code:
			import { CallToolResult } from "./lib/mcp-types.ts";
			import { viewProfile, ViewProfileResult } from "./lib/slack.ts";
	
			async function exec(): Promise<ViewProfileResult> {
				return await viewProfile({ withMetadata: true });
			}
`,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ExecuteCodeArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		// Create sandbox with request context (respects request cancellation)
		sb, err := sandbox.NewSandbox(ctx, "./wasm/dist/sandbox.wasm", sessionCtx.ClientHub)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create sandbox: %w", err)
		}
		defer sb.Close()

		// Execute code
		result, err := sb.ExecuteCode(args.Code)
		if err != nil {
			return nil, nil, fmt.Errorf("execution failed: %w", err)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: result},
			},
		}, nil, nil
	})

	// Register get_mcp_tools tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_files",
		Description: "List all files in the `lib` directory",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args GetMcpToolsArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		var toolsJSON []byte
		var toolsTreeBuf = bytes.Buffer{}

		if args.WithDescription {
			tools := sessionCtx.ClientHub.GetToolsWithDescription()
			toolsJSON, err = json.MarshalIndent(tools, "", "  ")

			for svr, toolList := range tools {
				toolsTreeBuf.WriteString(fmt.Sprintf("%s.ts\n", svr))

				for _, tool := range toolList {
					toolsTreeBuf.WriteString(fmt.Sprintf("└──%s() # %s\n", toCamelCase(tool.Name), tool.Description))
				}
			}
		} else {
			tools := sessionCtx.ClientHub.ListTools()
			// Convert to simpler format (just tool names)
			simpleTools := make(map[string][]string)
			for server, toolList := range tools {
				names := make([]string, len(toolList))
				for i, tool := range toolList {
					names[i] = tool.Name
				}
				simpleTools[server] = names
			}
			toolsJSON, err = json.MarshalIndent(simpleTools, "", "  ")

			for svr, toolList := range tools {
				toolsTreeBuf.WriteString(fmt.Sprintf("%s.ts\n", svr))

				for _, tool := range toolList {
					toolsTreeBuf.WriteString(fmt.Sprintf("└──%s()\n", toCamelCase(tool.Name)))
				}
			}
		}

		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal tools: %w", err)
		}

		_ = toolsJSON
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: toolsTreeBuf.String()},
			},
		}, nil, nil
	})

	// Register get_mcp_file tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_file",
		Description: "Read a file from `lib` directory",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args GetMcpFileArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		intr := codegen.NewIntrospector(sessionCtx.ClientHub)
		if args.FileName[len(args.FileName)-3:] == ".ts" {
			args.FileName = args.FileName[:len(args.FileName)-3]
		}

		tsgen := codegen.NewTypeScriptGenerator()
		var file string

		if args.FileName == "mcp-types" {
			file = tsgen.GenerateMCPTypesFile()
		} else {
			tools, err := intr.IntrospectServer(ctx, args.FileName)
			if err != nil {
				return nil, nil, err
			}

			file, err = tsgen.GenerateFile(args.FileName, tools)
			if err != nil {
				return nil, nil, err
			}
		}

		// Create detailed response
		details := map[string]interface{}{
			"content": file,
		}

		detailsJSON, err := json.MarshalIndent(details, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal tool details: %w", err)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(detailsJSON)},
			},
		}, nil, nil
	})

	// Register get_mcp_tool_details tool
	//mcp.AddTool(server, &mcp.Tool{
	//	Name:        "get_mcp_tool_details",
	//	Description: "Get detailed information about a specific MCP tool",
	//}, func(ctx context.Context, req *mcp.CallToolRequest, args GetMcpToolDetailsArgs) (*mcp.CallToolResult, any, error) {
	//	// Get or create session context
	//	sessionCtx, err := sessionMgr.GetOrCreateSession(ctx, req.Session.ID())
	//	if err != nil {
	//		return nil, nil, err
	//	}
	//	if sessionCtx == nil {
	//		return nil, nil, fmt.Errorf("invalid session")
	//	}
	//
	//	allTools := sessionCtx.ClientHub.ListTools()
	//
	//	// Find the tool
	//	var foundTool *mcp.Tool
	//	for serverName, tools := range allTools {
	//		if serverName != args.FileName {
	//			continue
	//		}
	//
	//		for _, tool := range tools {
	//			if tool.Name != args.ToolName {
	//				continue
	//			}
	//
	//			foundTool = tool
	//			break
	//		}
	//	}
	//
	//	if foundTool == nil {
	//		return nil, nil, fmt.Errorf("tool %q not found", args.ToolName)
	//	}
	//
	//	// Create detailed response
	//	details := map[string]interface{}{
	//		"server":       args.FileName,
	//		"name":         foundTool.Name,
	//		"description":  foundTool.Description,
	//		"inputSchema":  foundTool.InputSchema,
	//		"outputSchema": foundTool.OutputSchema,
	//	}
	//
	//	detailsJSON, err := json.MarshalIndent(details, "", "  ")
	//	if err != nil {
	//		return nil, nil, fmt.Errorf("failed to marshal tool details: %w", err)
	//	}
	//
	//	return &mcp.CallToolResult{
	//		Content: []mcp.Content{
	//			&mcp.TextContent{Text: string(detailsJSON)},
	//		},
	//	}, nil, nil
	//})

	return server
}

// TODO: Extract to util
// toPascalCase converts a string to PascalCase
func toPascalCase(s string) string {
	// Split by underscore or dash
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == '_' || r == '-'
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
func toCamelCase(s string) string {
	pascal := toPascalCase(s)
	if len(pascal) == 0 {
		return pascal
	}
	return strings.ToLower(pascal[0:1]) + pascal[1:]
}
