package sandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TransformOptions configures the TypeScript transformation
type TransformOptions struct {
	Target string // e.g., "es2020"
	Module string // e.g., "es6", "commonjs"
}

// TypeScriptTransformer handles TypeScript to JavaScript transformation using SWC
type TypeScriptTransformer struct {
	swcPath string
	options TransformOptions
}

// NewTypeScriptTransformer creates a new transformer instance
func NewTypeScriptTransformer() (*TypeScriptTransformer, error) {
	// Try to find SWC in common locations
	swcPath, err := findSWC()
	if err != nil {
		return nil, fmt.Errorf("SWC not found: %w (install with: npm install -g @swc/cli @swc/core)", err)
	}

	return &TypeScriptTransformer{
		swcPath: swcPath,
		options: TransformOptions{
			Target: "es2020",
			Module: "es6",
		},
	}, nil
}

// findSWC attempts to locate the SWC executable
func findSWC() (string, error) {
	// Try common locations
	candidates := []string{
		"swc", // In PATH
		"npx", // Use npx to run @swc/cli
		filepath.Join(os.Getenv("HOME"), ".nvm", "versions", "node", "*", "bin", "swc"),
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

	return "", fmt.Errorf("swc executable not found")
}

// Transform converts TypeScript code to JavaScript
func (t *TypeScriptTransformer) Transform(code string) (string, error) {
	// Create SWC config
	config := map[string]interface{}{
		"jsc": map[string]interface{}{
			"parser": map[string]interface{}{
				"syntax":        "typescript",
				"tsx":           false,
				"decorators":    false,
				"dynamicImport": true,
			},
			"target": t.options.Target,
		},
		"module": map[string]interface{}{
			"type": t.options.Module,
		},
		"sourceMaps": false,
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("failed to create SWC config: %w", err)
	}

	// Create unique temporary directory for this transformation
	// This ensures parallel requests don't interfere with each other
	tmpDir, err := os.MkdirTemp("", "swc-transform-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	inputFile := filepath.Join(tmpDir, "input.ts")
	configFile := filepath.Join(tmpDir, ".swcrc")

	// Write input code
	if err := os.WriteFile(inputFile, []byte(code), 0644); err != nil {
		return "", fmt.Errorf("failed to write input file: %w", err)
	}

	// Write config
	if err := os.WriteFile(configFile, configJSON, 0644); err != nil {
		return "", fmt.Errorf("failed to write config file: %w", err)
	}

	// Execute SWC
	var cmd *exec.Cmd
	if t.swcPath == "npx" {
		cmd = exec.Command("npx", "-y", "@swc/cli", "compile", inputFile, "--config-file", configFile)
	} else {
		cmd = exec.Command(t.swcPath, "compile", inputFile, "--config-file", configFile)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("SWC transformation failed: %w\nStderr: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// IsTypeScript checks if the code appears to be TypeScript
func IsTypeScript(code string) bool {
	// Simple heuristics to detect TypeScript
	tsIndicators := []string{
		": string",
		": number",
		": boolean",
		": any",
		"interface ",
		"type ",
		"enum ",
		"<T>",
		"<T,",
		" as ",
		"readonly ",
		"public ",
		"private ",
		"protected ",
	}

	for _, indicator := range tsIndicators {
		if strings.Contains(code, indicator) {
			return true
		}
	}

	return false
}
