package server

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/codebraid-mcp/internal/session"
)

// sessionContextKey is the context key for storing session context
type contextKey string

const sessionContextKey contextKey = "session"

// getSessionFromContext retrieves the session context from the request context.
// SessionContext is stored as a value to keep request lifecycle separate from session lifecycle.
func getSessionFromContext(ctx context.Context) (*session.SessionContext, error) {
	sessionCtx, ok := ctx.Value(sessionContextKey).(*session.SessionContext)
	if !ok || sessionCtx == nil {
		return nil, fmt.Errorf("session context not found in request context")
	}
	return sessionCtx, nil
}

// createSessionInjectionMiddleware creates middleware that automatically manages session lifecycle.
// It stores SessionContext as a value in the request context, keeping request and session lifecycles separate.
func createSessionInjectionMiddleware(sessionMgr *session.Manager) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(
			ctx context.Context,
			method string,
			req mcp.Request,
		) (mcp.Result, error) {
			sessionID := req.GetSession().ID()

			// Get or create session context
			sessionCtx, err := sessionMgr.GetOrCreateSession(ctx, sessionID)
			if err != nil {
				return nil, fmt.Errorf("failed to get/create session: %w", err)
			}

			if sessionCtx == nil {
				return nil, fmt.Errorf("invalid session context")
			}

			// Update last accessed timestamp
			sessionCtx.UpdateLastAccessed()

			// Store SessionContext as value in request context
			// This keeps session lifecycle independent from request lifecycle
			ctx = context.WithValue(ctx, sessionContextKey, sessionCtx)

			// Pass request context (can be cancelled without affecting session)
			return next(ctx, method, req)
		}
	}
}

// createLoggingMiddleware creates middleware that logs all MCP method calls
func createLoggingMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(
			ctx context.Context,
			method string,
			req mcp.Request,
		) (mcp.Result, error) {
			start := time.Now()
			sessionID := req.GetSession().ID()

			// Log request details
			log.Printf("[REQUEST] Session: %s | Method: %s", sessionID, method)

			// Call the actual handler
			result, err := next(ctx, method, req)

			// Log response details
			duration := time.Since(start)

			if err != nil {
				log.Printf("[RESPONSE] Session: %s | Method: %s | Status: ERROR | Duration: %v | Error: %v",
					sessionID, method, duration, err)
			} else {
				log.Printf("[RESPONSE] Session: %s | Method: %s | Status: OK | Duration: %v",
					sessionID, method, duration)
			}

			return result, err
		}
	}
}
