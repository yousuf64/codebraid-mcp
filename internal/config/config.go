package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Config represents the main configuration structure
type Config struct {
	Server     *ServerConfig              `json:"server,omitempty"`
	Runtime    *RuntimeConfig             `json:"runtime,omitempty"`
	McpServers map[string]McpServerConfig `json:"mcpServers"`

	// configDir stores the directory containing the config file (not serialized)
	// Used for resolving relative paths in mount configurations
	configDir string
}

// ServerConfig contains HTTP server settings
type ServerConfig struct {
	Port              int    `json:"port,omitempty"`
	Timeout           int    `json:"timeout,omitempty"`           // in seconds
	WasmPath          string `json:"wasmPath,omitempty"`          // Optional path to sandbox WASM file (defaults to embedded)
	SessionTimeout    int    `json:"sessionTimeout,omitempty"`    // Session idle timeout in minutes (default: 30, 0 = no timeout)
	SessionCleanupInt int    `json:"sessionCleanupInt,omitempty"` // Session cleanup check interval in minutes (default: 5)
}

// RuntimeConfig contains runtime execution settings
type RuntimeConfig struct {
	Language string        `json:"language,omitempty"` // "typescript" or "python" (default: "typescript")
	Python   *PythonConfig `json:"python,omitempty"`   // Python-specific configuration
	Mounts   []MountConfig `json:"mounts,omitempty"`   // Custom directory mounts
}

// PythonConfig contains Python runtime settings
type PythonConfig struct {
	Packages            []string `json:"packages,omitempty"`            // Python packages to preload via micropip
	EnableTopLevelAwait bool     `json:"enableTopLevelAwait,omitempty"` // Enable top-level await (default: true)
}

