/**
 * Type definitions for CodeBraid MCP Plugin
 */

declare module "main" {
    /**
     * Main entry point - executes user code in sandbox
     * Extism exports take no params and return an I32
     */
    export function executeCode(): I32;
}

declare module "extism:host" {
    interface user {
        /**
         * Call an MCP tool on a downstream server
         * @param ptr Pointer to JSON string containing {serverName, toolName, args}
         * @returns Pointer to JSON string containing {success, result, error}
         */
        callMcpTool(ptr: I64): I64;
    }
}

/**
 * Global function available to user code
 */
declare function callMcpTool(
    serverName: string,
    toolName: string,
    args: Record<string, any>
): any;

