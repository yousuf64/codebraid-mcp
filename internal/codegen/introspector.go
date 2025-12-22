package codegen

import (
	"context"
	"fmt"

	"github.com/yousuf/codebraid-mcp/internal/client"
)

// Introspector discovers and extracts tool definitions from MCP servers
type Introspector struct {
	clientHub *client.McpClientHub
}

// NewIntrospector creates a new introspector
func NewIntrospector(clientHub *client.McpClientHub) *Introspector {
	return &Introspector{
		clientHub: clientHub,
	}
}

// IntrospectAll discovers all tools from all connected MCP servers
func (i *Introspector) IntrospectAll(ctx context.Context) ([]ToolDefinition, error) {
	allTools := i.clientHub.ListTools()

	definitions := make([]ToolDefinition, 0)

	for serverName, tools := range allTools {
		for _, tool := range tools {
			def := ToolDefinition{
				ServerName:  serverName,
				Name:        tool.Name,
				Description: tool.Description,
			}

			// Extract inputSchema if available
			if tool.InputSchema != nil {
				if schemaMap, ok := tool.InputSchema.(map[string]interface{}); ok {
					def.InputSchema = schemaMap
				}
			}

			// Extract outputSchema if available (new in MCP spec)
			if tool.OutputSchema != nil {
				if schemaMap, ok := tool.OutputSchema.(map[string]interface{}); ok {
					def.OutputSchema = schemaMap
				}
			}

			definitions = append(definitions, def)
		}
	}

	return definitions, nil
}

// IntrospectServer discovers tools from a specific MCP server
func (i *Introspector) IntrospectServer(ctx context.Context, serverName string) ([]ToolDefinition, error) {
	allTools := i.clientHub.ListTools()

	tools, exists := allTools[serverName]
	if !exists {
		return nil, fmt.Errorf("server %q not found", serverName)
	}

	definitions := make([]ToolDefinition, 0, len(tools))

	for _, tool := range tools {
		def := ToolDefinition{
			ServerName:  serverName,
			Name:        tool.Name,
			Description: tool.Description,
		}

		// Extract inputSchema if available
		if tool.InputSchema != nil {
			if schemaMap, ok := tool.InputSchema.(map[string]interface{}); ok {
				def.InputSchema = schemaMap
			}
		}

		// Extract outputSchema if available (new in MCP spec)
		if tool.OutputSchema != nil {
			if schemaMap, ok := tool.OutputSchema.(map[string]interface{}); ok {
				def.OutputSchema = schemaMap
			}
		}

		definitions = append(definitions, def)
	}

	return definitions, nil
}

// GroupByServer groups tool definitions by server name
func GroupByServer(tools []ToolDefinition) map[string][]ToolDefinition {
	grouped := make(map[string][]ToolDefinition)

	for _, tool := range tools {
		grouped[tool.ServerName] = append(grouped[tool.ServerName], tool)
	}

	return grouped
}

// ListServers returns a list of all available server names
func (i *Introspector) ListServers() []string {
	allTools := i.clientHub.ListTools()
	servers := make([]string, 0, len(allTools))

	for serverName := range allTools {
		servers = append(servers, serverName)
	}

	return servers
}
