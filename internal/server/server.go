package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	ToolName string `json:"toolName" jsonschema:"Name of the tool to get details for"`
}

// NewMCPServer creates and configures the MCP server
func NewMCPServer(sessionMgr *session.Manager) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "codebraid-mcp",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: "MCP orchestrator with TypeScript code execution sandbox. Use execute_code to run TypeScript that can call downstream MCP tools via callTool(serverName, toolName, args).",
	})

	// Register execute_code tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "execute_code",
		Description: "Execute TypeScript code in a sandbox with access to downstream MCP tools via callTool(serverName, toolName, args)",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ExecuteCodeArgs) (*mcp.CallToolResult, any, error) {
		// Get or create session context
		sessionCtx, err := sessionMgr.GetOrCreateSession(ctx, req.Session.ID())
		if err != nil {
			return nil, nil, err
		}
		if sessionCtx == nil {
			return nil, nil, fmt.Errorf("invalid session")
		}

		// Create sandbox with clientbox
		sb, err := sandbox.NewSandbox(ctx, "./wasm/dist/sandbox.wasm", sessionCtx.ClientBox)
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
		Name:        "get_mcp_tools",
		Description: "List all available downstream MCP tools",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args GetMcpToolsArgs) (*mcp.CallToolResult, any, error) {
		// Get or create session context
		sessionCtx, err := sessionMgr.GetOrCreateSession(ctx, req.Session.ID())
		if err != nil {
			return nil, nil, err
		}
		if sessionCtx == nil {
			return nil, nil, fmt.Errorf("invalid session")
		}

		var toolsJSON []byte

		if args.WithDescription {
			tools := sessionCtx.ClientBox.GetToolsWithDescription()
			toolsJSON, err = json.MarshalIndent(tools, "", "  ")
		} else {
			tools := sessionCtx.ClientBox.ListTools()
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
		}

		if err != nil {
			return nil, nil, fmt.Errorf("failed to marshal tools: %w", err)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: string(toolsJSON)},
			},
		}, nil, nil
	})

	// Register get_mcp_tool_details tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_mcp_tool_details",
		Description: "Get detailed information about a specific MCP tool",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args GetMcpToolDetailsArgs) (*mcp.CallToolResult, any, error) {
		// Get or create session context
		sessionCtx, err := sessionMgr.GetOrCreateSession(ctx, req.Session.ID())
		if err != nil {
			return nil, nil, err
		}
		if sessionCtx == nil {
			return nil, nil, fmt.Errorf("invalid session")
		}

		allTools := sessionCtx.ClientBox.ListTools()

		// Find the tool
		var foundTool *mcp.Tool
		var foundServer string
		for serverName, tools := range allTools {
			for _, tool := range tools {
				if tool.Name == args.ToolName {
					foundTool = tool
					foundServer = serverName
					break
				}
			}
			if foundTool != nil {
				break
			}
		}

		if foundTool == nil {
			return nil, nil, fmt.Errorf("tool %q not found", args.ToolName)
		}

		// Create detailed response
		details := map[string]interface{}{
			"server":      foundServer,
			"name":        foundTool.Name,
			"description": foundTool.Description,
			"inputSchema": foundTool.InputSchema,
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

	return server
}
