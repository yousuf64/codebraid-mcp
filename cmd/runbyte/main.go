package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/runbyte/internal/bundler"
	"github.com/yousuf/runbyte/internal/config"
	"github.com/yousuf/runbyte/internal/runtime"
	"github.com/yousuf/runbyte/internal/sandbox"
	"github.com/yousuf/runbyte/internal/server"
	"github.com/yousuf/runbyte/internal/session"
	"github.com/yousuf/runbyte/pkg/wasm"
)

// getWasmBytes returns WASM bytes based on language configuration
func getWasmBytes(cfg *config.Config) ([]byte, error) {
	// Check for config override (explicit WASM path)
	if wasmPath := cfg.GetWasmPath(); wasmPath != "" {
		data, err := os.ReadFile(wasmPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read WASM from config path %q: %w", wasmPath, err)
		}
		return data, nil
	}

	// Determine which WASM to load based on language setting
	lang, ok := runtime.ParseLanguage(cfg.GetLanguage())
	if !ok {
		return nil, fmt.Errorf("unsupported language: %s", cfg.GetLanguage())
	}

	// Only TypeScript WASM is supported
	// Python uses Node.js runtime server (see internal/pythonruntime)
	if lang != runtime.LanguageTypeScript {
		return nil, fmt.Errorf("only TypeScript WASM is supported, got: %s (Python uses Node.js runtime)", lang)
	}

	// Use embedded TypeScript WASM
	if len(wasm.Embedded) == 0 {
		return nil, fmt.Errorf("embedded TypeScript WASM not found - binary may not be built correctly")
	}
	return wasm.Embedded, nil
}

func runStdioServer(wasmBytes []byte, sessionMgr *session.Manager) {
	log.Println("Runbyte server running in stdio mode")

	// Create MCP server
	mcpServer := server.NewMcpServer(wasmBytes, sessionMgr)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Run server in goroutine
	errChan := make(chan error, 1)
	go func() {
		// Run blocks until connection is closed or context is cancelled
		errChan <- mcpServer.Run(ctx, &mcp.StdioTransport{})
	}()

	// Wait for either completion or interrupt
	select {
	case <-sigChan:
		log.Println("Interrupt received, shutting down...")
		cancel()
		// Wait for server to stop
		<-errChan
	case err := <-errChan:
		if err != nil {
			log.Printf("Server stopped with error: %v", err)
		}
	}

	// Close all sessions
	if err := sessionMgr.CloseAll(); err != nil {
		log.Printf("Error closing sessions: %v", err)
	}

	log.Println("Server stopped")
}

func runHttpServer(cfg *config.Config, wasmBytes []byte, sessionMgr *session.Manager, port int) {
	// Create a single MCP server instance to be reused across requests
	// This maintains session state properly
	mcpServer := server.NewMcpServer(wasmBytes, sessionMgr)

	// Create HTTP handler
	handler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		// Return the same server instance for all requests
		// The SDK handles session management internally
		return mcpServer
	}, &mcp.StreamableHTTPOptions{
		Stateless:      true, // MCP SDK doesn't manage sessions - Runbyte handles sessions via session.Manager
		JSONResponse:   false,
		Logger:         nil,
		EventStore:     nil,
		SessionTimeout: 0,
	})

	// Setup HTTP server
	timeout := time.Duration(cfg.GetServerTimeout()) * time.Second
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      handler,
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
		IdleTimeout:  timeout * 4,
	}

	// Start server in a goroutine
	go func() {
		log.Printf("Runbyte server listening on port %d", port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down server...")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}

	// Close all sessions
	if err := sessionMgr.CloseAll(); err != nil {
		log.Printf("Error closing sessions: %v", err)
	}

	log.Println("Server stopped")
}

func main() {
	// Parse command-line flags
	var (
		configPath    = flag.String("config", os.Getenv("RUNBYTE_CONFIG"), "Path to configuration file")
		portFlag      = flag.Int("port", 0, "HTTP server port (overrides config file)")
		transportMode = flag.String("transport", "http", "Transport mode: stdio or http")
		help          = flag.Bool("help", false, "Show usage information")
	)
	flag.Parse()

	if *help {
		flag.Usage()
		os.Exit(0)
	}

	// Load configuration with flexible options
	cfg, err := config.LoadWithOptions(config.LoadOptions{
		ConfigPath:        *configPath,
		SearchPaths:       config.DefaultSearchPaths(),
		AllowEnvOverrides: true,
	})
	if err != nil {
		log.Fatalf("Failed to load config: %v\n\nHint: Specify a config file with -config flag or RUNBYTE_CONFIG env var", err)
	}

	log.Printf("Loaded configuration with %d MCP server(s)", len(cfg.McpServers))

	// Initialize bundler
	if err = bundler.Initialize(); err != nil {
		log.Fatalf("Failed to initialize bundler: %v\n\nHint: Install rspack with: npm install -g @rspack/cli @rspack/core", err)
	}
	log.Println("Bundler initialized successfully")

	// Create session manager
	sessionMgr := session.NewManager(cfg)

	// Start MCP callback server for Python runtime tool calls
	// This server routes tool calls from Python runtimes back to MCP servers
	ctx := context.Background()
	mcpCallbackPort, err := sandbox.StartMcpCallbackServer(ctx, func(sessionID string) (sandbox.McpToolCaller, error) {
		sess := sessionMgr.GetSession(sessionID)
		if sess == nil {
			return nil, fmt.Errorf("session not found: %s", sessionID)
		}
		return sess.ClientHub, nil
	})
	if err != nil {
		log.Fatalf("Failed to start MCP callback server: %v", err)
	}
	log.Printf("MCP callback server started on port %d", mcpCallbackPort)

	// Set the callback port in session manager
	sessionMgr.SetMcpCallbackPort(mcpCallbackPort)

	// Start session cleanup worker
	sessionTimeout := time.Duration(cfg.GetSessionTimeout()) * time.Minute
	cleanupInterval := time.Duration(cfg.GetSessionCleanupInterval()) * time.Minute
	if sessionTimeout > 0 {
		log.Printf("Starting session cleanup worker (timeout: %v, interval: %v)", sessionTimeout, cleanupInterval)
		sessionMgr.StartCleanupWorker(cleanupInterval, sessionTimeout)
	} else {
		log.Println("Session timeout disabled (sessionTimeout = 0)")
	}

	// Load WASM bytes (only for TypeScript runtime)
	// Python uses Node.js runtime server instead
	var wasmBytes []byte
	lang, _ := runtime.ParseLanguage(cfg.GetLanguage())
	if lang == runtime.LanguageTypeScript {
		var err error
		wasmBytes, err = getWasmBytes(cfg)
		if err != nil {
			log.Fatalf("Failed to load WASM: %v", err)
		}
		log.Println("TypeScript WASM runtime loaded")
	} else {
		log.Println("Python runtime mode - using Node.js server (no WASM)")
	}

	// Route to appropriate transport mode
	switch *transportMode {
	case "stdio":
		runStdioServer(wasmBytes, sessionMgr)
	case "http":
		// Determine server port (priority: flag > env > config > default)
		port := *portFlag
		if port == 0 {
			if envPort := os.Getenv("RUNBYTE_PORT"); envPort != "" {
				fmt.Sscanf(envPort, "%d", &port)
			}
		}
		if port == 0 {
			port = cfg.GetServerPort()
		}
		runHttpServer(cfg, wasmBytes, sessionMgr, port)
	default:
		log.Fatalf("Invalid transport mode: %s (must be 'stdio' or 'http')", *transportMode)
	}
}
