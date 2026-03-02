/**
 * Runbyte Python Runtime Server
 * 
 * Node.js HTTP server that runs Pyodide to execute Python code.
 * Communicates with Go server via HTTP for MCP tool calls.
 */

import express from 'express';
import { loadPyodide } from 'pyodide';
import { readFileSync, readdirSync, statSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

// Configuration from command-line args
const PORT = parseInt(process.env.PORT || '0'); // 0 = random port
const PACKAGES = process.env.PACKAGES ? JSON.parse(process.env.PACKAGES) : [];
const MCP_CALLBACK_URL = process.env.MCP_CALLBACK_URL || 'http://localhost:8080/mcp-tool';
const SESSION_ID = process.env.SESSION_ID || 'unknown';
const SESSION_DIR = process.env.SESSION_DIR || ''; // Root directory containing servers/ and workspace/
const CUSTOM_MOUNTS = process.env.CUSTOM_MOUNTS ? JSON.parse(process.env.CUSTOM_MOUNTS) : []; // Custom directory mounts

console.log(`[Python Runtime] Environment: SESSION_ID=${SESSION_ID}, SESSION_DIR=${SESSION_DIR}`);
console.log(`[Python Runtime] CUSTOM_MOUNTS env var: ${process.env.CUSTOM_MOUNTS || '(not set)'}`);
console.log(`[Python Runtime] Parsed CUSTOM_MOUNTS: ${JSON.stringify(CUSTOM_MOUNTS)}`);

// Global state
let pyodide = null;
let isReady = false;
let isShuttingDown = false;

const app = express();
app.use(express.json({ limit: '50mb' }));

/**
 * Recursively mount a host directory into Pyodide's virtual filesystem (one-way copy)
 */
function mountDirectory(pyodide, hostPath, pyodidePath) {
    const fs = pyodide.FS;
    
    // Create directory in Pyodide FS
    try {
        fs.mkdir(pyodidePath);
    } catch (e) {
        // Directory might already exist, ignore
    }
    
    // Read directory contents
    const entries = readdirSync(hostPath);
    
    for (const entry of entries) {
        const hostEntryPath = join(hostPath, entry);
        const pyodideEntryPath = `${pyodidePath}/${entry}`;
        const stats = statSync(hostEntryPath);
        
        if (stats.isDirectory()) {
            // Recursively mount subdirectory
            mountDirectory(pyodide, hostEntryPath, pyodideEntryPath);
        } else if (stats.isFile()) {
            // Copy file into Pyodide FS
            const content = readFileSync(hostEntryPath);
            fs.writeFile(pyodideEntryPath, content);
        }
    }
}

/**
 * Mount a directory using NODEFS for bidirectional file sync
 */
function mountNodeFS(pyodide, hostPath, pyodidePath) {
    const fs = pyodide.FS;
    
    console.log(`[Python Runtime] mountNodeFS: Starting mount of ${hostPath} as NODEFS at ${pyodidePath}`);
    
    // Create parent directory if needed
    const parentPath = pyodidePath.split('/').slice(0, -1).join('/');
    if (parentPath) {
        console.log(`[Python Runtime] mountNodeFS: Creating parent path ${parentPath}`);
        try {
            fs.mkdirTree(parentPath);
        } catch (e) {
            console.log(`[Python Runtime] mountNodeFS: Parent path already exists or error: ${e.message}`);
        }
    }
    
    // Mount using NODEFS for bidirectional file sync
    console.log(`[Python Runtime] mountNodeFS: Creating mount point ${pyodidePath}`);
    try {
        fs.mkdir(pyodidePath);
        console.log(`[Python Runtime] mountNodeFS: Mount point created`);
    } catch (e) {
        console.log(`[Python Runtime] mountNodeFS: Mount point already exists or error: ${e.message}`);
    }
    
    console.log(`[Python Runtime] mountNodeFS: Calling fs.mount with NODEFS`);
    fs.mount(fs.filesystems.NODEFS, { root: hostPath }, pyodidePath);
    console.log(`[Python Runtime] mountNodeFS: NODEFS mounted successfully`);
}

/**
 * Initialize Pyodide and load packages
 */
async function initPyodide() {
    console.log('[Python Runtime] Initializing Pyodide...');
    
    try {
        // Load Pyodide (let it use default CDN or node_modules)
        pyodide = await loadPyodide();
        
        console.log('[Python Runtime] Pyodide loaded, loading micropip...');
        
        // Load micropip
        await pyodide.loadPackage('micropip');
        const micropip = pyodide.pyimport('micropip');
        
        // Install packages if specified
        if (PACKAGES.length > 0) {
            console.log(`[Python Runtime] Installing packages: ${PACKAGES.join(', ')}`);
            await micropip.install(PACKAGES);
            console.log('[Python Runtime] Packages installed successfully');
        }
        
        // Load runtime.py into Pyodide
        const runtimePyPath = join(__dirname, 'runtime.py');
        const runtimePyCode = readFileSync(runtimePyPath, 'utf-8');
        await pyodide.runPythonAsync(runtimePyCode);
        
        console.log('[Python Runtime] Runtime utilities loaded');
        
        // Mount session root directory using NODEFS for bidirectional sync
        // This gives access to both servers/ (MCP libraries) and workspace/ (user files)
        if (SESSION_DIR) {
            console.log(`[Python Runtime] Mounting session directory into Pyodide FS: ${SESSION_DIR}`);
            try {
                mountNodeFS(pyodide, SESSION_DIR, '/session');
                console.log('[Python Runtime] Session directory mounted at /session with NODEFS');
            } catch (error) {
                console.error('[Python Runtime] Failed to mount session directory:', error);
                throw error;
            }
            
            // Add the servers directory to Python's sys.path
            await pyodide.runPythonAsync(`
import sys
import os
sys.path.insert(0, '/session/servers')
print(f"[Python] sys.path updated: {sys.path[0]}")

# Set working directory to /session so users can access ./workspace/ and ./servers/
os.chdir('/session')
print(f"[Python] Working directory: {os.getcwd()}")
`);
            console.log('[Python Runtime] Python environment configured with sys.path and working directory');
        }
        
        // Mount custom directories if provided
        if (CUSTOM_MOUNTS && CUSTOM_MOUNTS.length > 0) {
            console.log(`[Python Runtime] Mounting ${CUSTOM_MOUNTS.length} custom director${CUSTOM_MOUNTS.length === 1 ? 'y' : 'ies'}`);
            const fs = pyodide.FS;
            
            for (const mount of CUSTOM_MOUNTS) {
                const { name, hostPath, readOnly } = mount;
                const pyodidePath = `/session/${name}`;
                
                try {
                    console.log(`[Python Runtime] Mounting custom directory "${name}" from ${hostPath} at ${pyodidePath} (readOnly=${readOnly})`);
                    mountNodeFS(pyodide, hostPath, pyodidePath);
                    console.log(`[Python Runtime] Successfully mounted custom directory "${name}"`);
                } catch (error) {
                    console.error(`[Python Runtime] Failed to mount custom directory "${name}":`, error);
                }
            }
        }
        
        // Create MCP tool call bridge function
        // This function is called from Python code via __call_mcp_tool()
        pyodide.globals.set('__mcp_callback_url', MCP_CALLBACK_URL);
        pyodide.globals.set('__session_id', SESSION_ID);
        
        await pyodide.runPythonAsync(`
import json
from js import fetch, Object
import builtins

async def __call_mcp_tool(server_name: str, tool_name: str, arguments: dict):
    """Call an MCP tool via HTTP bridge to Go server"""
    callback_url = __mcp_callback_url
    session_id = __session_id
    
    payload = {
        "server": server_name,
        "tool": tool_name,
        "arguments": arguments
    }
    
    # Convert headers dict to JS object
    headers = Object.fromEntries([
        ["Content-Type", "application/json"],
        ["X-Session-ID", session_id]
    ])
    
    # Make HTTP POST request to Go server with session ID
    response = await fetch(
        callback_url,
        method="POST",
        headers=headers,
        body=json.dumps(payload)
    )
    
    if not response.ok:
        raise Exception(f"MCP tool call failed: {response.status} {response.statusText}")
    
    result_json = await response.text()
    result = json.loads(result_json)
    
    if "error" in result and result["error"]:
        raise Exception(f"MCP tool error: {result['error']}")
    
    # MCP tool result has structure: {content: [{type: "text", text: "..."}]}
    # Return the full result object so Python code can access it
    return result

# Make __call_mcp_tool available globally as a builtin
builtins.__call_mcp_tool = __call_mcp_tool
`);
        
        isReady = true;
        console.log('[Python Runtime] Ready to execute code');
        
    } catch (error) {
        console.error('[Python Runtime] Initialization failed:', error);
        throw error;
    }
}

/**
 * Health check endpoint
 */
app.get('/health', (req, res) => {
    if (isShuttingDown) {
        return res.status(503).json({
            status: 'shutting_down',
            ready: false
        });
    }
    
    res.json({
        status: isReady ? 'ready' : 'initializing',
        ready: isReady,
        packages: PACKAGES
    });
});

/**
 * Execute Python code endpoint
 */
app.post('/execute', async (req, res) => {
    if (!isReady) {
        return res.status(503).json({
            error: 'Server not ready',
            ready: false
        });
    }
    
    if (isShuttingDown) {
        return res.status(503).json({
            error: 'Server is shutting down',
            ready: false
        });
    }
    
    const { code } = req.body;
    
    if (!code || typeof code !== 'string') {
        return res.status(400).json({
            error: 'Missing or invalid "code" field in request body'
        });
    }
    
    try {
        console.log('[Python Runtime] Executing code...');
        
        // Use Pyodide's runPythonAsync which properly handles top-level await
        // The __call_mcp_tool function is already available in globals
        const result = await pyodide.runPythonAsync(code);
        
        console.log('[Python Runtime] Execution complete, result:', result);
        
        // Serialize result to JSON
        let resultJson = null;
        if (result !== undefined && result !== null) {
            try {
                // Try to convert to JSON
                if (typeof result === 'object' && result.toJs) {
                    // It's a Python object, convert to JS
                    const jsObj = result.toJs({ dict_converter: Object.fromEntries });
                    resultJson = JSON.stringify(jsObj);
                } else {
                    // It's already a JS value
                    resultJson = JSON.stringify(result);
                }
            } catch (e) {
                // If not JSON serializable, convert to string
                resultJson = JSON.stringify(String(result));
            }
        }
        
        res.json({
            result: resultJson,
            error: null
        });
        
    } catch (error) {
        console.error('[Python Runtime] Execution error:', error);
        
        res.json({
            result: null,
            error: error.message,
            traceback: error.stack
        });
    }
});

/**
 * Graceful shutdown endpoint
 */
app.post('/shutdown', async (req, res) => {
    console.log('[Python Runtime] Shutdown requested');
    
    isShuttingDown = true;
    
    res.json({
        status: 'shutting_down',
        message: 'Server will shut down after current requests complete'
    });
    
    // Give time for response to be sent
    setTimeout(() => {
        console.log('[Python Runtime] Shutting down...');
        process.exit(0);
    }, 100);
});

/**
 * Start server
 */
async function start() {
    try {
        // Initialize Pyodide first
        await initPyodide();
        
        // Start HTTP server
        const server = app.listen(PORT, () => {
            const address = server.address();
            const actualPort = address.port;
            
            // Print port to stdout for Go to capture
            console.log(`PYTHON_RUNTIME_PORT=${actualPort}`);
            console.log(`[Python Runtime] Server listening on port ${actualPort}`);
        });
        
        // Handle server errors
        server.on('error', (error) => {
            console.error('[Python Runtime] Server error:', error);
            process.exit(1);
        });
        
    } catch (error) {
        console.error('[Python Runtime] Failed to start:', error);
        process.exit(1);
    }
}

// Handle uncaught errors
process.on('uncaughtException', (error) => {
    console.error('[Python Runtime] Uncaught exception:', error);
    process.exit(1);
});

process.on('unhandledRejection', (reason, promise) => {
    console.error('[Python Runtime] Unhandled rejection:', reason);
    process.exit(1);
});

// Start the server
start();
