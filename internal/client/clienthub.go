package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/codebraid-mcp/internal/config"
)

// McpClientHub manages multiple MCP client connections
type McpClientHub struct {
	clients map[string]*McpClient
	mu      sync.RWMutex
}

// NewMcpClientHub creates a new McpClientHub
func NewMcpClientHub() *McpClientHub {
	return &McpClientHub{
		clients: make(map[string]*McpClient),
	}
}

// Connect establishes connections to all configured MCP servers
func (ch *McpClientHub) Connect(ctx context.Context, cfg *config.Config) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	for name, serverCfg := range cfg.McpServers {
		client, err := NewMcpClient(ctx, name, serverCfg)
		if err != nil {
			return fmt.Errorf("failed to connect to server %q: %w", name, err)
		}
		ch.clients[name] = client
	}

	return nil
}

// CallTool calls a tool on a specific MCP server
func (ch *McpClientHub) CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	ch.mu.RLock()
	client, exists := ch.clients[serverName]
	ch.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("server %q not found", serverName)
	}

	return client.CallTool(ctx, toolName, args)
}

// FindToolServer finds which server has a specific tool
func (ch *McpClientHub) FindToolServer(toolName string) (string, error) {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	for name, client := range ch.clients {
		for _, tool := range client.GetTools() {
			if tool.Name == toolName {
				return name, nil
			}
		}
	}

	return "", fmt.Errorf("tool %q not found in any server", toolName)
}

// ListTools returns all tools from all servers
func (ch *McpClientHub) ListTools() map[string][]*mcp.Tool {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	result := make(map[string][]*mcp.Tool)
	for name, client := range ch.clients {
		result[name] = client.GetTools()
	}

	return result
}

// GetToolsWithDescription returns tools with their descriptions
func (ch *McpClientHub) GetToolsWithDescription() map[string][]ToolInfo {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	result := make(map[string][]ToolInfo)
	for name, client := range ch.clients {
		tools := make([]ToolInfo, 0, len(client.GetTools()))
		for _, tool := range client.GetTools() {
			tools = append(tools, ToolInfo{
				Name:        tool.Name,
				Description: tool.Description,
			})
		}
		result[name] = tools
	}

	return result
}

// Close closes all client connections
func (ch *McpClientHub) Close() error {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	var errs []error
	for name, client := range ch.clients {
		if err := client.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close client %q: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing clients: %v", errs)
	}

	return nil
}

// ToolInfo contains basic tool information
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}
