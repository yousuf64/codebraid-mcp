package pythonruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yousuf/runbyte/internal/config"
)

// Server manages the lifecycle of a Python runtime Node.js process
type Server struct {
	cmd    *exec.Cmd
	client *Client
	port   int
	cancel context.CancelFunc
}

// MountInfo represents a directory mount for the Python runtime
type MountInfo struct {
	Name     string `json:"name"`
	HostPath string `json:"hostPath"`
	ReadOnly bool   `json:"readOnly"`
}

// StartServer starts a new Python runtime server process
// sessionDir is the root directory for the session containing both servers/ and workspace/ subdirectories
// mounts contains additional directory mounts to expose to Python runtime
func StartServer(ctx context.Context, cfg *config.Config, sessionID, mcpCallbackURL, sessionDir string, mounts []MountInfo) (*Server, error) {
	// Get project root to find pkg/python-runtime
	projectRoot, err := getProjectRoot()
	if err != nil {
		return nil, fmt.Errorf("finding project root: %w", err)
	}

	serverDir := filepath.Join(projectRoot, "pkg", "python-runtime")
	serverScript := filepath.Join(serverDir, "server.js")

	// Verify server script exists
	if _, err := os.Stat(serverScript); err != nil {
		return nil, fmt.Errorf("python runtime server script not found at %s: %w", serverScript, err)
	}

	// Create context for the server process
	serverCtx, cancel := context.WithCancel(ctx)

	// Prepare command
	cmd := exec.CommandContext(serverCtx, "node", serverScript)
	cmd.Dir = serverDir

	// Set environment variables
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "PORT=0") // Use random port

	// Get Python packages from config
	pythonCfg := cfg.Runtime.Python
	if pythonCfg != nil && len(pythonCfg.Packages) > 0 {
		packagesJSON := fmt.Sprintf(`["%s"]`, strings.Join(pythonCfg.Packages, `","`))
		cmd.Env = append(cmd.Env, fmt.Sprintf("PACKAGES=%s", packagesJSON))
	} else {
		cmd.Env = append(cmd.Env, "PACKAGES=[]")
	}

	// Set MCP callback URL and session ID
	cmd.Env = append(cmd.Env, fmt.Sprintf("MCP_CALLBACK_URL=%s", mcpCallbackURL))
	cmd.Env = append(cmd.Env, fmt.Sprintf("SESSION_ID=%s", sessionID))

	// Set session root directory (contains both servers/ and workspace/ subdirectories)
	cmd.Env = append(cmd.Env, fmt.Sprintf("SESSION_DIR=%s", sessionDir))

	// Pass custom mounts as JSON
	if len(mounts) > 0 {
		mountsJSON, err := json.Marshal(mounts)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("marshaling mounts: %w", err)
		}
		cmd.Env = append(cmd.Env, fmt.Sprintf("CUSTOM_MOUNTS=%s", string(mountsJSON)))
		log.Printf("Passing %d custom mount(s) to Python runtime", len(mounts))
	}

	// Capture stdout to get the port
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	// Capture stderr for logging
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("creating stderr pipe: %w", err)
	}

	// Start the process
	log.Printf("Starting Python runtime server: %s", serverScript)
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting python runtime server: %w", err)
	}

	// Read port from stdout
	portChan := make(chan int, 1)
	errChan := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			log.Printf("Python runtime stdout: %s", line)

			// Look for PYTHON_RUNTIME_PORT=<port>
			if strings.HasPrefix(line, "PYTHON_RUNTIME_PORT=") {
				portStr := strings.TrimPrefix(line, "PYTHON_RUNTIME_PORT=")
				port, err := strconv.Atoi(portStr)
				if err != nil {
					errChan <- fmt.Errorf("parsing port: %w", err)
					return
				}
				portChan <- port
				return
			}
		}
		if err := scanner.Err(); err != nil {
			errChan <- fmt.Errorf("reading stdout: %w", err)
		}
	}()

	// Log stderr in background
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("Python runtime stderr: %s", scanner.Text())
		}
	}()

	// Wait for port or error with timeout
	var port int
	select {
	case port = <-portChan:
		log.Printf("Python runtime server started on port %d", port)
	case err := <-errChan:
		cancel()
		cmd.Wait()
		return nil, fmt.Errorf("failed to start python runtime: %w", err)
	case <-time.After(60 * time.Second):
		cancel()
		cmd.Wait()
		return nil, fmt.Errorf("timeout waiting for python runtime to start")
	}

	// Create client
	client := NewClient(port)

	// Wait for server to be ready
	log.Printf("Waiting for Python runtime to be ready...")
	if err := client.WaitForReady(serverCtx, 30*time.Second); err != nil {
		cancel()
		cmd.Wait()
		return nil, fmt.Errorf("python runtime not ready: %w", err)
	}

	log.Printf("Python runtime is ready")

	server := &Server{
		cmd:    cmd,
		client: client,
		port:   port,
		cancel: cancel,
	}

	// Monitor process in background
	go server.monitor()

	return server, nil
}

// monitor watches the process and logs when it exits
func (s *Server) monitor() {
	err := s.cmd.Wait()
	if err != nil {
		log.Printf("Python runtime process exited with error: %v", err)
	} else {
		log.Printf("Python runtime process exited")
	}
}

// Client returns the HTTP client for this server
func (s *Server) Client() *Client {
	return s.client
}

// Port returns the port the server is listening on
func (s *Server) Port() int {
	return s.port
}

// Shutdown gracefully shuts down the Python runtime server
func (s *Server) Shutdown(ctx context.Context) error {
	log.Printf("Shutting down Python runtime server")

	// Try graceful shutdown first
	if _, err := s.client.Shutdown(ctx); err != nil {
		log.Printf("Warning: failed to gracefully shutdown Python runtime, killing process: %v", err)
		s.cancel()
		return err
	}

	// Wait for process to exit
	done := make(chan error, 1)
	go func() {
		done <- s.cmd.Wait()
	}()

	select {
	case <-done:
		log.Printf("Python runtime server shut down successfully")
		return nil
	case <-time.After(5 * time.Second):
		log.Printf("Warning: Python runtime did not shut down in time, killing process")
		s.cancel()
		return fmt.Errorf("timeout waiting for shutdown")
	}
}

// getProjectRoot finds the project root directory
func getProjectRoot() (string, error) {
	// Try to find go.mod file
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find project root (go.mod not found)")
		}
		dir = parent
	}
}
