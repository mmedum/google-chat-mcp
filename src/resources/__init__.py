"""MCP Resources — bounded, addressable read-only Chat objects exposed by URI.

Complements the tool surface: tools are model-controlled (LLM picks one to call),
resources are application-driven (host UI picks which to include in context).
The same chat_client methods back both surfaces; resource handlers are thin
wrappers around the equivalent tool handler.
"""

from .space import register_space_resource

__all__ = ["register_space_resource"]
