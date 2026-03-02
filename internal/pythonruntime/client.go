package pythonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is an HTTP client for communicating with the Python runtime server
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new Python runtime client
func NewClient(port int) *Client {
	return &Client{
		baseURL: fmt.Sprintf("http://localhost:%d", port),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// HealthResponse represents the response from /health endpoint
type HealthResponse struct {
	Status   string   `json:"status"`
	Ready    bool     `json:"ready"`
	Packages []string `json:"packages"`
}

// ExecuteRequest represents a request to /execute endpoint
type ExecuteRequest struct {
	Code string `json:"code"`
}

// ExecuteResponse represents the response from /execute endpoint
type ExecuteResponse struct {
	Result    *string `json:"result"`    // JSON string of result, or nil
	Error     *string `json:"error"`     // Error message, or nil
	Traceback *string `json:"traceback"` // Python traceback, or nil
}

// ShutdownResponse represents the response from /shutdown endpoint
type ShutdownResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Health checks if the Python runtime server is ready
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/health", nil)
	if err != nil {
		return nil, fmt.Errorf("creating health request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("health request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading health response: %w", err)
	}

	var health HealthResponse
	if err := json.Unmarshal(body, &health); err != nil {
		return nil, fmt.Errorf("parsing health response: %w", err)
	}

	return &health, nil
}

// Execute sends Python code to the runtime server for execution
func (c *Client) Execute(ctx context.Context, code string) (*ExecuteResponse, error) {
	reqBody := ExecuteRequest{Code: code}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/execute", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("creating execute request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading execute response: %w", err)
	}

	// Handle non-200 responses
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("execute failed with status %d: %s", resp.StatusCode, string(body))
	}

	var execResp ExecuteResponse
	if err := json.Unmarshal(body, &execResp); err != nil {
		return nil, fmt.Errorf("parsing execute response: %w", err)
	}

	return &execResp, nil
}

// Shutdown gracefully shuts down the Python runtime server
func (c *Client) Shutdown(ctx context.Context) (*ShutdownResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/shutdown", nil)
	if err != nil {
		return nil, fmt.Errorf("creating shutdown request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("shutdown request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading shutdown response: %w", err)
	}

	var shutdownResp ShutdownResponse
	if err := json.Unmarshal(body, &shutdownResp); err != nil {
		return nil, fmt.Errorf("parsing shutdown response: %w", err)
	}

	return &shutdownResp, nil
}

// WaitForReady waits for the Python runtime server to be ready
// Returns an error if the server doesn't become ready within the timeout
func (c *Client) WaitForReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for Python runtime to be ready")
			}

			health, err := c.Health(ctx)
			if err == nil && health.Ready {
				return nil
			}
		}
	}
}
