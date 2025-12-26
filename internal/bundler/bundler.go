package bundler

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

var (
	globalRspackPath string
	rspackInitOnce   sync.Once
	rspackInitError  error
)

// Bundler handles TypeScript to JavaScript transformation using Rspack/SWC
type Bundler struct {
	rspackPath string
}

// embeddedRspackConfig is the bundler configuration embedded in the binary
//
//go:embed rspack.config.ts
var embeddedRspackConfig string

// Initialize finds and caches the rspack executable path
// Should be called once at application startup
func Initialize() error {
	rspackInitOnce.Do(func() {
		globalRspackPath, rspackInitError = findRspack()
	})
	return rspackInitError
}

// GetRspackPath returns the cached rspack path
func GetRspackPath() (string, error) {
	if globalRspackPath == "" {
		return "", fmt.Errorf("rspack not initialized - call Initialize() first")
	}
	return globalRspackPath, nil
}

// New creates a new bundler instance with pre-located rspack
func New() (*Bundler, error) {
	rspackPath, err := GetRspackPath()
	if err != nil {
		return nil, err
	}

	return &Bundler{
		rspackPath: rspackPath,
	}, nil
}

// GetEmbeddedConfig returns the embedded rspack configuration
func GetEmbeddedConfig() string {
	return embeddedRspackConfig
}

// findRspack attempts to locate the rspack executable
func findRspack() (string, error) {
	// Try common locations
	candidates := []string{
		"rspack", // In PATH
		"npx",    // Use npx to run @rspack/cli
		filepath.Join(os.Getenv("HOME"), ".nvm", "versions", "node", "*", "bin", "rspack"),
	}

	for _, candidate := range candidates {
		if candidate == "npx" {
			// Check if npx is available
			if _, err := exec.LookPath("npx"); err == nil {
				return "npx", nil
			}
		} else {
			if path, err := exec.LookPath(candidate); err == nil {
				return path, nil
			}
		}
	}

	return "", fmt.Errorf("rspack executable not found")
}

// BundleWithSession bundles TypeScript code using a session's bundle directory
// This allows reuse of server library files across multiple requests in the same session
func (b *Bundler) BundleWithSession(sessionBundleDir, code string) (js string, sourceMap string, err error) {
	// Create unique work directory for this request
	workID, err := generateWorkID()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate work ID: %w", err)
	}

	workDir := filepath.Join(sessionBundleDir, "work", workID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	// Symlink to shared servers directory
	serversSrc := filepath.Join(sessionBundleDir, "servers")
	serversDst := filepath.Join(workDir, "servers")
	if err := os.Symlink(serversSrc, serversDst); err != nil {
		return "", "", fmt.Errorf("failed to create servers symlink: %w", err)
	}

	// Write user code
	indexPath := filepath.Join(workDir, "index.ts")
	if err := os.WriteFile(indexPath, []byte(code), 0644); err != nil {
		return "", "", fmt.Errorf("failed to write user code: %w", err)
	}

	// Use session-level config (absolute path)
	configPath := filepath.Join(sessionBundleDir, "rspack.config.ts")
	outputDir := filepath.Join(workDir, "dist")

	// Execute Rspack
	var cmd *exec.Cmd
	if b.rspackPath == "npx" {
		cmd = exec.Command("npx", "-y", "@rspack/cli", "--entry", indexPath, "--config", configPath, "--output-path", outputDir)
	} else {
		cmd = exec.Command(b.rspackPath, "--entry", indexPath, "--config", configPath, "--output-path", outputDir)
	}

	var stdout bytes.Buffer
	cmd.Dir = workDir
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("rspack failed: %w\nOutput: %s", err, stdout.String())
	}

	// Read outputs
	jsBytes, err := os.ReadFile(filepath.Join(outputDir, "main.js"))
	if err != nil {
		return "", "", fmt.Errorf("failed to read bundled JS: %w", err)
	}

	sourceMapBytes, err := os.ReadFile(filepath.Join(outputDir, "main.js.map"))
	if err != nil {
		return "", "", fmt.Errorf("failed to read source map: %w", err)
	}

	return string(jsBytes), string(sourceMapBytes), nil
}

// generateWorkID creates a unique identifier for a work directory
func generateWorkID() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
