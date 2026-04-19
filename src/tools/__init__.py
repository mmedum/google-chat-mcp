"""MCP tool handlers. Imported and registered by `src.app`."""

from .find_direct_message import find_direct_message_handler
from .get_message import get_message_handler
from .get_messages import get_messages_handler
from .get_space import get_space_handler
from .get_thread import get_thread_handler
from .list_members import list_members_handler
from .list_spaces import list_spaces_handler
from .send_message import send_message_handler
from .whoami import whoami_handler

__all__ = [
    "find_direct_message_handler",
    "get_message_handler",
    "get_messages_handler",
    "get_space_handler",
    "get_thread_handler",
    "list_members_handler",
    "list_spaces_handler",
    "send_message_handler",
    "whoami_handler",
]
