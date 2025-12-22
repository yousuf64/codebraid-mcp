package session

import (
	"context"
	"sync"
	"time"

	"github.com/yousuf/codebraid-mcp/internal/client"
)

// SessionContext represents a session with its associated resources and lifecycle.
// It embeds context.Context to provide cancellation, deadlines, and value propagation.
type SessionContext struct {
	context.Context // Embedded context for lifecycle management

	SessionID      string
	ClientBox      *client.ClientBox
	CreatedAt      time.Time
	lastAccessedAt time.Time
	mu             sync.RWMutex
}

// NewSessionContext creates a new session context with the given parent context.
// The parent context is typically context.Background() for long-lived sessions,
// but can be any context for testing or request-scoped sessions.
func NewSessionContext(ctx context.Context, sessionID string, clientBox *client.ClientBox) *SessionContext {
	now := time.Now()
	return &SessionContext{
		Context:        ctx,
		SessionID:      sessionID,
		ClientBox:      clientBox,
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
