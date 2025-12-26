package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/codebraid-mcp/internal/client"
	"github.com/yousuf/codebraid-mcp/internal/codegen"
	"github.com/yousuf/codebraid-mcp/internal/config"
)

// toCamelCase converts snake_case or kebab-case to camelCase
func toCamelCase(s string) string {
	if len(s) == 0 {
		return s
	}

	// If already camelCase or PascalCase (no underscores/dashes/spaces), just ensure first char is lowercase
	if !strings.ContainsAny(s, "_- ") {
		return strings.ToLower(s[0:1]) + s[1:]
	}

	// Split by underscore, dash, or space
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == '_' || r == '-' || r == ' '
	})

	for i, part := range parts {
		if len(part) > 0 {
			if i == 0 {
				parts[i] = strings.ToLower(part)
			} else {
				parts[i] = strings.ToUpper(part[0:1]) + part[1:]
			}
		}
	}

	return strings.Join(parts, "")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Parse flags
	configPath := flag.String("config", os.Getenv("CODEBRAID_CONFIG"), "Path to config file")
	outputDir := flag.String("output-dir", "./generated", "Directory to write TypeScript files")
	serverFilter := flag.String("server", "", "Generate only for specific server(s), comma-separated")
	verbose := flag.Bool("verbose", false, "Enable verbose output")
	flag.Parse()

	ctx := context.Background()

	// Load config with auto-discovery
	if *verbose && *configPath != "" {
		fmt.Printf("Loading config from: %s\n", *configPath)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{
		ConfigPath:        *configPath,
		SearchPaths:       config.DefaultSearchPaths(),
		AllowEnvOverrides: true,
	})
	if err != nil {
		return fmt.Errorf("failed to load config: %w\n\nHint: Specify a config file with -config flag or CODEBRAID_CONFIG env var", err)
	}

	// Create McpClientHub and connect to MCP servers
	if *verbose {
		fmt.Println("Connecting to MCP servers...")
	}
	clientHub := client.NewMcpClientHub()
	if err := clientHub.Connect(ctx, cfg); err != nil {
		return fmt.Errorf("failed to connect to MCP servers: %w", err)
	}
	defer clientHub.Close()

	// Get all tools from connected servers
	allTools := clientHub.Tools()

	// Filter servers if requested
	var grouped map[string][]*mcp.Tool
	if *serverFilter != "" {
		// Parse and filter requested servers
		requestedServers := strings.Split(*serverFilter, ",")
		grouped = make(map[string][]*mcp.Tool)

		for _, serverName := range requestedServers {
			serverName = strings.TrimSpace(serverName)
			if tools, ok := allTools[serverName]; ok {
				grouped[serverName] = tools
			} else {
				fmt.Fprintf(os.Stderr, "Warning: server %q not found\n", serverName)
			}
		}

		if len(grouped) == 0 {
			return fmt.Errorf("none of the requested servers were found")
		}
	} else {
		grouped = allTools
	}

	if *verbose {
		serverNames := make([]string, 0, len(grouped))
		for name := range grouped {
			serverNames = append(serverNames, name)
		}
		fmt.Printf("Processing servers: %v\n", serverNames)
		fmt.Println("Discovering tools...")
		for name, tools := range grouped {
			fmt.Printf("  %s: %d tools\n", name, len(tools))
		}
	}

	if len(grouped) == 0 {
		return fmt.Errorf("no tools found")
	}

	// Create output directory
	if err := os.MkdirAll(*outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Generate TypeScript files
	generator := codegen.NewTypeScriptGenerator()

	generatedServers := make([]string, 0, len(grouped))
	totalFunctions := 0

	for serverName, tools := range grouped {
		if *verbose {
			fmt.Printf("Generating %s/ directory with %d functions...\n", serverName, len(tools))
		}

		// Create server directory
		serverDir := filepath.Join(*outputDir, serverName)
		if err := os.MkdirAll(serverDir, 0755); err != nil {
			return fmt.Errorf("failed to create server directory %s: %w", serverDir, err)
		}

		// Generate one file per function
		for _, tool := range tools {
			funcName := toCamelCase(tool.Name)

			content, err := generator.GenerateFunctionFile(serverName, tool)
			if err != nil {
				return fmt.Errorf("failed to generate function file for %s.%s: %w", serverName, tool.Name, err)
			}

			functionPath := filepath.Join(serverDir, funcName+".ts")
			if err := os.WriteFile(functionPath, []byte(content), 0644); err != nil {
				return fmt.Errorf("failed to write %s: %w", functionPath, err)
			}

			if *verbose {
				fmt.Printf("  - %s/%s.ts\n", serverName, funcName)
			}
		}

		// Generate server index.ts
		serverIndexContent := generator.GenerateServerIndexFile(serverName, tools)
		serverIndexPath := filepath.Join(serverDir, "index.ts")
		if err := os.WriteFile(serverIndexPath, []byte(serverIndexContent), 0644); err != nil {
			return fmt.Errorf("failed to write server index %s: %w", serverIndexPath, err)
		}

		generatedServers = append(generatedServers, serverName)
		totalFunctions += len(tools)
	}

	// Generate mcp-types.ts
	if *verbose {
		fmt.Println("\nGenerating mcp-types.ts...")
	}
	mcpTypesContent := generator.GenerateMCPTypesFile()
	mcpTypesPath := filepath.Join(*outputDir, "mcp-types.ts")
	if err := os.WriteFile(mcpTypesPath, []byte(mcpTypesContent), 0644); err != nil {
		return fmt.Errorf("failed to write mcp-types.ts: %w", err)
	}

	// Generate top-level index.ts
	if *verbose {
		fmt.Println("Generating index.ts...")
	}
	indexContent := generator.GenerateIndexFile(generatedServers)
	indexPath := filepath.Join(*outputDir, "index.ts")
	if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
		return fmt.Errorf("failed to write index.ts: %w", err)
	}

	fmt.Printf("\n✓ Successfully generated TypeScript definitions\n")
	fmt.Printf("  Servers: %d\n", len(generatedServers))
	fmt.Printf("  Functions: %d\n", totalFunctions)
	fmt.Printf("  Output directory: %s\n", *outputDir)
	fmt.Println("\nGenerated structure:")
	fmt.Println("  ./lib/")
	fmt.Println("  ├── mcp-types.ts")
	fmt.Println("  ├── index.ts")
	for _, server := range generatedServers {
		fmt.Printf("  └── %s/\n", server)
		fmt.Printf("      ├── index.ts\n")
		fmt.Printf("      └── [%d function files]\n", len(grouped[server]))
	}

	return nil
}
