package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config represents the main configuration structure
type Config struct {
	Server     *ServerConfig              `json:"server,omitempty"`
	McpServers map[string]McpServerConfig `json:"mcpServers"`
}

// ServerConfig contains HTTP server settings
type ServerConfig struct {
	Port    int `json:"port,omitempty"`
	Timeout int `json:"timeout,omitempty"` // in seconds
}

// McpServerConfig is the interface for all MCP server configurations
type McpServerConfig struct {
	Type string `json:"type,omitempty"` // Optional: "stdio", "http", or "sse" - will be inferred if omitted

	// Stdio fields
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// HTTP/SSE fields
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// LoadOptions configures how configuration is loaded
type LoadOptions struct {
	// ConfigPath is the explicit path to the config file
	ConfigPath string

	// SearchPaths are default locations to search for config files
	// if ConfigPath is not provided
	SearchPaths []string

	// AllowEnvOverrides enables environment variable overrides
	AllowEnvOverrides bool
}

// DefaultSearchPaths returns common config file locations
func DefaultSearchPaths() []string {
	homeDir, _ := os.UserHomeDir()
	return []string{
		"codebraid.json",
		filepath.Join(homeDir, ".config", "codebraid", "config.json"),
		filepath.Join(homeDir, ".codebraid", "config.json"),
		"/etc/codebraid/config.json",
	}
}

// Load reads and parses the configuration file with default options
func Load(configPath string) (*Config, error) {
	return LoadWithOptions(LoadOptions{
		ConfigPath:        configPath,
		SearchPaths:       DefaultSearchPaths(),
		AllowEnvOverrides: true,
	})
}

// LoadWithOptions provides more control over configuration loading
func LoadWithOptions(opts LoadOptions) (*Config, error) {
	// Determine which config file to use
	configPath, err := resolveConfigPath(opts)
	if err != nil {
		return nil, err
	}

	// Read the config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %q: %w", configPath, err)
	}

	// Parse the config
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Expand ${VAR} syntax in config values
	expandEnvVars(&config)

	// Apply environment variable overrides
	if opts.AllowEnvOverrides {
		applyEnvOverrides(&config)
	}

	// Infer server types if not specified
	inferServerTypes(&config)

	// Validate the config
	if err := validate(&config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &config, nil
}

// resolveConfigPath determines which config file to use
func resolveConfigPath(opts LoadOptions) (string, error) {
	// If explicit path provided, use it
	if opts.ConfigPath != "" {
		if _, err := os.Stat(opts.ConfigPath); err != nil {
			return "", fmt.Errorf("config file not found at %q: %w", opts.ConfigPath, err)
		}
		return opts.ConfigPath, nil
	}

	// Search in default locations
	for _, path := range opts.SearchPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("no config file found. Searched: %s", strings.Join(opts.SearchPaths, ", "))
}

// expandEnvVars expands ${VAR} syntax in config values
func expandEnvVars(config *Config) {
	for name, server := range config.McpServers {
		server.Command = os.ExpandEnv(server.Command)
		server.URL = os.ExpandEnv(server.URL)
		server.Cwd = os.ExpandEnv(server.Cwd)

		// Expand in args
		for i, arg := range server.Args {
			server.Args[i] = os.ExpandEnv(arg)
		}

		// Expand in env vars
		for key, val := range server.Env {
			server.Env[key] = os.ExpandEnv(val)
		}

		// Expand in headers
		for key, val := range server.Headers {
			server.Headers[key] = os.ExpandEnv(val)
		}

		config.McpServers[name] = server
	}
}

// applyEnvOverrides allows environment variables to override config values
// Uses os.Environ() to discover all CODEBRAID_SERVER_* variables
//
// Patterns:
//
//	CODEBRAID_SERVER_<NAME>_TYPE=stdio
//	CODEBRAID_SERVER_<NAME>_COMMAND=node
//	CODEBRAID_SERVER_<NAME>_ARGS=arg1,arg2
//	CODEBRAID_SERVER_<NAME>_CWD=/path
//	CODEBRAID_SERVER_<NAME>_URL=https://...
//	CODEBRAID_SERVER_<NAME>_HEADER_<KEY>=value
//	CODEBRAID_SERVER_<NAME>_ENV_<KEY>=value
func applyEnvOverrides(config *Config) {
	const prefix = "CODEBRAID_SERVER_"

	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key, value := parts[0], parts[1]

		if !strings.HasPrefix(key, prefix) {
			continue
		}

		// Remove prefix: CODEBRAID_SERVER_GITHUB_URL -> GITHUB_URL
		remainder := strings.TrimPrefix(key, prefix)
		segments := strings.Split(remainder, "_")

		if len(segments) < 2 {
			continue
		}

		// Extract server name (first segment, lowercase)
		serverName := strings.ToLower(segments[0])

		// Get or create server config
		server, exists := config.McpServers[serverName]
		if !exists {
			server = McpServerConfig{}
		}

		// Property is everything after server name
		property := strings.Join(segments[1:], "_")

		// Apply the override
		applyServerOverride(&server, property, value)

		config.McpServers[serverName] = server
	}
}

