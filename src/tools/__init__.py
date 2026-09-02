"""MCP tool handlers. Imported and registered by `src.app`."""

from .add_member import add_member_handler
from .add_reaction import add_reaction_handler
from .create_group_chat import create_group_chat_handler
from .create_section import create_section_handler
from .create_space import create_space_handler
from .delete_message import delete_message_handler
from .delete_section import delete_section_handler
from .find_direct_message import find_direct_message_handler
from .get_message import get_message_handler
from .get_messages import get_messages_handler
from .get_space import get_space_handler
from .get_thread import get_thread_handler
from .list_members import list_members_handler
from .list_reactions import list_reactions_handler
from .list_section_items import list_section_items_handler
from .list_sections import list_sections_handler
from .list_spaces import list_spaces_handler
from .move_space_to_section import move_space_to_section_handler
from .position_section import position_section_handler
from .remove_member import remove_member_handler
from .remove_reaction import remove_reaction_handler
from .rename_section import rename_section_handler
from .search_messages import search_messages_handler
from .search_people import search_people_handler
from .send_message import send_message_handler
from .update_message import update_message_handler
from .update_space import update_space_handler
from .whoami import whoami_handler

__all__ = [
    "add_member_handler",
    "add_reaction_handler",
    "create_group_chat_handler",
    "create_section_handler",
    "create_space_handler",
    "delete_message_handler",
    "delete_section_handler",
    "find_direct_message_handler",
    "get_message_handler",
    "get_messages_handler",
    "get_space_handler",
    "get_thread_handler",
    "list_members_handler",
    "list_reactions_handler",
    "list_section_items_handler",
    "list_sections_handler",
    "list_spaces_handler",
    "move_space_to_section_handler",
    "position_section_handler",
    "remove_member_handler",
    "remove_reaction_handler",
    "rename_section_handler",
    "search_messages_handler",
    "search_people_handler",
    "send_message_handler",
    "update_message_handler",
    "update_space_handler",
    "whoami_handler",
]
