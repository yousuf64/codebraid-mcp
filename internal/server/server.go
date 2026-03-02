package server

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yousuf/runbyte/internal/bundler"
	"github.com/yousuf/runbyte/internal/config"
	"github.com/yousuf/runbyte/internal/sandbox"
	"github.com/yousuf/runbyte/internal/session"
	"github.com/yousuf/runbyte/internal/strutil"
)

// ExecuteCodeArgs represents the arguments for the execute_code tool
type ExecuteCodeArgs struct {
	Code string `json:"code" jsonschema:"Code to execute in sandbox"`
}

// ListDirectoryArgs represents the arguments for the list_directory tool
type ListDirectoryArgs struct {
	Path             string `json:"path" jsonschema:"Path to directory (e.g., '/', '/servers', '/servers/github'). Defaults to '/' if not provided."`
	WithDescriptions bool   `json:"withDescriptions,omitempty" jsonschema:"Include descriptions for functions (default: false)"`
}

// ReadFileArgs represents the arguments for the read_file tool
type ReadFileArgs struct {
	Path string `json:"path" jsonschema:"Required. Path to file in virtual filesystem (e.g., '/servers/github/listRepos.ts', '/servers/github/index.ts')"`
}

// getExecuteCodeDescription returns the tool description based on language
func getExecuteCodeDescription(language string) string {
	if language == "python" {
		return getPythonExecuteCodeDescription()
	}
	return getTypeScriptExecuteCodeDescription()
}

// getTypeScriptExecuteCodeDescription returns the TypeScript-specific tool description
func getTypeScriptExecuteCodeDescription() string {
	return `Execute TypeScript code that calls MCP tools as functions. Process data in the sandbox, chain multiple operations, and use the workspace for multi-step workflows.

Your code must export an exec() function as the entry point:

    async function exec() {
        // Your workflow here
        return result;
    }

The exec() function:
- Required entry point (async or sync)
- Returns any JSON-serializable value
- Has access to all MCP servers as typed modules
- Can read/write workspace for persistent data

Import MCP servers as TypeScript modules:
    import * as github from './servers/github';
    import * as slack from './servers/slack';
    import * as fs from '@runbyte/fs';

Single-step example:
    import * as github from './servers/github';
    
    async function exec() {
        const repos = await github.listRepos({ owner: "octocat" });
        return repos.filter(r => r.stargazers_count > 100);
    }

Multi-step workflow example (process in sandbox, store intermediate results):
    import * as github from './servers/github';
    import * as slack from './servers/slack';
    import * as fs from '@runbyte/fs';
    
    async function exec() {
        // Fetch data
        const repos = await github.listRepos({ owner: "myorg" });
        
        // Process in sandbox (doesn't pass through your context)
        const active = repos.filter(r => r.open_issues_count > 5);
        
        // Store full data for later
        fs.writeFile("./workspace/repos.json", JSON.stringify(active));
        
        // Get detailed info for each
        const details = [];
        for (const repo of active) {
            const issues = await github.listIssues({ 
                owner: "myorg", 
                repo: repo.name 
            });
            details.push({ name: repo.name, issueCount: issues.length });
        }
        
        // Notify team
        await slack.postMessage({
            channel: "#eng",
            text: ` + "`" + `Found ${active.length} repos with issues` + "`" + `
        });
        
        // Return only summary (not all data)
        return { summary: details };
    }

Filesystem API for workflows:
    import * as fs from '@runbyte/fs';
    
    // All operations use the workspace/ directory
    fs.writeFile("./workspace/data.json", jsonString);
    const content = fs.readFile("./workspace/data.json");
    const files = fs.listFiles("./workspace");
    fs.deleteFile("./workspace/old.json");

Sandbox environment:
- Execution timeout: 30 seconds
- Automatic bundling with TypeScript support
- No Node.js built-ins or DOM APIs
- All MCP tool calls are async
`
}

