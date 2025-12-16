package sandbox

import (
	"context"
	"fmt"

	extism "github.com/extism/go-sdk"
	"github.com/yousuf/codebraid-mcp/internal/client"
)

// Sandbox provides a WebAssembly execution environment for user code
type Sandbox struct {
	plugin      *extism.Plugin
	clientBox   *client.ClientBox
	ctx         context.Context
	transformer *TypeScriptTransformer
}

// NewSandbox creates a new sandbox instance
func NewSandbox(ctx context.Context, wasmPath string, clientBox *client.ClientBox) (*Sandbox, error) {
	manifest := extism.Manifest{
		Wasm: []extism.Wasm{
			extism.WasmFile{
				Path: wasmPath,
			},
		},
	}

	config := extism.PluginConfig{
		EnableWasi: true,
	}

	// Initialize TypeScript transformer
	transformer, err := NewTypeScriptTransformer()
	if err != nil {
		// Log warning but don't fail - TypeScript support is optional
		fmt.Printf("Warning: TypeScript transformer not available: %v\n", err)
	}

	sb := &Sandbox{
		clientBox:   clientBox,
		ctx:         ctx,
		transformer: transformer,
	}

	// Create host functions
	hostFunctions := []extism.HostFunction{
		createCallMcpToolHostFunc(sb),
	}

	plugin, err := extism.NewPlugin(ctx, manifest, config, hostFunctions)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin: %w", err)
	}

	sb.plugin = plugin
	return sb, nil
}

// ExecuteCode executes JavaScript code in the sandbox
// If the code appears to be TypeScript, it will be transformed first
func (s *Sandbox) ExecuteCode(code string) (string, error) {
	// Transform TypeScript to JavaScript if needed
	code = fmt.Sprintf("(async () => { %s })();", code)
	if s.transformer != nil {
		var err error
		code, err = s.transformer.Transform(code)
		if err != nil {
			return "", fmt.Errorf("TypeScript transformation failed: %w", err)
		}
	}

	// Call the executeCode function exported by the JavaScript plugin
	exit, output, err := s.plugin.Call("executeCode", []byte(code))
	if err != nil {
		return "", fmt.Errorf("plugin execution failed: %w", err)
	}
	if exit != 0 {
		return "", fmt.Errorf("plugin exited with code %d", exit)
	}

	return string(output), nil
}

// Close closes the sandbox and frees resources
func (s *Sandbox) Close() {
	if s.plugin != nil {
		s.plugin.Close(s.ctx)
	}
}
