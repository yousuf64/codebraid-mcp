package sandbox

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/yousuf/codebraid-mcp/internal/codegen"
)

// TypeScriptBundler handles TypeScript to JavaScript transformation using Rspack/SWC
type TypeScriptBundler struct {
	rspackPath string
	libs       map[string]string
}

// embeddedRspackConfig is the bundler configuration embedded in the binary
//
//go:embed rspack.config.ts
var embeddedRspackConfig string

// NewTypeScriptBundler creates a new bundler instance
func NewTypeScriptBundler(libs map[string]string) (*TypeScriptBundler, error) {
	// Try to find SWC in common locations
	rspackPath, err := findRspack()
	if err != nil {
		return nil, fmt.Errorf("SWC not found: %w (install with: npm install -g @rspack/cli @rspack/core)", err)
	}

	return &TypeScriptBundler{
		rspackPath: rspackPath,
		libs:       libs,
	}, nil
}

// findRspack attempts to locate the SWC executable
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

// Bundle bundles TypeScript modules into a single JavaScript file
func (t *TypeScriptBundler) Bundle(code string) (js string, sourceMap string, err error) {
	// Create unique temporary directory for this bundling
	// This ensures parallel requests don't interfere with each other
	tmpDir, err := os.MkdirTemp("", "rspack-transform-*")
	if err != nil {
		return "", "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	index := filepath.Join(tmpDir, "index.ts")
	configFile := filepath.Join(tmpDir, "rspack.config.ts")
	libDir := filepath.Join(tmpDir, "lib")
	mcpTypes := filepath.Join(libDir, "mcp-types.ts")
	mcpTypesFile := codegen.NewTypeScriptGenerator().GenerateMCPTypesFile()

	// Write input code to index.ts
	if err := os.WriteFile(index, []byte(code), 0644); err != nil {
		return "", "", fmt.Errorf("failed to write input file: %w", err)
	}

	// Write embedded rspack config
	if err := os.WriteFile(configFile, []byte(embeddedRspackConfig), 0644); err != nil {
		return "", "", fmt.Errorf("failed to write config file: %w", err)
	}

	// Create lib dir
	if err := os.Mkdir(libDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create lib dir: %w", err)
	}

	// Write MCP types
	if err := os.WriteFile(mcpTypes, []byte(mcpTypesFile), 0644); err != nil {
		return "", "", fmt.Errorf("failed to write config file: %w", err)
	}

	// Write libs
	for serverName, tsFile := range t.libs {
		libPath := filepath.Join(libDir, fmt.Sprintf("%s.ts", serverName))
		if err := os.WriteFile(libPath, []byte(tsFile), 0644); err != nil {
			return "", "", fmt.Errorf("failed to write config file: %w", err)
		}
	}

	outputDir := filepath.Join(tmpDir, "dist")

	// Execute Rspack
	var cmd *exec.Cmd
	if t.rspackPath == "npx" {
		cmd = exec.Command("npx", "-y", "@rspack/cli", "--entry", index, "--config", configFile, "--output-path", outputDir)
	} else {
		cmd = exec.Command(t.rspackPath, "--entry", index, "--config", configFile, "--output-path", outputDir)
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("Error: %w\nStderr: %s", err, stdout.String())
	}

	jsBytes, err := os.ReadFile(filepath.Join(outputDir, "main.js"))
	if err != nil {
		return "Failed to read output file", "", err
	}

	sourceMapBytes, err := os.ReadFile(filepath.Join(outputDir, "main.js.map"))
	if err != nil {
		return "", "Failed to read source map file", err
	}

	return string(jsBytes), string(sourceMapBytes), nil
}