// getPythonExecuteCodeDescription returns the Python-specific tool description
func getPythonExecuteCodeDescription() string {
	return `Execute Python code that calls MCP tools as functions. Process data in the sandbox, chain multiple operations, and use the workspace for multi-step workflows.

Your code is executed directly - no wrapper function required:

    # Code executes top to bottom
    data = fetch_something()
    processed = transform(data)
    
    # Last expression is returned (Jupyter-style)
    processed  # ✓ This returns the value
    
    # NOTE: Assignments don't return values
    result = 200 * 200  # ✗ This returns None
    200 * 200           # ✓ This returns 40000

Entry point:
- Code executes from top to bottom
- Last expression is automatically returned (like Jupyter/REPL)
- To return a variable, put it on the last line without assignment
- All MCP tools are available as async functions
- Use 'await' for async MCP tool calls

Import MCP servers as Python modules:
    from servers.github.github import list_repos, create_issue
    from servers.slack.slack import post_message

Single-step example:
    from servers.github.github import list_repos
    
    repos = await list_repos({"owner": "octocat", "type": "public"})
    # Return the filtered list (last expression)
    [r for r in repos if r["stargazers_count"] > 100]

Multi-step workflow example (process in sandbox, store intermediate results):
    from servers.github.github import list_repos, list_issues
    from servers.slack.slack import post_message
    import json
    
    # Fetch data
    repos = await list_repos({"owner": "myorg"})
    
    # Process in sandbox (doesn't pass through your context)
    active = [r for r in repos if r["open_issues_count"] > 5]
    
    # Store full data for later using standard Python I/O
    with open("./workspace/repos.json", "w") as f:
        json.dump(active, f)
    
    # Get detailed info for each
    details = []
    for repo in active:
        issues = await list_issues({
            "owner": "myorg",
            "repo": repo["name"]
        })
        details.append({"name": repo["name"], "issueCount": len(issues)})
    
    # Notify team
    await post_message({
        "channel": "#eng",
        "text": f"Found {len(active)} repos with issues"
    })
    
    # Return summary (last expression, no assignment)
    {"summary": details}

Workspace filesystem access (standard Python I/O):
    import json
    import os
    
    # Write files to workspace
    with open("./workspace/data.json", "w") as f:
        json.dump({"key": "value"}, f)
    
    # Read files from workspace
    with open("./workspace/data.json", "r") as f:
        data = json.load(f)
    
    # List workspace contents
    files = os.listdir("./workspace")
    
    # Check if file exists
    if os.path.exists("./workspace/data.json"):
        os.remove("./workspace/data.json")
    
    # Create subdirectories
    os.makedirs("./workspace/cache", exist_ok=True)

Sandbox environment:
- Execution timeout: 30 seconds
- Pyodide runtime with standard library
- Working directory: /session (use ./workspace/ for persistent storage)
- All MCP tool calls require 'await'
`
}

// getServerInstructions returns language-specific server instructions
func getServerInstructions(language string, packages []string, serverNames []string, mounts []config.MountConfig) string {
	if language == "python" {
		return getPythonServerInstructions(packages, serverNames, mounts)
	}
	return getTypeScriptServerInstructions(serverNames, mounts)
}

// buildFilesystemTree generates the filesystem tree string for instructions
func buildFilesystemTree(serverNames []string, mounts []config.MountConfig) string {
	tree := "/\n├── servers/              MCP tools compiled to TypeScript\n"

	// Add configured MCP servers
	for i, name := range serverNames {
		isLast := i == len(serverNames)-1 && len(mounts) == 0
		prefix := "│   "
		if isLast {
			tree += "│   └── " + name + "/\n"
		} else {
			tree += "│   ├── " + name + "/\n"
		}
		_ = prefix
	}

	// Add workspace
	if len(mounts) > 0 {
		tree += "├── workspace/           Your persistent workspace (read/write)\n"
	} else {
		tree += "└── workspace/           Your persistent workspace (read/write)\n"
	}

	// Add mounted directories
	for i, mount := range mounts {
		isLast := i == len(mounts)-1
		access := "(read/write)"
		if mount.ReadOnly {
			access = "(read-only)"
		}
		if isLast {
			tree += "└── " + mount.Name + "/              " + mount.Description + " " + access + "\n"
		} else {
			tree += "├── " + mount.Name + "/              " + mount.Description + " " + access + "\n"
		}
	}

	return tree
}

