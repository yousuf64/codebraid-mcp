package session

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yousuf/codebraid-mcp/internal/bundler"
	"github.com/yousuf/codebraid-mcp/internal/client"
	"github.com/yousuf/codebraid-mcp/internal/codegen"
	"github.com/yousuf/codebraid-mcp/internal/config"
)

// toCamelCase converts snake_case to camelCase
func toCamelCase(s string) string {
	parts := strings.Split(s, "_")
	if len(parts) == 0 {
		return s
	}

	// First part stays lowercase
	result := parts[0]

	// Capitalize first letter of remaining parts
	for _, part := range parts[1:] {
		if len(part) > 0 {
			result += strings.ToUpper(part[:1]) + part[1:]
		}
	}

	return result
}

// Manager manages session contexts
type Manager struct {
	sessions map[string]*SessionContext
	mu       sync.RWMutex
	config   *config.Config
}

// NewManager creates a new session manager
func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		sessions: make(map[string]*SessionContext),
		config:   cfg,
	}
}

// GetOrCreateSession gets an existing session or creates a new one
func (m *Manager) GetOrCreateSession(ctx context.Context, sessionID string) (*SessionContext, error) {
	// Try to get existing session
	m.mu.RLock()
	session, exists := m.sessions[sessionID]
	m.mu.RUnlock()

	if exists {
		return session, nil
	}

	// Create new session
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if session, exists := m.sessions[sessionID]; exists {
		return session, nil
	}

	// Create new McpClientHub and connect to all servers
	clientHub := client.NewMcpClientHub()
	if err := clientHub.Connect(ctx, m.config); err != nil {
		return nil, fmt.Errorf("failed to connect client hub: %w", err)
	}

	// Initialize session context
	session = NewSessionContext(sessionID, clientHub)

	// Setup bundle directory and generate library files
	if err := m.initializeSessionBundleDir(ctx, session); err != nil {
		// Clean up client hub on error
		clientHub.Close()
		return nil, fmt.Errorf("failed to initialize session bundle directory: %w", err)
	}

	// Setup automatic library regeneration when MCP servers notify of tool changes
	clientHub.SetToolsRefreshedCallback(func(serverName string) {
		log.Printf("Session %s: tools changed for server %q, regenerating libraries...", sessionID, serverName)

		if err := regenerateLibForServer(session, serverName); err != nil {
			log.Printf("Session %s: failed to regenerate libs for %q: %v", sessionID, serverName, err)
		} else {
			log.Printf("Session %s: successfully regenerated libs for %q", sessionID, serverName)
		}
	})

	m.sessions[sessionID] = session

	return session, nil
}

// GetSession retrieves an existing session
func (m *Manager) GetSession(sessionID string) *SessionContext {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

// DeleteSession removes a session and cleans up its resources
func (m *Manager) DeleteSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[sessionID]
	if !exists {
		return fmt.Errorf("session %q not found", sessionID)
	}

	// Close all client connections
	if err := session.ClientHub.Close(); err != nil {
		return fmt.Errorf("failed to close client hub: %w", err)
	}

	// Clean up bundle directory
	if session.BundleDir != "" {
		if err := os.RemoveAll(session.BundleDir); err != nil {
			log.Printf("Warning: failed to clean up bundle dir %s: %v", session.BundleDir, err)
		}
	}

	delete(m.sessions, sessionID)
	return nil
}

// CloseAll closes all sessions
func (m *Manager) CloseAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for sessionID, session := range m.sessions {
		if err := session.ClientHub.Close(); err != nil {
			errs = append(errs, fmt.Errorf("session %q: %w", sessionID, err))
		}

		// Clean up bundle directory
		if session.BundleDir != "" {
			if err := os.RemoveAll(session.BundleDir); err != nil {
				log.Printf("Warning: failed to clean up bundle dir %s: %v", session.BundleDir, err)
			}
		}
	}

	m.sessions = make(map[string]*SessionContext)

	if len(errs) > 0 {
		return fmt.Errorf("errors closing sessions: %v", errs)
	}

	return nil
}

