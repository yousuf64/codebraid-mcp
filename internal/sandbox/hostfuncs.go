package sandbox

import (
	"context"
	"encoding/json"

	extism "github.com/extism/go-sdk"
)

// McpToolCall represents a call to an MCP tool from the sandbox
type McpToolCall struct {
	ServerName string                 `json:"serverName"`
	ToolName   string                 `json:"toolName"`
	Args       map[string]interface{} `json:"args"`
}

// McpToolResponse represents the response from an MCP tool call
type McpToolResponse struct {
	Success bool        `json:"success"`
	Result  interface{} `json:"result,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// createCallMcpToolHostFunc creates the host function for calling MCP tools
func createCallMcpToolHostFunc(sb *Sandbox) extism.HostFunction {
	return extism.NewHostFunctionWithStack(
		"callMcpTool",
		func(ctx context.Context, plugin *extism.CurrentPlugin, stack []uint64) {
			// Read input from plugin memory
			offset := stack[0]
			inputData, err := plugin.ReadBytes(offset)
			if err != nil {
				plugin.Logf(extism.LogLevelError, "Failed to read input: %v", err)
				stack[0] = 0
				return
			}

			// Parse tool call request
			var toolCall McpToolCall
			if err := json.Unmarshal(inputData, &toolCall); err != nil {
				plugin.Logf(extism.LogLevelError, "Failed to parse tool call: %v", err)
				writeErrorResponse(plugin, stack, "Invalid tool call format")
				return
			}

			plugin.Logf(extism.LogLevelInfo, "Calling MCP tool: %s.%s", toolCall.ServerName, toolCall.ToolName)

			// Make synchronous MCP call
			result, err := sb.clientHub.CallTool(sb.ctx, toolCall.ServerName, toolCall.ToolName, toolCall.Args)
			if err != nil {
				plugin.Logf(extism.LogLevelError, "Failed to call MCP tool: %v", err)

				errResp := map[string]string{"error": err.Error()}
				responseData, _ := json.Marshal(errResp)
				responseOffset, err := plugin.WriteBytes(responseData)
				if err != nil {
					plugin.Logf(extism.LogLevelError, "Failed to write error response: %v", err)
					stack[0] = 0
					return
				}

				stack[0] = responseOffset
				return
			}

			// Prepare response
			response := McpToolResponse{
				Success: err == nil,
			}

			if err != nil {
				response.Error = err.Error()
				plugin.Logf(extism.LogLevelError, "MCP call failed: %v", err)
			} else {
				if result.StructuredContent == nil {
					response.Result = result
				} else {
					response.Result = result.StructuredContent
				}

				plugin.Log(extism.LogLevelInfo, "MCP call succeeded")
			}

			// Write response back to plugin memory
			responseData, _ := json.Marshal(response)
			responseOffset, err := plugin.WriteBytes(responseData)
			if err != nil {
				plugin.Logf(extism.LogLevelError, "Failed to write response: %v", err)
				stack[0] = 0
				return
			}

			// Return offset to response
			stack[0] = responseOffset
		},
		[]extism.ValueType{extism.ValueTypeI64}, // input: offset to tool call JSON
		[]extism.ValueType{extism.ValueTypeI64}, // output: offset to result JSON
	)
}

// writeErrorResponse writes an error response to the plugin
func writeErrorResponse(plugin *extism.CurrentPlugin, stack []uint64, errorMsg string) {
	response := McpToolResponse{
		Success: false,
		Error:   errorMsg,
	}
	responseData, _ := json.Marshal(response)
	responseOffset, err := plugin.WriteBytes(responseData)
	if err != nil {
		stack[0] = 0
		return
	}
	stack[0] = responseOffset
}
