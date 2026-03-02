"""
Runbyte Python Runtime Utilities

This module provides utilities for executing user Python code within Pyodide,
including MCP tool call support.
"""

import json
import traceback
from typing import Any, Dict, Optional


async def execute_code(code: str, call_mcp_tool_func) -> Dict[str, Any]:
    """
    Execute user Python code with last-expression return (Jupyter-style).

    Args:
        code: Python code to execute
        call_mcp_tool_func: JavaScript function to call MCP tools

    Returns:
        Dict with 'result' (JSON string) or 'error' (error message)
    """
    import asyncio
    import inspect

    try:
        # Make MCP tool function available to user code
        globals()["__call_mcp_tool"] = call_mcp_tool_func

        # Execute code and capture last expression
        # This mimics Jupyter notebook behavior

        compiled = compile(
            code, "<user_code>", "eval", flags=0x200
        )  # PyCF_ALLOW_TOP_LEVEL_AWAIT

        result = eval(compiled, globals())

        # If result is a coroutine, await it
        if inspect.iscoroutine(result):
            result = await result

        # Serialize result to JSON
        if result is None:
            result_json = None
        else:
            try:
                result_json = json.dumps(result)
            except (TypeError, ValueError):
                # If not JSON serializable, convert to string
                result_json = json.dumps(str(result))

        return {"result": result_json, "error": None}

    except SyntaxError:
        # Code might be statements, not an expression
        # Try executing as statements with top-level await support
        try:
            # Create a new namespace for execution
            namespace = globals().copy()
            namespace["__call_mcp_tool"] = call_mcp_tool_func

            # Compile with top-level await support
            # When PyCF_ALLOW_TOP_LEVEL_AWAIT is used, exec returns a coroutine if there's await
            compiled = compile(
                code, "<user_code>", "exec", flags=0x200
            )  # PyCF_ALLOW_TOP_LEVEL_AWAIT

            # Execute - this might return a coroutine if code has top-level await
            coro = eval(compiled, namespace)

            # If it's a coroutine, await it
            if inspect.iscoroutine(coro):
                await coro

            # No explicit return value from statements
            return {"result": None, "error": None}

        except Exception as e:
            tb = traceback.format_exc()
            return {"result": None, "error": str(e), "traceback": tb}

    except Exception as e:
        tb = traceback.format_exc()
        return {"result": None, "error": str(e), "traceback": tb}
