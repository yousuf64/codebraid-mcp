package client

import (
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/codebraid-mcp/internal/config"
	"net/http"
	"os"
	"os/exec"
)

// MCPClient wraps an MCP client connection
type MCPClient struct {
	name    string
	session *mcp.ClientSession
	tools   []*mcp.Tool
}

// NewMCPClient creates a new MCP client based on the configuration
func NewMCPClient(ctx context.Context, name string, cfg config.McpServerConfig) (*MCPClient, error) {
	var transport mcp.Transport
	var err error

	switch cfg.Type {
	case "stdio":
		transport, err = createStdioTransport(cfg)
	case "http":
		transport, err = createHttpTransport(cfg)
	case "sse":
		transport, err = createSSETransport(cfg)
	default:
		return nil, fmt.Errorf("unsupported transport type: %s", cfg.Type)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create transport: %w", err)
	}

	// Create MCP client
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "codebraid-mcp-client",
		Version: "1.0.0",
	}, &mcp.ClientOptions{})

	// Connect to the server
	session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	// List available tools
	toolsResult, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("failed to list tools: %w", err)
	}

	return &MCPClient{
		name:    name,
		session: session,
		tools:   toolsResult.Tools,
	}, nil
}

// createStdioTransport creates a stdio transport
func createStdioTransport(cfg config.McpServerConfig) (mcp.Transport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)

	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}

	if len(cfg.Env) > 0 {
		// Start with current environment
		cmd.Env = os.Environ()
		// Add/override with config env vars
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	return &mcp.CommandTransport{Command: cmd}, nil
}

// McpClientRoundTripper is a custom RoundTripper for injecting configured headers into MCP client requests
type McpClientRoundTripper struct {
	headers map[string]string
	next    http.RoundTripper
}

// RoundTrip implements the http.RoundTripper interface
func (lrt *McpClientRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range lrt.headers {
		req.Header.Set(k, v)
	}

	// Execute the actual request by calling the "next" RoundTripper
	resp, err := lrt.next.RoundTrip(req)

	// Return the response and error from the inner RoundTripper
	return resp, err
}

func createHttpTransport(cfg config.McpServerConfig) (mcp.Transport, error) {
	c := &http.Client{}
	c.Transport = &McpClientRoundTripper{
		headers: cfg.Headers,
		next:    http.DefaultTransport,
	}

	return &mcp.StreamableClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: c,
		MaxRetries: 0,
	}, nil
}

func createSSETransport(cfg config.McpServerConfig) (mcp.Transport, error) {
	return &mcp.SSEClientTransport{
		Endpoint:   cfg.URL,
		HTTPClient: nil,
	}, nil
}

// CallTool calls a tool on this MCP client
func (c *MCPClient) CallTool(ctx context.Context, toolName string, args map[string]interface{}) (*mcp.CallToolResult, error) {
	return c.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
}

// GetTools returns the list of available tools
func (c *MCPClient) GetTools() []*mcp.Tool {
	return c.tools
}

// GetName returns the client name
func (c *MCPClient) GetName() string {
	return c.name
}

// Close closes the client connection
func (c *MCPClient) Close() error {
	if c.session != nil {
		return c.session.Close()
	}
	return nil
}