// MountConfig defines a directory mount
type MountConfig struct {
	Name         string `json:"name"`                   // Virtual name (e.g., "data", "cache")
	Description  string `json:"description,omitempty"`  // Optional description of mount purpose
	Path         string `json:"path"`                   // Host path (absolute or relative)
	ReadOnly     bool   `json:"readOnly,omitempty"`     // Default: false
	MaxFileSize  string `json:"maxFileSize,omitempty"`  // Default: "10MB" (human readable)
	MaxFiles     int    `json:"maxFiles,omitempty"`     // Default: 1000
	MaxTotalSize string `json:"maxTotalSize,omitempty"` // Default: "100MB"
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
		"runbyte.json",
		filepath.Join(homeDir, ".config", "runbyte", "config.json"),
		filepath.Join(homeDir, ".runbyte", "config.json"),
		"/etc/runbyte/config.json",
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

	// Store the config directory for resolving relative paths
	// Use absolute path to ensure it works regardless of cwd
	absConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute config path: %w", err)
	}
	config.configDir = filepath.Dir(absConfigPath)

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

	// Debug: Log the loaded MCP servers
	fmt.Printf("[CONFIG DEBUG] Loaded config from: %s\n", configPath)
	fmt.Printf("[CONFIG DEBUG] MCP Servers in config:\n")
	for name := range config.McpServers {
		fmt.Printf("[CONFIG DEBUG]   - %s\n", name)
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

// applyRuntimeOverrides applies environment variable overrides for runtime configuration
// Patterns:
//
//	RUNBYTE_RUNTIME_LANGUAGE=python
//	RUNBYTE_RUNTIME_PYTHON_PACKAGES=numpy,pandas,scipy
func applyRuntimeOverrides(config *Config) {
	// Initialize runtime config if not present
	if config.Runtime == nil {
		config.Runtime = &RuntimeConfig{}
	}

	// Override language
	if lang := os.Getenv("RUNBYTE_RUNTIME_LANGUAGE"); lang != "" {
		config.Runtime.Language = lang
	}

	// Override Python packages
	if packages := os.Getenv("RUNBYTE_RUNTIME_PYTHON_PACKAGES"); packages != "" {
		// Initialize Python config if not present
		if config.Runtime.Python == nil {
			config.Runtime.Python = &PythonConfig{}
		}

		// Parse comma-separated list
		pkgList := strings.Split(packages, ",")
		for i := range pkgList {
			pkgList[i] = strings.TrimSpace(pkgList[i])
		}
		config.Runtime.Python.Packages = pkgList
	}
}

// applyEnvOverrides allows environment variables to override config values
// Uses os.Environ() to discover all RUNBYTE_* variables
//
// Runtime Patterns:
//
//	RUNBYTE_RUNTIME_LANGUAGE=python
//	RUNBYTE_RUNTIME_PYTHON_PACKAGES=numpy,pandas,scipy
//
// Server Patterns:
//
//	RUNBYTE_SERVER_<NAME>_TYPE=stdio
//	RUNBYTE_SERVER_<NAME>_COMMAND=node
//	RUNBYTE_SERVER_<NAME>_ARGS=arg1,arg2
//	RUNBYTE_SERVER_<NAME>_CWD=/path
//	RUNBYTE_SERVER_<NAME>_URL=https://...
//	RUNBYTE_SERVER_<NAME>_HEADER_<KEY>=value
//	RUNBYTE_SERVER_<NAME>_ENV_<KEY>=value
func applyEnvOverrides(config *Config) {
	// Apply runtime overrides
	applyRuntimeOverrides(config)

	// Apply server overrides
	const prefix = "RUNBYTE_SERVER_"

	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key, value := parts[0], parts[1]

		if !strings.HasPrefix(key, prefix) {
			continue
		}

		// Remove prefix: RUNBYTE_SERVER_GITHUB_URL -> GITHUB_URL
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

	// Validate runtime configuration
	if config.Runtime != nil {
		if err := validateRuntime(config.Runtime); err != nil {
			return fmt.Errorf("invalid runtime config: %w", err)
		}
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

// validateRuntime validates runtime configuration
func validateRuntime(runtime *RuntimeConfig) error {
	// Validate language
	if runtime.Language != "" {
		switch runtime.Language {
		case "typescript", "python":
			// Valid languages
		default:
			return fmt.Errorf("invalid language %q (must be 'typescript' or 'python')", runtime.Language)
		}
	}

	// Validate Python-specific config
	if runtime.Python != nil {
		// Package names validation (basic check)
		for _, pkg := range runtime.Python.Packages {
			if pkg == "" {
				return fmt.Errorf("empty package name in python.packages")
			}
			// Could add more validation here (e.g., check for valid Python package names)
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

// GetWasmPath returns the configured WASM path, or empty string to use embedded
func (c *Config) GetWasmPath() string {
	if c.Server != nil {
		return c.Server.WasmPath
	}
	return ""
}

// GetSessionTimeout returns the configured session timeout in minutes with fallback to default
func (c *Config) GetSessionTimeout() int {
	if c.Server != nil && c.Server.SessionTimeout > 0 {
		return c.Server.SessionTimeout
	}
	return 30 // Default 30 minutes
}

// GetSessionCleanupInterval returns the configured cleanup interval in minutes with fallback to default
func (c *Config) GetSessionCleanupInterval() int {
	if c.Server != nil && c.Server.SessionCleanupInt > 0 {
		return c.Server.SessionCleanupInt
	}
	return 5 // Default 5 minutes
}

// GetLanguage returns the configured runtime language with fallback to default
func (c *Config) GetLanguage() string {
	if c.Runtime != nil && c.Runtime.Language != "" {
		return c.Runtime.Language
	}
	return "typescript" // Default language
}

// GetPythonPackages returns the list of Python packages to preload
func (c *Config) GetPythonPackages() []string {
	if c.Runtime != nil && c.Runtime.Python != nil {
		return c.Runtime.Python.Packages
	}
	return []string{}
}

// GetPythonTopLevelAwait returns whether top-level await is enabled for Python
func (c *Config) GetPythonTopLevelAwait() bool {
	if c.Runtime != nil && c.Runtime.Python != nil {
		return c.Runtime.Python.EnableTopLevelAwait
	}
	return true // Default: enabled
}

// ParseSize converts human-readable size strings to bytes
// Examples: "10MB" -> 10485760, "1GB" -> 1073741824, "500KB" -> 512000
func ParseSize(size string) (int64, error) {
	if size == "" {
		return 0, fmt.Errorf("empty size string")
	}

	// Regular expression to parse size strings like "10MB", "1.5GB", etc.
	re := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([KMGT]?B?)$`)
	matches := re.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(size)))

	if matches == nil {
		return 0, fmt.Errorf("invalid size format: %s (expected formats: 10MB, 1GB, 500KB)", size)
	}

	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric value: %s", matches[1])
	}

	unit := matches[2]
	var multiplier int64 = 1

	switch unit {
	case "B", "":
		multiplier = 1
	case "KB", "K":
		multiplier = 1024
	case "MB", "M":
		multiplier = 1024 * 1024
	case "GB", "G":
		multiplier = 1024 * 1024 * 1024
	case "TB", "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("invalid size unit: %s", unit)
	}

	return int64(value * float64(multiplier)), nil
}

// ResolvePath resolves a mount path to an absolute path
// Supports:
// - Absolute paths: /Users/me/data
// - Relative to config file: ./data or ../shared/data
// - Home directory: ~/Documents/project-data
func (m *MountConfig) ResolvePath(configDir string) (string, error) {
	path := m.Path

	// Handle home directory expansion
	if strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		path = filepath.Join(homeDir, path[2:])
	}

	// If already absolute, return as-is
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}

	// Relative path - resolve based on config directory
	if configDir == "" {
		// No config directory provided, use current working directory
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get current directory: %w", err)
		}
		configDir = cwd
	}

	absPath := filepath.Join(configDir, path)
	return filepath.Clean(absPath), nil
}

// GetMounts returns the list of directory mounts
func (c *Config) GetMounts() []MountConfig {
	if c.Runtime != nil {
		return c.Runtime.Mounts
	}
	return []MountConfig{}
}

// GetConfigDir returns the directory containing the config file
func (c *Config) GetConfigDir() string {
	return c.configDir
}

// GetMcpServerNames returns the names of all configured MCP servers
func (c *Config) GetMcpServerNames() []string {
	names := make([]string, 0, len(c.McpServers))
	for name := range c.McpServers {
		names = append(names, name)
	}
	return names
}
