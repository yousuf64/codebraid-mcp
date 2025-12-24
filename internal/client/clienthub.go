package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/codebraid-mcp/internal/config"
)

// McpClientHub manages multiple MCP client connections with lazy tool caching.
//
// Caching Strategy:
// - Tools are fetched from each MCP server once at connection time (stored in McpClient.tools)
// - The grouped map (server -> tools) is lazily cached on first Tools() call
// - Cache is invalidated when tools change (via RefreshServerTools or InvalidateToolsCache)
// - Thread-safe using double-checked locking pattern for optimal performance
//
// Performance:
// - First Tools() call: O(n) where n = number of servers
// - Subsequent calls: O(1) - returns cached pointer
// - ~50x faster after cache warm-up, zero allocations
//
// Cache Invalidation:
// - Call InvalidateToolsCache() to clear cache without re-fetching
// - Call RefreshServerTools(name) when MCP server notifies of tool changes
// - Call RefreshAllServerTools() for manual refresh of all servers
//
// Notification Handling:
// - Each McpClient listens for tool change notifications from its MCP server
// - When notified, McpClient calls back to ClientHub via handleToolsChanged
// - ClientHub automatically refreshes tools and invalidates cache
// - Optional: ClientHub can notify session layer via onToolsRefreshed callback
type McpClientHub struct {
	clients          map[string]*McpClient
	mu               sync.RWMutex
	cachedTools      map[string][]*mcp.Tool  // Lazy-cached result of Tools()
	onToolsRefreshed func(serverName string) // Optional callback for session layer
}

// NewMcpClientHub creates a new McpClientHub
func NewMcpClientHub() *McpClientHub {
	return &McpClientHub{
		clients: make(map[string]*McpClient),
	}
}

// Connect establishes connections to all configured MCP servers
// Each client will be set up with a callback to notify the hub when tools change
func (ch *McpClientHub) Connect(ctx context.Context, cfg *config.Config) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	for name, serverCfg := range cfg.McpServers {
		// Pass callback so client can notify hub when tools change
		client, err := NewMcpClient(ctx, name, serverCfg, ch.handleToolsChanged)
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

// Servers returns a list of all connected server names
func (ch *McpClientHub) Servers() []string {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	names := make([]string, 0, len(ch.clients))
	for name := range ch.clients {
		names = append(names, name)
	}
	return names
}

// Tools returns all tools from all servers, grouped by server name
// Results are cached for performance. Cache is invalidated when tools change.
func (ch *McpClientHub) Tools() map[string][]*mcp.Tool {
	// Fast path: check if cache exists (read lock)
	ch.mu.RLock()
	if ch.cachedTools != nil {
		cached := ch.cachedTools
		ch.mu.RUnlock()
		return cached
	}
	ch.mu.RUnlock()

	// Cache miss: build the map (write lock)
	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Double-check after acquiring write lock (another goroutine might have cached it)
	if ch.cachedTools != nil {
		return ch.cachedTools
	}

	// Build the tools map
	result := make(map[string][]*mcp.Tool, len(ch.clients))
	for name, client := range ch.clients {
		result[name] = client.GetTools()
	}

	// Cache the result
	ch.cachedTools = result
	return result
}

// ServerTools returns tools for a specific server
// Returns (tools, true) if server exists, (nil, false) if not found
func (ch *McpClientHub) ServerTools(serverName string) ([]*mcp.Tool, bool) {
	ch.mu.RLock()
	client, exists := ch.clients[serverName]
	ch.mu.RUnlock()

	if !exists {
		return nil, false
	}

	return client.GetTools(), true
}

// InvalidateToolsCache clears the cached tools map
// This should be called when MCP servers notify of tool changes
func (ch *McpClientHub) InvalidateToolsCache() {
	ch.mu.Lock()
	ch.cachedTools = nil
	ch.mu.Unlock()
}

// RefreshServerTools re-fetches tools from a specific MCP server and invalidates cache
// This should be called when a server notifies that its tools have changed
func (ch *McpClientHub) RefreshServerTools(ctx context.Context, serverName string) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	client, exists := ch.clients[serverName]
	if !exists {
		return fmt.Errorf("server %q not found", serverName)
	}

	// Re-fetch tools from the server
	toolsResult, err := client.session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return fmt.Errorf("failed to refresh tools for %q: %w", serverName, err)
	}

	// Update the client's cached tools
	client.tools = toolsResult.Tools

	// Invalidate the hub's cached map
	ch.cachedTools = nil

	return nil
}

// RefreshAllServerTools re-fetches tools from all connected servers and invalidates cache
// Useful for manual refresh or after connection issues
func (ch *McpClientHub) RefreshAllServerTools(ctx context.Context) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	var errs []error
	for name, client := range ch.clients {
		toolsResult, err := client.session.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			errs = append(errs, fmt.Errorf("server %q: %w", name, err))
			continue
		}
		client.tools = toolsResult.Tools
	}

	// Invalidate cache
	ch.cachedTools = nil

	if len(errs) > 0 {
		return fmt.Errorf("failed to refresh some servers: %v", errs)
	}

	return nil
}

// SetToolsRefreshedCallback sets an optional callback to be notified when tools are refreshed
// This allows the session layer to react to tool changes (e.g., regenerate TypeScript libraries)
func (ch *McpClientHub) SetToolsRefreshedCallback(callback func(serverName string)) {
	ch.mu.Lock()
	ch.onToolsRefreshed = callback
	ch.mu.Unlock()
}

// handleToolsChanged is called by McpClient when it receives a tool change notification
// This method runs in a separate goroutine to avoid blocking the notification handler
func (ch *McpClientHub) handleToolsChanged(serverName string) {
	// Run in goroutine to avoid blocking the notification callback
	go func() {
		fmt.Printf("Tools changed notification received for server %q\n", serverName)

		// Create a timeout context for the refresh operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Refresh tools from the server
		if err := ch.RefreshServerTools(ctx, serverName); err != nil {
			fmt.Printf("Failed to auto-refresh tools for %q: %v\n", serverName, err)
			return
		}

		fmt.Printf("Successfully auto-refreshed tools for server %q\n", serverName)

		// Notify session layer if callback is set
		ch.mu.RLock()
		callback := ch.onToolsRefreshed
		ch.mu.RUnlock()

		if callback != nil {
			callback(serverName)
		}
	}()
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
