package session

import (
	"context"
	"fmt"
	"sync"

	"github.com/yousuf/codebraid-mcp/internal/client"
	"github.com/yousuf/codebraid-mcp/internal/config"
)

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

	// Create session context
	session = NewSessionContext(sessionID, clientHub)
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
	}

	m.sessions = make(map[string]*SessionContext)

	if len(errs) > 0 {
		return fmt.Errorf("errors closing sessions: %v", errs)
	}

	return nil
}