// applyServerOverride applies a single environment variable override to a server config
func applyServerOverride(server *McpServerConfig, property, value string) {
	switch {
	case property == "TYPE":
		server.Type = value

	case property == "COMMAND":
		server.Command = value

	case property == "ARGS":
		// Comma-separated values
		server.Args = strings.Split(value, ",")
		for i := range server.Args {
			server.Args[i] = strings.TrimSpace(server.Args[i])
		}

	case property == "CWD":
		server.Cwd = value

	case property == "URL":
		server.URL = value

	case strings.HasPrefix(property, "HEADER_"):
		// HEADER_AUTHORIZATION -> Authorization header
		headerKey := strings.TrimPrefix(property, "HEADER_")
		if server.Headers == nil {
			server.Headers = make(map[string]string)
		}
		server.Headers[headerKey] = value

	case strings.HasPrefix(property, "ENV_"):
		// ENV_NODE_ENV -> NODE_ENV env var
		envKey := strings.TrimPrefix(property, "ENV_")
		if server.Env == nil {
			server.Env = make(map[string]string)
		}
		server.Env[envKey] = value
	}
}

// inferServerTypes infers the server type based on available fields if not explicitly set
func inferServerTypes(config *Config) {
	for name, server := range config.McpServers {
		if server.Type != "" {
			continue // Type already specified
		}

		// Infer based on fields
		hasCommand := server.Command != ""
		hasURL := server.URL != ""

		if hasCommand && hasURL {
			// Ambiguous - will be caught in validation
			continue
		} else if hasCommand {
			server.Type = "stdio"
		} else if hasURL {
			// Leave as empty - McpClientHub will try HTTP first, then SSE
			server.Type = ""
		}

		config.McpServers[name] = server
	}
}

// validate checks if the configuration is valid
func validate(config *Config) error {
	if len(config.McpServers) == 0 {
		return fmt.Errorf("no MCP servers configured")
	}

	for name, server := range config.McpServers {
		hasCommand := server.Command != ""
		hasURL := server.URL != ""

		// Check for ambiguous configuration
		if hasCommand && hasURL {
			return fmt.Errorf("server %q: cannot specify both 'command' and 'url' (ambiguous server type)", name)
		}

		// Check that at least one is specified
		if !hasCommand && !hasURL {
			return fmt.Errorf("server %q: must specify either 'command' (for stdio) or 'url' (for http/sse)", name)
		}

		// Validate type-specific fields
		if server.Type != "" {
			switch server.Type {
			case "stdio":
				if !hasCommand {
					return fmt.Errorf("server %q: 'command' is required for stdio type", name)
				}
			case "http", "sse":
				if !hasURL {
					return fmt.Errorf("server %q: 'url' is required for %s type", name, server.Type)
				}
			default:
				return fmt.Errorf("server %q: invalid type %q (must be stdio, http, or sse)", name, server.Type)
			}
		}
	}

	return nil
}

// GetServerPort returns the configured server port with fallback to default
func (c *Config) GetServerPort() int {
	if c.Server != nil && c.Server.Port > 0 {
		return c.Server.Port
	}
	return 3000 // Default port
}

// GetServerTimeout returns the configured timeout with fallback to default
func (c *Config) GetServerTimeout() int {
	if c.Server != nil && c.Server.Timeout > 0 {
		return c.Server.Timeout
	}
	return 30 // Default 30 seconds
}