// getTypeScriptServerInstructions returns TypeScript-specific server instructions
func getTypeScriptServerInstructions(serverNames []string, mounts []config.MountConfig) string {
	fsTree := buildFilesystemTree(serverNames, mounts)
	return `
# Runbyte: Execute MCP Tools as TypeScript Code

Runbyte implements the code execution pattern for MCP, enabling you to call any MCP tool by writing TypeScript code instead of traditional tool calling. This dramatically reduces token consumption (up to 98.7%) for complex workflows by processing data in a sandboxed environment rather than passing everything through your context.

## Virtual Filesystem Architecture

All MCP servers are exposed as TypeScript modules in a virtual filesystem. Explore this filesystem to discover what's available:

` + fsTree + `

## Efficient Discovery Pattern

Don't load all tools upfront. Discover on-demand:

1. **Start at root**: list_directory({ path: "/" })
2. **Browse servers**: list_directory({ path: "/servers" })
3. **Explore specific server**: list_directory({ path: "/servers/github" })
4. **Read tool signatures**: read_file({ path: "/servers/github/createIssue.ts" })

Each tool file contains complete TypeScript types, JSDoc, and function signatures. Read only what you need.

## Code Execution Pattern

Write TypeScript code that calls MCP tools as normal async functions. All code must export an exec() function as the entry point:

### Simple Example: Call One Tool
` + "```typescript" + `
import * as github from './servers/github';

async function exec() {
  const repos = await github.listRepos({ 
    owner: "octocat",
    type: "public"
  });
  
  return repos.filter(r => r.stargazers_count > 100);
}
` + "```" + `

### Realistic Example: Multi-Step Workflow with Workspace
` + "```typescript" + `
import * as github from './servers/github';
import * as slack from './servers/slack';
import * as fs from '@runbyte/fs';

async function exec() {
  // Step 1: Fetch all repos
  const repos = await github.listRepos({ owner: "myorg" });
  
  // Step 2: Process and filter (happens in sandbox, not in your context)
  const criticalRepos = repos.filter(r => 
    r.open_issues_count > 10 && 
    r.pushed_at > Date.now() - 7 * 24 * 60 * 60 * 1000
  );
  
  // Step 3: Get detailed issues for each repo
  const repoIssues = [];
  for (const repo of criticalRepos) {
    const issues = await github.listIssues({ 
      owner: "myorg", 
      repo: repo.name,
      state: "open"
    });
    repoIssues.push({ repo: repo.name, issues });
  }
  
  // Step 4: Store full results in workspace for later analysis
  fs.writeFile(
    "./workspace/issues-report.json", 
    JSON.stringify(repoIssues, null, 2)
  );
  
  // Step 5: Generate summary for Slack (only summary goes through your context)
  const summary = ` + "`" + `Found ${criticalRepos.length} repos needing attention with ${
    repoIssues.reduce((sum, r) => sum + r.issues.length, 0)
  } total open issues.` + "`" + `;
  
  await slack.postMessage({
    channel: "#dev-alerts",
    text: summary
  });
  
  return { summary, criticalRepos: criticalRepos.length };
}
` + "```" + `

Notice: Full repo and issue data never passes through your context. Only the final summary does.

IMPORTANT: Since most MCP server tools do not explicitly document their output schema (return type), learn and memorize their return types from the interactions.  

### Complex Example: Data Aggregation with State Management
` + "```typescript" + `
import * as github from './servers/github';
import * as filesystem from './servers/filesystem';
import * as fs from '@runbyte/fs';

async function exec() {
  // Check if we have cached results from previous run
  try {
    const cached = fs.readFile("./workspace/metrics.json");
    const data = JSON.parse(cached);
    if (Date.now() - data.timestamp < 3600000) { // 1 hour
      return data.metrics;
    }
  } catch {}
  
  // No cache or expired - fetch and compute
  const repos = await github.listRepos({ owner: "myorg" });
  
  const metrics = {
    total: repos.length,
    byLanguage: repos.reduce((acc, r) => {
      acc[r.language] = (acc[r.language] || 0) + 1;
      return acc;
    }, {}),
    avgStars: repos.reduce((sum, r) => sum + r.stargazers_count, 0) / repos.length,
    recentlyUpdated: repos.filter(r => 
      new Date(r.updated_at) > new Date(Date.now() - 30 * 24 * 60 * 60 * 1000)
    ).length
  };
  
  // Store for next time
  fs.writeFile("./workspace/metrics.json", JSON.stringify({
    timestamp: Date.now(),
    metrics
  }));
  
  // Also append to history log
  const logEntry = ` + "`" + `[${new Date().toISOString()}] Metrics: ${JSON.stringify(metrics)}\n` + "`" + `;
  try {
    const existing = fs.readFile("./workspace/metrics-history.log");
    fs.writeFile("./workspace/metrics-history.log", existing + logEntry);
  } catch {
    fs.writeFile("./workspace/metrics-history.log", logEntry);
  }
  
  return metrics;
}
` + "```" + `

## Available Tools

- **list_directory** - Explore the virtual filesystem to discover MCP servers and tools
- **read_file** - Read tool definitions, workspace files, or cached data
- **execute_code** - Run your TypeScript code with automatic bundling

## Filesystem API (@runbyte/fs)

For multi-step workflows, use the workspace to store intermediate results:

` + "```typescript" + `
import * as fs from '@runbyte/fs';

// Write files to workspace
fs.writeFile("./workspace/data.json", JSON.stringify(data));

// Read files from workspace
const content = fs.readFile("./workspace/data.json");

// List workspace contents
const files = fs.listFiles("./workspace");

// Delete files from workspace
fs.deleteFile("./workspace/old-data.json");
` + "```" + `

## Key Principles

1. **Discover efficiently**: Start broad, narrow down. Don't read all tools.
2. **Process in sandbox**: Loops, filtering, aggregation happen in exec(), not in your context.
3. **Use workspace strategically**: Store full datasets, keep summaries for your context.
4. **Chain operations**: Call multiple MCP tools in sequence within one exec().
5. **Return what matters**: Only final results/summaries should return to your context.

## Technical Details

- All paths start with '/'
- Namespace imports work best: ` + "`" + `import * as github from './servers/github'` + "`" + `
- exec() can be sync or async
- Execution timeout: 30 seconds
- Automatic bundling with full TypeScript support
- Each tool file has complete type definitions and JSDoc
`
}

