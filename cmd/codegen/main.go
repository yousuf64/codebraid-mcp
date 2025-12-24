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

	for serverName, tools := range grouped {
		if *verbose {
			fmt.Printf("Generating %s.ts...\n", serverName)
		}

		content, err := generator.GenerateFile(serverName, tools)
		if err != nil {
			return fmt.Errorf("failed to generate file for %q: %w", serverName, err)
		}

		outputPath := filepath.Join(*outputDir, serverName+".ts")
		if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", outputPath, err)
		}

		generatedServers = append(generatedServers, serverName)
	}

	// Generate mcp-types.ts
	if *verbose {
		fmt.Println("Generating mcp-types.ts...")
	}
	mcpTypesContent := generator.GenerateMCPTypesFile()
	mcpTypesPath := filepath.Join(*outputDir, "mcp-types.ts")
	if err := os.WriteFile(mcpTypesPath, []byte(mcpTypesContent), 0644); err != nil {
		return fmt.Errorf("failed to write mcp-types.ts: %w", err)
	}

	// Generate index.ts
	if *verbose {
		fmt.Println("Generating index.ts...")
	}
	indexContent := generator.GenerateIndexFile(generatedServers)
	indexPath := filepath.Join(*outputDir, "index.ts")
	if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
		return fmt.Errorf("failed to write index.ts: %w", err)
	}

	fmt.Printf("\n✓ Successfully generated TypeScript definitions for %d servers\n", len(generatedServers))
	fmt.Printf("  Output directory: %s\n", *outputDir)
	fmt.Println("\nGenerated files:")
	fmt.Println("  - mcp-types.ts")
	fmt.Println("  - index.ts")
	for _, server := range generatedServers {
		fmt.Printf("  - %s.ts\n", server)
	}

	return nil
}