// initializeSessionBundleDir creates the bundle directory and writes library files
func (m *Manager) initializeSessionBundleDir(ctx context.Context, session *SessionContext) error {
	// Create persistent bundle directory for this session
	bundleDir, err := os.MkdirTemp("", fmt.Sprintf("codebraid-%s-", session.SessionID))
	if err != nil {
		return fmt.Errorf("failed to create bundle dir: %w", err)
	}

	// Create servers directory
	serversDir := filepath.Join(bundleDir, "servers")
	if err := os.Mkdir(serversDir, 0755); err != nil {
		os.RemoveAll(bundleDir)
		return fmt.Errorf("failed to create servers dir: %w", err)
	}

	// Get all tools from connected MCP servers and generate TypeScript libraries
	allTools := session.ClientHub.Tools()
	generator := codegen.NewTypeScriptGenerator()

	// Generate and write per-function library files for each server
	serverNames := make([]string, 0, len(allTools))
	for serverName, tools := range allTools {
		// Create server directory
		serverDir := filepath.Join(serversDir, serverName)
		if err := os.Mkdir(serverDir, 0755); err != nil {
			os.RemoveAll(bundleDir)
			return fmt.Errorf("failed to create server dir %s: %w", serverName, err)
		}

		// Generate a file for each tool/function
		for _, tool := range tools {
			functionName := toCamelCase(tool.Name)
			functionContent, err := generator.GenerateFunctionFile(serverName, tool)
			if err != nil {
				os.RemoveAll(bundleDir)
				return fmt.Errorf("failed to generate function %s for %s: %w", functionName, serverName, err)
			}

			functionPath := filepath.Join(serverDir, fmt.Sprintf("%s.ts", functionName))
			if err := os.WriteFile(functionPath, []byte(functionContent), 0644); err != nil {
				os.RemoveAll(bundleDir)
				return fmt.Errorf("failed to write function %s for %s: %w", functionName, serverName, err)
			}
		}

		// Generate server index.ts
		indexContent := generator.GenerateServerIndexFile(serverName, tools)
		indexPath := filepath.Join(serverDir, "index.ts")
		if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
			os.RemoveAll(bundleDir)
			return fmt.Errorf("failed to write index.ts for %s: %w", serverName, err)
		}

		serverNames = append(serverNames, serverName)
	}

	// Generate top-level index.ts
	topIndexContent := generator.GenerateIndexFile(serverNames)
	topIndexPath := filepath.Join(serversDir, "index.ts")
	if err := os.WriteFile(topIndexPath, []byte(topIndexContent), 0644); err != nil {
		os.RemoveAll(bundleDir)
		return fmt.Errorf("failed to write top-level index.ts: %w", err)
	}

	// Write mcp-types.ts
	mcpTypesContent := generator.GenerateMCPTypesFile()
	mcpTypesPath := filepath.Join(serversDir, "mcp-types.ts")
	if err := os.WriteFile(mcpTypesPath, []byte(mcpTypesContent), 0644); err != nil {
		os.RemoveAll(bundleDir)
		return fmt.Errorf("failed to write mcp-types.ts: %w", err)
	}

	// Write rspack config
	rspackConfigPath := filepath.Join(bundleDir, "rspack.config.ts")
	rspackConfig := bundler.GetEmbeddedConfig()
	if err := os.WriteFile(rspackConfigPath, []byte(rspackConfig), 0644); err != nil {
		os.RemoveAll(bundleDir)
		return fmt.Errorf("failed to write rspack config: %w", err)
	}

	// Update session
	session.BundleDir = bundleDir

	return nil
}

// regenerateLibForServer regenerates TypeScript library for a specific server
// This is called automatically when the MCP server notifies of tool changes
func regenerateLibForServer(session *SessionContext, serverName string) error {
	session.mu.Lock()
	defer session.mu.Unlock()

	// Get tools from the server (already refreshed by ClientHub notification handler)
	tools, ok := session.ClientHub.ServerTools(serverName)
	if !ok {
		return fmt.Errorf("server %q not found", serverName)
	}

	// Remove old server directory
	serverDir := filepath.Join(session.BundleDir, "servers", serverName)
	if err := os.RemoveAll(serverDir); err != nil {
		return fmt.Errorf("failed to remove old server dir: %w", err)
	}

	// Create fresh server directory
	if err := os.Mkdir(serverDir, 0755); err != nil {
		return fmt.Errorf("failed to create server dir: %w", err)
	}

	// Generate TypeScript files for this server
	generator := codegen.NewTypeScriptGenerator()

	// Generate a file for each tool/function
	for _, tool := range tools {
		functionName := toCamelCase(tool.Name)
		functionContent, err := generator.GenerateFunctionFile(serverName, tool)
		if err != nil {
			return fmt.Errorf("failed to generate function %s: %w", functionName, err)
		}

		functionPath := filepath.Join(serverDir, fmt.Sprintf("%s.ts", functionName))
		if err := os.WriteFile(functionPath, []byte(functionContent), 0644); err != nil {
			return fmt.Errorf("failed to write function %s: %w", functionName, err)
		}
	}

	// Generate server index.ts
	indexContent := generator.GenerateServerIndexFile(serverName, tools)
	indexPath := filepath.Join(serverDir, "index.ts")
	if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
		return fmt.Errorf("failed to write index.ts: %w", err)
	}

	return nil
}