// getPythonServerInstructions returns Python-specific server instructions
func getPythonServerInstructions(packages []string, serverNames []string, mounts []config.MountConfig) string {
	packagesSection := ""
	if len(packages) > 0 {
		packagesSection = "\n\n## Available Python Packages\n\nThe following third-party Python packages are pre-installed and available for import:\n\n"
		for _, pkg := range packages {
			packagesSection += "- " + pkg + "\n"
		}
		packagesSection += "\nYou can import and use these packages directly in your code. The Python standard library is always available.\n"
	}

	fsTree := buildFilesystemTree(serverNames, mounts)
	return `
# Runbyte: Execute MCP Tools as Python Code

Runbyte implements the code execution pattern for MCP, enabling you to call any MCP tool by writing Python code instead of traditional tool calling. This dramatically reduces token consumption (up to 98.7%) for complex workflows by processing data in a sandboxed environment rather than passing everything through your context.

## Virtual Filesystem Architecture

All MCP servers are exposed as Python modules in a virtual filesystem. Explore this filesystem to discover what's available:

` + fsTree + `

## Efficient Discovery Pattern

Don't load all tools upfront. Discover on-demand:

1. **Start at root**: list_directory({ path: "/" })
2. **Browse servers**: list_directory({ path: "/servers" })
3. **Explore specific server**: list_directory({ path: "/servers/github" })
4. **Read tool signatures**: read_file({ path: "/servers/github/list_repos.py" })

Each tool file contains complete Python type hints, docstrings, and function signatures. Read only what you need.

## Code Execution Pattern

Write Python code that calls MCP tools as normal async functions. Your code executes directly (Jupyter-style):

### Simple Example: Call One Tool
` + "```python" + `
from servers.github.github import list_repos

repos = await list_repos({"owner": "octocat", "type": "public"})

# Last expression is returned
[r for r in repos if r["stargazers_count"] > 100]
` + "```" + `

### Realistic Example: Multi-Step Workflow with Workspace
` + "```python" + `
from servers.github.github import list_repos, list_issues
from servers.slack.slack import post_message
import json
from datetime import datetime, timedelta

# Step 1: Fetch all repos
repos = await list_repos({"owner": "myorg"})

# Step 2: Process and filter (happens in sandbox, not in your context)
week_ago = datetime.now() - timedelta(days=7)
critical_repos = [
    r for r in repos 
    if r["open_issues_count"] > 10 and 
       datetime.fromisoformat(r["pushed_at"].replace("Z", "+00:00")) > week_ago
]

# Step 3: Get detailed issues for each repo
repo_issues = []
for repo in critical_repos:
    issues = await list_issues({
        "owner": "myorg",
        "repo": repo["name"],
        "state": "open"
    })
    repo_issues.append({"repo": repo["name"], "issues": issues})

# Step 4: Store full results in workspace for later analysis
with open("./workspace/issues-report.json", "w") as f:
    json.dump(repo_issues, f, indent=2)

# Step 5: Generate summary for Slack (only summary goes through your context)
total_issues = sum(len(r["issues"]) for r in repo_issues)
summary = f"Found {len(critical_repos)} repos needing attention with {total_issues} total open issues."

await post_message({
    "channel": "#dev-alerts",
    "text": summary
})

# Return only summary
{"summary": summary, "critical_repos": len(critical_repos)}
` + "```" + `

Notice: Full repo and issue data never passes through your context. Only the final summary does.

IMPORTANT: Since most MCP server tools do not explicitly document their output schema (return type), learn and memorize their return types from the interactions.

### Complex Example: Data Aggregation with State Management
` + "```python" + `
from servers.github.github import list_repos
import json
import os
from datetime import datetime, timedelta

# Check if we have cached results from previous run
try:
    with open("./workspace/metrics.json", "r") as f:
        data = json.load(f)
        if datetime.now().timestamp() - data["timestamp"] < 3600:  # 1 hour
            # Return cached metrics (last expression)
            data["metrics"]
except FileNotFoundError:
    pass

# No cache or expired - fetch and compute
repos = await list_repos({"owner": "myorg"})

# Aggregate metrics
by_language = {}
for r in repos:
    lang = r.get("language", "Unknown")
    by_language[lang] = by_language.get(lang, 0) + 1

thirty_days_ago = datetime.now() - timedelta(days=30)
recently_updated = sum(
    1 for r in repos 
    if datetime.fromisoformat(r["updated_at"].replace("Z", "+00:00")) > thirty_days_ago
)

metrics = {
    "total": len(repos),
    "by_language": by_language,
    "avg_stars": sum(r["stargazers_count"] for r in repos) / len(repos),
    "recently_updated": recently_updated
}

# Store for next time
with open("./workspace/metrics.json", "w") as f:
    json.dump({
        "timestamp": datetime.now().timestamp(),
        "metrics": metrics
    }, f)

# Append to history log
log_entry = f"[{datetime.now().isoformat()}] Metrics: {json.dumps(metrics)}\n"
mode = "a" if os.path.exists("./workspace/metrics-history.log") else "w"
with open("./workspace/metrics-history.log", mode) as f:
    f.write(log_entry)

# Return metrics (last expression)
metrics
` + "```" + `

## Available Tools

- **list_directory** - Explore the virtual filesystem to discover MCP servers and tools
- **read_file** - Read tool definitions, workspace files, or cached data
- **execute_code** - Run your Python code with Pyodide

## Workspace File Access (Standard Python I/O)

For multi-step workflows, use the workspace to store intermediate results:

` + "```python" + `
import json
import os

# Write files to workspace
with open("./workspace/data.json", "w") as f:
    json.dump({"key": "value"}, f)

# Read files from workspace
with open("./workspace/data.json", "r") as f:
    data = json.load(f)

# List workspace contents
files = os.listdir("./workspace")

# Check if file exists and delete
if os.path.exists("./workspace/old-data.json"):
    os.remove("./workspace/old-data.json")

# Create subdirectories
os.makedirs("./workspace/cache", exist_ok=True)
` + "```" + `

## Key Principles

1. **Discover efficiently**: Start broad, narrow down. Don't read all tools.
2. **Process in sandbox**: Loops, filtering, aggregation happen in your code, not in your context.
3. **Use workspace strategically**: Store full datasets, keep summaries for your context.
4. **Chain operations**: Call multiple MCP tools in sequence within one script.
5. **Return what matters**: Only final results/summaries should return to your context (last expression).

## Technical Details

- All paths start with '/'
- Use standard Python imports: ` + "`" + `from servers.github.github import list_repos` + "`" + `
- Last expression is automatically returned (Jupyter-style)
- To return a variable, put it on the last line without assignment
- Execution timeout: 30 seconds
- Pyodide runtime with Python standard library
- All MCP tool calls require 'await'
- Working directory: /session (use ./workspace/ for persistent storage)
` + packagesSection + `
`
}

