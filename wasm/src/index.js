/**
 * CodeBraid MCP JavaScript Plugin
 *
 * This plugin runs user code in a WebAssembly sandbox with access to
 * downstream MCP tools via the callMcpTool function.
 */

async function executeCode() {
    try {
        const {callMcpTool} = Host.getFunctions();
        // TODO: Make sure callMcpTool is not accessible

        /**
         * Call an MCP tool on a downstream server
         * @param {string} serverName - Name of the MCP server
         * @param {string} toolName - Name of the tool to call
         * @param {object} args - Arguments to pass to the tool
         * @returns {any} The result from the MCP tool
         */
        function callTool(serverName, toolName, args) {
            const msg = {
                serverName,
                toolName,
                args: args || {}
            };

            // Call host function
            const mem = Memory.fromString(JSON.stringify(msg));
            const offset = callMcpTool(mem.offset);
            const response = Memory.find(offset).readString();
            const result = JSON.parse(response);

            // Check if the call was successful
            if (!result.success) {
                throw new Error(result.error || "MCP call failed");
            }

            return result.result;
        }

        // Get user's code from input
        const code = Host.inputString();

        try {
            // Execute user's code
            // User can call: callMcpTool("github", "list_repos", {})
            const result = await eval(code);

            // Return result as JSON string
            Host.outputString(result !== undefined ? JSON.stringify(result) : "");
        } catch (error) {
            // Return error information
            Host.outputString(JSON.stringify({
                error: error.message,
                stack: error.stack
            }));
        }
    } catch (e) {
        Host.outputString(e.message)
    }
}

module.exports = { executeCode };

