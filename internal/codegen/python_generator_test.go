package codegen

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPythonGenerator(t *testing.T) {
	gen := NewPythonGenerator()

	// Test tool with input and output schemas
	tool := &mcp.Tool{
		Name:        "get_user",
		Description: "Get user information by ID",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"user_id": map[string]interface{}{
					"type":        "string",
					"description": "The user ID",
				},
				"include_details": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether to include full details",
				},
			},
			"required": []interface{}{"user_id"},
		},
		OutputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{
					"type": "string",
				},
				"name": map[string]interface{}{
					"type": "string",
				},
				"age": map[string]interface{}{
					"type": "integer",
				},
			},
		},
	}

	code, err := gen.GenerateFunctionFile("test_server", tool)
	if err != nil {
		t.Fatalf("GenerateFunctionFile failed: %v", err)
	}

	t.Logf("Generated code:\n%s", code)

	// Verify generated code contains expected elements
	if !strings.Contains(code, "class GetUserArgs(TypedDict)") {
		t.Error("Missing GetUserArgs TypedDict")
	}
	if !strings.Contains(code, "user_id: str") {
		t.Error("Missing user_id property")
	}
	if !strings.Contains(code, "include_details: Optional[bool]") {
		t.Error("Missing optional include_details property")
	}
	if !strings.Contains(code, "class GetUserResult(TypedDict)") {
		t.Error("Missing GetUserResult TypedDict")
	}
	if !strings.Contains(code, "async def get_user(args: GetUserArgs) -> GetUserResult:") {
		t.Error("Missing function signature")
	}
	if !strings.Contains(code, `await __call_mcp_tool("test_server", "get_user", args)`) {
		t.Error("Missing MCP tool call")
	}
	if !strings.Contains(code, "from typing import Any, Dict, List, Optional, Union, TypedDict") {
		t.Error("Missing typing imports")
	}
}

func TestPythonGeneratorNoArgs(t *testing.T) {
	gen := NewPythonGenerator()

	// Test tool with no input schema
	tool := &mcp.Tool{
		Name:        "ping",
		Description: "Simple ping command",
	}

	code, err := gen.GenerateFunctionFile("test_server", tool)
	if err != nil {
		t.Fatalf("GenerateFunctionFile failed: %v", err)
	}

	t.Logf("Generated code:\n%s", code)

	// Verify generated code
	if !strings.Contains(code, "async def ping() -> Any:") {
		t.Error("Missing function signature for no-args function")
	}
	if !strings.Contains(code, `await __call_mcp_tool("test_server", "ping", {})`) {
		t.Error("Missing MCP tool call with empty args")
	}
}

func TestPythonGeneratorServerInit(t *testing.T) {
	gen := NewPythonGenerator()

	tools := []*mcp.Tool{
		{Name: "get_user"},
		{Name: "create_user"},
		{Name: "delete_user"},
	}

	code := gen.GenerateServerInitFile("test_server", tools)

	t.Logf("Generated __init__.py:\n%s", code)

	// Verify imports
	if !strings.Contains(code, "from .get_user import get_user") {
		t.Error("Missing get_user import")
	}
	if !strings.Contains(code, "from .create_user import create_user") {
		t.Error("Missing create_user import")
	}
	if !strings.Contains(code, "from .delete_user import delete_user") {
		t.Error("Missing delete_user import")
	}

	// Verify __all__
	if !strings.Contains(code, "__all__ = [") {
		t.Error("Missing __all__ definition")
	}
	if !strings.Contains(code, `"get_user"`) {
		t.Error("Missing get_user in __all__")
	}
}