// NewMcpServer creates and configures the MCP server
func NewMcpServer(wasmBytes []byte, sessionMgr *session.Manager) *mcp.Server {
	// Get the configured language and packages from session manager
	language := sessionMgr.GetLanguage()
	packages := sessionMgr.GetPythonPackages()
	serverNames := sessionMgr.GetMcpServerNames()
	mounts := sessionMgr.GetMounts()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "runbyte",
		Version: "1.0.0",
	}, &mcp.ServerOptions{
		Instructions: getServerInstructions(language, packages, serverNames, mounts),
	})

	server.AddReceivingMiddleware(createSessionInjectionMiddleware(sessionMgr))
	server.AddReceivingMiddleware(createLoggingMiddleware())

	// Register execute_code tool with language-specific description
	mcp.AddTool(server, &mcp.Tool{
		Name:        "execute_code",
		Description: getExecuteCodeDescription(language),
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ExecuteCodeArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		var result string

		// Route to appropriate execution based on session language
		if sessionCtx.Language == "python" {
			// Python execution path
			if sessionCtx.PythonRuntime == nil {
				return nil, nil, fmt.Errorf("Python runtime not initialized for this session")
			}

			// Execute Python code directly (no bundling needed)
			execResult, err := sessionCtx.PythonRuntime.Execute(ctx, args.Code)
			if err != nil {
				return nil, nil, fmt.Errorf("Python execution request failed: %w", err)
			}

			// Check for Python execution errors
			if execResult.Error != nil {
				errorMsg := *execResult.Error
				if execResult.Traceback != nil {
					errorMsg = fmt.Sprintf("%s\n\nTraceback:\n%s", errorMsg, *execResult.Traceback)
				}
				return nil, nil, fmt.Errorf("Python execution failed:\n%s", errorMsg)
			}

			// Get result
			if execResult.Result != nil {
				result = *execResult.Result
			} else {
				result = "" // No result (e.g., statement with no return value)
			}

		} else {
			// TypeScript execution path (existing logic)
			// Step 1: Bundle the code using session's bundle directory
			b, err := bundler.New()
			if err != nil {
				return nil, nil, fmt.Errorf("failed to create bundler: %w", err)
			}

			codeWithCaller := fmt.Sprintf(`%s
exec();
`, args.Code)
			bundledCode, sourceMap, err := b.Bundle(sessionCtx.BundleDir, codeWithCaller)
			if err != nil {
				return nil, nil, fmt.Errorf("bundling failed: %w", err)
			}

			// Step 2: Create sandbox with filesystem access
			sb, err := sandbox.NewSandbox(ctx, wasmBytes, sessionCtx.ClientHub, sessionCtx.SandboxFS)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to create sandbox: %w", err)
			}
			defer sb.Close()

			// Step 3: Execute bundled code
			result, err = sb.ExecuteCode(bundledCode, sourceMap)
			if err != nil {
				return nil, nil, fmt.Errorf("execution failed: %w", err)
			}
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: result},
			},
		}, nil, nil
	})

	// Register list_directory tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_directory",
		Description: "List contents of a directory in the virtual filesystem. Returns directories and files with their types. Supports both /servers/* and /workspace/* directories.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListDirectoryArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		var output bytes.Buffer

		// Normalize path
		path := strings.TrimPrefix(args.Path, "/")
		path = strings.TrimSuffix(path, "/")

		if path == "" {
			// Root directory - show both servers and filesystem directories
			output.WriteString("/\n")
			output.WriteString("├── servers/ (MCP servers)\n")

			// List filesystem directories
			if sessionCtx.SandboxFS != nil {
				dirs := sessionCtx.SandboxFS.GetDirectories()
				for i, dir := range dirs {
					prefix := "├──"
					if i == len(dirs)-1 {
						prefix = "└──"
					}
					output.WriteString(fmt.Sprintf("%s %s/\n", prefix, dir))
				}
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: output.String()},
				},
			}, nil, nil
		}

		if path == "servers" {
			// List all MCP servers
			allTools := sessionCtx.ClientHub.Tools()
			output.WriteString("/servers/\n")

			serverCount := 0
			for svr, toolList := range allTools {
				serverCount++
				prefix := "├──"
				output.WriteString(fmt.Sprintf("%s %s/ (%d functions)\n", prefix, svr, len(toolList)))
			}

			// Show appropriate index file based on session language
			if sessionCtx.Language == "python" {
				output.WriteString("└── __init__.py\n")
			} else {
				output.WriteString("└── index.ts\n")
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: output.String()},
				},
			}, nil, nil
		}

		if strings.HasPrefix(path, "servers/") {
			// List specific server directory
			serverName := strings.TrimPrefix(path, "servers/")
			tools, ok := sessionCtx.ClientHub.ServerTools(serverName)
			if !ok {
				availableServers := sessionCtx.ClientHub.Servers()
				return nil, nil, fmt.Errorf("directory '/servers/%s/' not found. Available servers: %v",
					serverName, availableServers)
			}

			output.WriteString(fmt.Sprintf("/servers/%s/\n", serverName))

			// Determine file extension and function naming based on session language
			var fileExt, indexFile string
			if sessionCtx.Language == "python" {
				fileExt = ".py"
				indexFile = "__init__.py"
			} else {
				fileExt = ".ts"
				indexFile = "index.ts"
			}

			for i, tool := range tools {
				prefix := "├──"
				if i == len(tools)-1 {
					prefix = "├──"
				}

				var funcName string
				if sessionCtx.Language == "python" {
					funcName = strutil.ToSnakeCase(tool.Name)
				} else {
					funcName = strutil.ToCamelCase(tool.Name)
				}

				if args.WithDescriptions && tool.Description != "" {
					output.WriteString(fmt.Sprintf("%s %s%s - %s\n", prefix, funcName, fileExt, tool.Description))
				} else {
					output.WriteString(fmt.Sprintf("%s %s%s\n", prefix, funcName, fileExt))
				}
			}
			output.WriteString(fmt.Sprintf("└── %s\n", indexFile))
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: output.String()},
				},
			}, nil, nil
		}

		// Check if it's a filesystem directory (workspace)
		if sessionCtx.SandboxFS != nil {
			dirs := sessionCtx.SandboxFS.GetDirectories()
			for _, dir := range dirs {
				if path == dir || strings.HasPrefix(path, dir+"/") {
					// List filesystem directory
					fsPath := "./" + path
					files, err := sessionCtx.SandboxFS.ListFiles(fsPath)
					if err != nil {
						return nil, nil, fmt.Errorf("failed to list directory '/%s': %w", path, err)
					}

					output.WriteString(fmt.Sprintf("/%s/\n", path))
					for i, file := range files {
						prefix := "├──"
						if i == len(files)-1 {
							prefix = "└──"
						}
						output.WriteString(fmt.Sprintf("%s %s\n", prefix, file))
					}

					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{Text: output.String()},
						},
					}, nil, nil
				}
			}
		}

		return nil, nil, fmt.Errorf("directory '/%s' not found", path)
	})

	// Register read_file tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_file",
		Description: "Read a file from the virtual filesystem. Supports /servers/* paths (TypeScript .ts or Python .py files depending on runtime language) and /workspace/* paths. Examples: '/servers/github/listRepos.ts', '/servers/echo/echo.py', '/workspace/config.json'.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ReadFileArgs) (*mcp.CallToolResult, any, error) {
		sessionCtx, err := getSessionFromContext(ctx)
		if err != nil {
			return nil, nil, err
		}

		// Normalize path - remove leading slash
		path := strings.TrimPrefix(args.Path, "/")

		// Check if it's a filesystem path (workspace)
		if sessionCtx.SandboxFS != nil {
			dirs := sessionCtx.SandboxFS.GetDirectories()
			for _, dir := range dirs {
				if strings.HasPrefix(path, dir+"/") {
					// Read from sandbox filesystem
					fsPath := "./" + path
					content, err := sessionCtx.SandboxFS.ReadFile(fsPath)
					if err != nil {
						return nil, nil, fmt.Errorf("failed to read file '/%s': %w", path, err)
					}

					return &mcp.CallToolResult{
						Content: []mcp.Content{
							&mcp.TextContent{Text: content},
						},
					}, nil, nil
				}
			}
		}

		// If path starts with "servers/", read from bundle directory
		if strings.HasPrefix(path, "servers/") {
			// Both Python and TypeScript use the same directory structure
			filePath := filepath.Join(sessionCtx.BundleDir, path)

			// Read the file from disk
			content, err := os.ReadFile(filePath)
			if err != nil {
				return nil, nil, fmt.Errorf("file '/%s' not found", path)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: string(content)},
				},
			}, nil, nil
		}

		return nil, nil, fmt.Errorf("file '/%s' not found - path must start with 'servers/', 'workspace/'", path)
	})

	return server
}
