package session

import (
	"sync"
	"time"

	"github.com/yousuf/codebraid-mcp/internal/client"
)

// SessionContext represents a session with its associated resources and lifecycle.
// It stores session data independently of request contexts.
type SessionContext struct {
	SessionID      string
	ClientHub      *client.McpClientHub
	CreatedAt      time.Time
	lastAccessedAt time.Time
	mu             sync.RWMutex
}

// NewSessionContext creates a new session context.
func NewSessionContext(sessionID string, clientHub *client.McpClientHub) *SessionContext {
	now := time.Now()
	return &SessionContext{
		SessionID:      sessionID,
		ClientHub:      clientHub,
		CreatedAt:      now,
		lastAccessedAt: now,
	}
}

// UpdateLastAccessed updates the last accessed timestamp (thread-safe)
func (s *SessionContext) UpdateLastAccessed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAccessedAt = time.Now()
}

// LastAccessedAt returns the last accessed timestamp (thread-safe)
func (s *SessionContext) LastAccessedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastAccessedAt
}

// Age returns the duration since the session was created
func (s *SessionContext) Age() time.Duration {
	return time.Since(s.CreatedAt)
}

// IdleDuration returns the duration since the last access
func (s *SessionContext) IdleDuration() time.Duration {
	return time.Since(s.LastAccessedAt())
}
