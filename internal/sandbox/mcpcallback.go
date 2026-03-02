package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// McpToolCallbackRequest represents a request from Python runtime to call an MCP tool
type McpToolCallbackRequest struct {
	Server    string                 `json:"server"`
	Tool      string                 `json:"tool"`
	Arguments map[string]interface{} `json:"arguments"`
}

// McpToolCaller is an interface for calling MCP tools
// This avoids import cycles with the session package
type McpToolCaller interface {
	CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (*mcp.CallToolResult, error)
}

// McpToolCallbackHandler handles MCP tool calls from Python runtime
type McpToolCallbackHandler struct {
	getToolCaller func(sessionID string) (McpToolCaller, error)
}

// NewMcpToolCallbackHandler creates a new MCP tool callback handler
func NewMcpToolCallbackHandler(getToolCaller func(sessionID string) (McpToolCaller, error)) *McpToolCallbackHandler {
	return &McpToolCallbackHandler{
		getToolCaller: getToolCaller,
	}
}

// ServeHTTP handles HTTP POST /mcp-tool requests from Python runtime
func (h *McpToolCallbackHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only allow POST
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract session ID from header or query param
	sessionID := r.Header.Get("X-Session-ID")
	if sessionID == "" {
		sessionID = r.URL.Query().Get("session_id")
	}
	if sessionID == "" {
		http.Error(w, "Missing session ID", http.StatusBadRequest)
		return
	}

	// Get tool caller for this session
	toolCaller, err := h.getToolCaller(sessionID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Session error: %v", err), http.StatusNotFound)
		return
	}

	// Parse request
	var req McpToolCallbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	// Validate request
	if req.Server == "" {
		http.Error(w, "Missing server name", http.StatusBadRequest)
		return
	}
	if req.Tool == "" {
		http.Error(w, "Missing tool name", http.StatusBadRequest)
		return
	}

	// Call MCP tool
	result, err := toolCaller.CallTool(r.Context(), req.Server, req.Tool, req.Arguments)
	if err != nil {
		http.Error(w, fmt.Sprintf("MCP tool call failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Return result as JSON
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode response: %v", err), http.StatusInternalServerError)
		return
	}
}

// StartMcpCallbackServer starts an HTTP server for MCP tool callbacks
func StartMcpCallbackServer(ctx context.Context, getToolCaller func(sessionID string) (McpToolCaller, error)) (int, error) {
	handler := NewMcpToolCallbackHandler(getToolCaller)

	mux := http.NewServeMux()
	mux.Handle("/mcp-tool", handler)

	// Create listener on random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("failed to create listener: %w", err)
	}

	// Get the actual port
	port := listener.Addr().(*net.TCPAddr).Port

	// Create server
	server := &http.Server{
		Handler: mux,
	}

	// Start server in background
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Printf("MCP callback server error: %v\n", err)
		}
	}()

	// Shutdown server when context is cancelled
	go func() {
		<-ctx.Done()
		server.Shutdown(context.Background())
	}()

	return port, nil
}
