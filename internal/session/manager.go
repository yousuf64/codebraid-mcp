package session

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/runbyte/internal/bundler"
	"github.com/yousuf/runbyte/internal/client"
	"github.com/yousuf/runbyte/internal/codegen"
	"github.com/yousuf/runbyte/internal/config"
	"github.com/yousuf/runbyte/internal/pythonruntime"
	"github.com/yousuf/runbyte/internal/sandbox"
	"github.com/yousuf/runbyte/internal/strutil"
)

// Manager manages session contexts
type Manager struct {
	sessions        map[string]*SessionContext
	mu              sync.RWMutex
	config          *config.Config
	mcpCallbackPort int // Port for MCP tool callback server (set via SetMcpCallbackPort)

	// Cleanup worker
	cleanupTicker *time.Ticker
	cleanupStop   chan struct{}
	cleanupWg     sync.WaitGroup
}

// NewManager creates a new session manager
func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		sessions: make(map[string]*SessionContext),
		config:   cfg,
	}
}

// SetMcpCallbackPort sets the MCP callback server port
// This should be called once during initialization after starting the callback server
func (m *Manager) SetMcpCallbackPort(port int) {
	m.mcpCallbackPort = port
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

	// Initialize SandboxFileSystem with multiple directories
	if err := m.initializeSandboxFileSystem(session); err != nil {
		clientHub.Close()
		if session.BundleDir != "" {
			os.RemoveAll(session.BundleDir)
		}
		return nil, fmt.Errorf("failed to initialize sandbox filesystem: %w", err)
	}

	// Start Python runtime if language is Python
	// This must be done AFTER SandboxFS initialization so workspace directory exists for NODEFS mounting
	if m.config.GetLanguage() == "python" {
		if err := m.startPythonRuntime(ctx, session); err != nil {
			clientHub.Close()
			if session.BundleDir != "" {
				os.RemoveAll(session.BundleDir)
			}
			return nil, fmt.Errorf("failed to start Python runtime: %w", err)
		}
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

// GetLanguage returns the configured language from config
func (m *Manager) GetLanguage() string {
	return m.config.GetLanguage()
}

// GetPythonPackages returns the configured Python packages from config
func (m *Manager) GetPythonPackages() []string {
	return m.config.GetPythonPackages()
}

// GetMcpServerNames returns the names of all configured MCP servers
func (m *Manager) GetMcpServerNames() []string {
	return m.config.GetMcpServerNames()
}

// GetMounts returns the configured mounts from config
func (m *Manager) GetMounts() []config.MountConfig {
	return m.config.GetMounts()
}

// DeleteSession removes a session and cleans up its resources
func (m *Manager) DeleteSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[sessionID]
	if !exists {
		return fmt.Errorf("session %q not found", sessionID)
	}

	// Cleanup session resources
	m.cleanupSession(session)

	delete(m.sessions, sessionID)
	return nil
}

// CloseAll closes all sessions and stops the cleanup worker
func (m *Manager) CloseAll() error {
	// Stop cleanup worker first
	m.StopCleanupWorker()

	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for sessionID, session := range m.sessions {
		log.Printf("Closing session: %s", sessionID)
		m.cleanupSession(session)
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
	bundleDir, err := os.MkdirTemp("", fmt.Sprintf("runbyte-%s-", session.SessionID))
	if err != nil {
		return fmt.Errorf("failed to create bundle dir: %w", err)
	}

	// Get runtime language from config
	lang := m.config.GetLanguage()

	// Create servers directory
	serversDir := filepath.Join(bundleDir, "servers")
	if err := os.Mkdir(serversDir, 0755); err != nil {
		os.RemoveAll(bundleDir)
		return fmt.Errorf("failed to create servers dir: %w", err)
	}

	// Get all tools from connected MCP servers
	allTools := session.ClientHub.Tools()

	// Generate library files based on language
	if lang == "python" {
		if err := m.generatePythonLibraries(bundleDir, serversDir, allTools); err != nil {
			os.RemoveAll(bundleDir)
			return err
		}
	} else {
		// Default to TypeScript
		if err := m.generateTypeScriptLibraries(bundleDir, serversDir, allTools); err != nil {
			os.RemoveAll(bundleDir)
			return err
		}
	}

	// Update session
	session.BundleDir = bundleDir
	session.Language = lang

	// Note: Python runtime will be started later in GetSession() after SandboxFS is initialized
	// This ensures the workspace directory exists before mounting with NODEFS

	return nil
}

// generateTypeScriptLibraries generates TypeScript library files for all MCP servers
func (m *Manager) generateTypeScriptLibraries(bundleDir, serversDir string, allTools map[string][]*mcp.Tool) error {
	generator := codegen.NewTypeScriptGenerator()

	// Generate and write per-function library files for each server
	serverNames := make([]string, 0, len(allTools))
	for serverName, tools := range allTools {
		// Create server directory
		serverDir := filepath.Join(serversDir, serverName)
		if err := os.Mkdir(serverDir, 0755); err != nil {
			return fmt.Errorf("failed to create server dir %s: %w", serverName, err)
		}

		// Generate a file for each tool/function
		for _, tool := range tools {
			functionName := strutil.ToCamelCase(tool.Name)
			functionContent, err := generator.GenerateFunctionFile(serverName, tool)
			if err != nil {
				return fmt.Errorf("failed to generate function %s for %s: %w", functionName, serverName, err)
			}

			functionPath := filepath.Join(serverDir, fmt.Sprintf("%s.ts", functionName))
			if err := os.WriteFile(functionPath, []byte(functionContent), 0644); err != nil {
				return fmt.Errorf("failed to write function %s for %s: %w", functionName, serverName, err)
			}
		}

		// Generate server index.ts
		indexContent := generator.GenerateServerIndexFile(serverName, tools)
		indexPath := filepath.Join(serverDir, "index.ts")
		if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
			return fmt.Errorf("failed to write index.ts for %s: %w", serverName, err)
		}

		serverNames = append(serverNames, serverName)
	}

	// Generate top-level index.ts
	topIndexContent := generator.GenerateIndexFile(serverNames)
	topIndexPath := filepath.Join(serversDir, "index.ts")
	if err := os.WriteFile(topIndexPath, []byte(topIndexContent), 0644); err != nil {
		return fmt.Errorf("failed to write top-level index.ts: %w", err)
	}

	// Write rspack config
	rspackConfigPath := filepath.Join(bundleDir, "rspack.config.ts")
	rspackConfig := bundler.GetEmbeddedConfig()
	if err := os.WriteFile(rspackConfigPath, []byte(rspackConfig), 0644); err != nil {
		return fmt.Errorf("failed to write rspack config: %w", err)
	}

	// Generate @runbyte/fs stub
	if err := generateFsStub(bundleDir); err != nil {
		return fmt.Errorf("failed to generate @runbyte/fs stub: %w", err)
	}

	return nil
}

// generatePythonLibraries generates Python library files for all MCP servers
func (m *Manager) generatePythonLibraries(bundleDir, serversDir string, allTools map[string][]*mcp.Tool) error {
	generator := codegen.NewPythonGenerator()

	// Generate and write per-function library files for each server
	// Note: serversDir is already {bundleDir}/servers
	for serverName, tools := range allTools {
		// Sanitize server name for Python (replace hyphens with underscores)
		sanitizedName := strutil.ToSnakeCase(serverName)

		// Create server directory
		serverDir := filepath.Join(serversDir, sanitizedName)
		if err := os.Mkdir(serverDir, 0755); err != nil {
			return fmt.Errorf("failed to create server dir %s: %w", serverName, err)
		}

		// Generate a file for each tool/function
		for _, tool := range tools {
			functionName := strutil.ToSnakeCase(tool.Name)
			functionContent, err := generator.GenerateFunctionFile(serverName, tool)
			if err != nil {
				return fmt.Errorf("failed to generate function %s for %s: %w", functionName, serverName, err)
			}

			functionPath := filepath.Join(serverDir, fmt.Sprintf("%s.py", functionName))
			if err := os.WriteFile(functionPath, []byte(functionContent), 0644); err != nil {
				return fmt.Errorf("failed to write function %s for %s: %w", functionName, serverName, err)
			}
		}

		// Generate server __init__.py
		initContent := generator.GenerateServerInitFile(serverName, tools)

		initPath := filepath.Join(serverDir, "__init__.py")
		if err := os.WriteFile(initPath, []byte(initContent), 0644); err != nil {
			return fmt.Errorf("failed to write __init__.py for %s: %w", serverName, err)
		}
	}

	// Create empty __init__.py in servers directory for package structure
	serversInitPath := filepath.Join(serversDir, "__init__.py")
	if err := os.WriteFile(serversInitPath, []byte(""), 0644); err != nil {
		return fmt.Errorf("failed to write servers/__init__.py: %w", err)
	}

	return nil
}

// regenerateLibForServer regenerates library for a specific server based on session language
// This is called automatically when the MCP server notifies of tool changes
func regenerateLibForServer(session *SessionContext, serverName string) error {
	session.mu.Lock()
	defer session.mu.Unlock()

	// Get tools from the server (already refreshed by ClientHub notification handler)
	tools, ok := session.ClientHub.ServerTools(serverName)
	if !ok {
		return fmt.Errorf("server %q not found", serverName)
	}

	// Sanitize server name for Python (replace hyphens with underscores)
	// TypeScript can handle hyphens in directory names, but Python cannot in module names
	dirName := serverName
	if session.Language == "python" {
		dirName = strutil.ToSnakeCase(serverName)
	}

	// Both Python and TypeScript use the same directory structure: {bundleDir}/servers/{serverName}
	serverDir := filepath.Join(session.BundleDir, "servers", dirName)

	// Remove old server directory
	if err := os.RemoveAll(serverDir); err != nil {
		return fmt.Errorf("failed to remove old server dir: %w", err)
	}

	// Create fresh server directory
	if err := os.Mkdir(serverDir, 0755); err != nil {
		return fmt.Errorf("failed to create server dir: %w", err)
	}

	// Generate files based on language
	if session.Language == "python" {
		return regeneratePythonServer(serverDir, serverName, tools)
	}
	return regenerateTypeScriptServer(serverDir, serverName, tools)
}

// regenerateTypeScriptServer regenerates TypeScript files for a server
func regenerateTypeScriptServer(serverDir, serverName string, tools []*mcp.Tool) error {
	generator := codegen.NewTypeScriptGenerator()

	// Generate a file for each tool/function
	for _, tool := range tools {
		functionName := strutil.ToCamelCase(tool.Name)
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

// regeneratePythonServer regenerates Python files for a server
func regeneratePythonServer(serverDir, serverName string, tools []*mcp.Tool) error {
	generator := codegen.NewPythonGenerator()

	// Generate a file for each tool/function
	for _, tool := range tools {
		functionName := strutil.ToSnakeCase(tool.Name)
		functionContent, err := generator.GenerateFunctionFile(serverName, tool)
		if err != nil {
			return fmt.Errorf("failed to generate function %s: %w", functionName, err)
		}

		functionPath := filepath.Join(serverDir, fmt.Sprintf("%s.py", functionName))
		if err := os.WriteFile(functionPath, []byte(functionContent), 0644); err != nil {
			return fmt.Errorf("failed to write function %s: %w", functionName, err)
		}
	}

	// Generate server __init__.py
	initContent := generator.GenerateServerInitFile(serverName, tools)
	initPath := filepath.Join(serverDir, "__init__.py")
	if err := os.WriteFile(initPath, []byte(initContent), 0644); err != nil {
		return fmt.Errorf("failed to write __init__.py: %w", err)
	}

	return nil
}

// initializeSandboxFileSystem creates and configures the SandboxFileSystem for a session
func (m *Manager) initializeSandboxFileSystem(session *SessionContext) error {
	// Workspace is a subdirectory within the session's bundle directory
	// This ensures workspace is isolated per session, not shared across all sessions
	workspaceDir := filepath.Join(session.BundleDir, "workspace")

	// Start with the default workspace directory (always present)
	directories := []sandbox.DirectoryConfig{
		{
			Name:         "workspace",
			Description:  "Default working directory for session files",
			Root:         workspaceDir,
			ReadOnly:     false,
			MaxFileSize:  10 * 1024 * 1024, // 10MB per file
			MaxFiles:     1000,
			MaxTotalSize: 100 * 1024 * 1024, // 100MB total
		},
	}

	// Add custom mounts from config
	mounts := m.config.GetMounts()
	configDir := m.config.GetConfigDir()

	if len(mounts) > 0 {
		log.Printf("Loading %d custom mount(s) from config (configDir=%q)", len(mounts), configDir)
	}

	for _, mount := range mounts {
		dirConfig, err := m.resolveMountConfig(mount, configDir)
		if err != nil {
			log.Printf("Warning: Skipping mount %q: %v", mount.Name, err)
			continue
		}
		log.Printf("Mounted %q at %s (readOnly=%v)", mount.Name, dirConfig.Root, dirConfig.ReadOnly)
		directories = append(directories, dirConfig)
	}

	// Create SandboxFileSystem
	sfs, err := sandbox.NewSandboxFileSystem(directories)
	if err != nil {
		return fmt.Errorf("failed to create sandbox filesystem: %w", err)
	}

	session.SandboxFS = sfs
	return nil
}

// resolveMountConfig resolves and validates a mount configuration
func (m *Manager) resolveMountConfig(mount config.MountConfig, configDir string) (sandbox.DirectoryConfig, error) {
	// Validate mount name
	if mount.Name == "" {
		return sandbox.DirectoryConfig{}, fmt.Errorf("mount name cannot be empty")
	}
	if mount.Name == "workspace" || mount.Name == "servers" {
		return sandbox.DirectoryConfig{}, fmt.Errorf("mount name %q is reserved", mount.Name)
	}
	if !isValidMountName(mount.Name) {
		return sandbox.DirectoryConfig{}, fmt.Errorf("mount name %q contains invalid characters (only alphanumeric, dash, and underscore allowed)", mount.Name)
	}

	// Resolve path
	resolvedPath, err := mount.ResolvePath(configDir)
	if err != nil {
		return sandbox.DirectoryConfig{}, fmt.Errorf("failed to resolve path: %w", err)
	}

	// Validate path exists
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return sandbox.DirectoryConfig{}, fmt.Errorf("path does not exist: %w", err)
	}
	if !info.IsDir() {
		return sandbox.DirectoryConfig{}, fmt.Errorf("path is not a directory: %s", resolvedPath)
	}

	// Parse size limits with defaults
	maxFileSize := int64(10 * 1024 * 1024) // Default: 10MB
	if mount.MaxFileSize != "" {
		maxFileSize, err = config.ParseSize(mount.MaxFileSize)
		if err != nil {
			return sandbox.DirectoryConfig{}, fmt.Errorf("invalid maxFileSize: %w", err)
		}
	}

	maxFiles := 1000 // Default
	if mount.MaxFiles > 0 {
		maxFiles = mount.MaxFiles
	}

	maxTotalSize := int64(100 * 1024 * 1024) // Default: 100MB
	if mount.MaxTotalSize != "" {
		maxTotalSize, err = config.ParseSize(mount.MaxTotalSize)
		if err != nil {
			return sandbox.DirectoryConfig{}, fmt.Errorf("invalid maxTotalSize: %w", err)
		}
	}

	// Check if we have write permissions (if not read-only)
	if !mount.ReadOnly {
		testFile := filepath.Join(resolvedPath, ".runbyte_write_test")
		if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
			log.Printf("Warning: Mount %q at %s does not have write permissions, forcing read-only", mount.Name, resolvedPath)
			mount.ReadOnly = true
		} else {
			os.Remove(testFile)
		}
	}

	return sandbox.DirectoryConfig{
		Name:         mount.Name,
		Description:  mount.Description,
		Root:         resolvedPath,
		ReadOnly:     mount.ReadOnly,
		MaxFileSize:  maxFileSize,
		MaxFiles:     maxFiles,
		MaxTotalSize: maxTotalSize,
	}, nil
}

// isValidMountName checks if a mount name contains only valid characters
func isValidMountName(name string) bool {
	// Allow alphanumeric, dash, and underscore
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// startPythonRuntime starts the Python runtime server for a session
func (m *Manager) startPythonRuntime(ctx context.Context, session *SessionContext) error {
	if m.mcpCallbackPort == 0 {
		return fmt.Errorf("MCP callback port not set - call SetMcpCallbackPort first")
	}

	// Create a custom config with MCP callback URL for this session
	// We'll pass the session ID via header, not in URL
	mcpCallbackURL := fmt.Sprintf("http://localhost:%d/mcp-tool", m.mcpCallbackPort)

	// IMPORTANT: Use background context instead of request context
	// The Python runtime must live beyond individual request contexts
	// In stateless mode, the request context gets cancelled after each request
	// which would kill the Python process prematurely
	runtimeCtx := context.Background()

	// Collect mount information from the sandbox filesystem
	var mounts []pythonruntime.MountInfo
	if session.SandboxFS != nil {
		for _, dirInfo := range session.SandboxFS.GetDirectoryInfo() {
			// Skip the default workspace and servers directories as they're handled by SESSION_DIR
			if dirInfo.Name == "workspace" || dirInfo.Name == "servers" {
				continue
			}
			// Get the actual directory config to find the host path
			// We need to access the internal directories map - for now, we'll reconstruct from config
			mounts = append(mounts, pythonruntime.MountInfo{
				Name:     dirInfo.Name,
				HostPath: "", // Will be filled in next iteration
				ReadOnly: dirInfo.ReadOnly,
			})
		}
	}

	// Get mounts from config and resolve their paths to pass to Python runtime
	configMounts := m.config.GetMounts()
	configDir := m.config.GetConfigDir()
	mounts = nil // Reset and rebuild with proper paths

	for _, mount := range configMounts {
		resolvedPath, err := mount.ResolvePath(configDir)
		if err != nil {
			log.Printf("Warning: Skipping mount %q for Python runtime: %v", mount.Name, err)
			continue
		}
		// Verify path still exists
		if _, err := os.Stat(resolvedPath); err != nil {
			log.Printf("Warning: Skipping mount %q for Python runtime: path no longer exists: %v", mount.Name, err)
			continue
		}
		mounts = append(mounts, pythonruntime.MountInfo{
			Name:     mount.Name,
			HostPath: resolvedPath,
			ReadOnly: mount.ReadOnly,
		})
	}

	// Start Python runtime server with session root directory
	// The session directory contains both servers/ and workspace/ subdirectories
	server, err := pythonruntime.StartServer(runtimeCtx, m.config, session.SessionID, mcpCallbackURL, session.BundleDir, mounts)
	if err != nil {
		return fmt.Errorf("failed to start Python runtime: %w", err)
	}

	// Store both server and client in session
	session.pythonServer = server
	session.PythonRuntime = server.Client()

	log.Printf("Session %s: Python runtime started on port %d", session.SessionID, server.Port())

	return nil
}

// StartCleanupWorker starts a background goroutine that periodically cleans up idle sessions
func (m *Manager) StartCleanupWorker(interval, maxIdleTime time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Don't start if already running
	if m.cleanupTicker != nil {
		return
	}

	// Don't start if maxIdleTime is 0 (disabled)
	if maxIdleTime == 0 {
		log.Println("Session cleanup disabled (maxIdleTime = 0)")
		return
	}

	m.cleanupTicker = time.NewTicker(interval)
	m.cleanupStop = make(chan struct{})

	m.cleanupWg.Add(1)
	go func() {
		defer m.cleanupWg.Done()
		log.Printf("Session cleanup worker started (interval: %v, max idle: %v)", interval, maxIdleTime)

		for {
			select {
			case <-m.cleanupTicker.C:
				m.cleanupIdleSessions(maxIdleTime)
			case <-m.cleanupStop:
				log.Println("Session cleanup worker stopped")
				return
			}
		}
	}()
}

// StopCleanupWorker stops the cleanup worker goroutine
func (m *Manager) StopCleanupWorker() {
	m.mu.Lock()
	if m.cleanupTicker != nil {
		m.cleanupTicker.Stop()
		close(m.cleanupStop)
		m.cleanupTicker = nil
		m.cleanupStop = nil
	}
	m.mu.Unlock()

	// Wait for cleanup goroutine to finish
	m.cleanupWg.Wait()
}

// cleanupIdleSessions removes sessions that have been idle for longer than maxIdleTime
func (m *Manager) cleanupIdleSessions(maxIdleTime time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var toDelete []string

	// Find idle sessions
	for sessionID, session := range m.sessions {
		idleDuration := session.IdleDuration()
		if idleDuration > maxIdleTime {
			toDelete = append(toDelete, sessionID)
			log.Printf("Session %s is idle for %v (max: %v) - marking for cleanup",
				sessionID, idleDuration, maxIdleTime)
		}
	}

	// Delete idle sessions
	for _, sessionID := range toDelete {
		session := m.sessions[sessionID]
		log.Printf("Closing idle session: %s (age: %v, idle: %v)",
			sessionID, session.Age(), session.IdleDuration())

		// Cleanup session resources
		m.cleanupSession(session)

		// Remove from map
		delete(m.sessions, sessionID)
	}

	if len(toDelete) > 0 {
		log.Printf("Cleaned up %d idle session(s). Active sessions: %d", len(toDelete), len(m.sessions))
	}
}

// cleanupSession cleans up all resources associated with a session
// Caller must hold the lock
func (m *Manager) cleanupSession(session *SessionContext) {
	// Close Python runtime if exists
	if session.pythonServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := session.pythonServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("Warning: failed to shutdown Python runtime for session %s: %v", session.SessionID, err)
		}
	}

	// Close MCP client connections
	if session.ClientHub != nil {
		if err := session.ClientHub.Close(); err != nil {
			log.Printf("Warning: failed to close client hub for session %s: %v", session.SessionID, err)
		}
	}

	// Cleanup sandbox filesystem
	if session.SandboxFS != nil {
		if err := session.SandboxFS.Cleanup(); err != nil {
			log.Printf("Warning: failed to cleanup sandbox filesystem for session %s: %v", session.SessionID, err)
		}
	}

	// Remove bundle directory
	if session.BundleDir != "" {
		if err := os.RemoveAll(session.BundleDir); err != nil {
			log.Printf("Warning: failed to remove bundle directory for session %s: %v", session.SessionID, err)
		}
	}
}
