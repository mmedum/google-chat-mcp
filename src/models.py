"""Pydantic models for tool I/O and Chat API responses.

Every model sets ``extra="forbid"`` and ``strict=True``. Schema drift in Google's
API surfaces as validation errors instead of silent field drops — the runbook
covers the failure mode.
"""

from __future__ import annotations

from datetime import datetime
from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, EmailStr, Field, StringConstraints


class _Strict(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True)


# ---------- tool I/O ----------

# Google Chat resource names use IDs that include dots, dashes, underscores,
# and alphanumerics. Patterns kept permissive enough to accept every real-world
# form we've seen, strict enough to reject obviously-malformed input.
_ID = r"[A-Za-z0-9._-]+"
SpaceId = Annotated[str, StringConstraints(pattern=rf"^spaces/{_ID}$")]
ThreadName = Annotated[str, StringConstraints(pattern=rf"^spaces/{_ID}/threads/{_ID}$")]
MessageId = Annotated[str, StringConstraints(pattern=rf"^spaces/{_ID}/messages/{_ID}$")]
UserId = Annotated[str, StringConstraints(pattern=rf"^users/{_ID}$")]

SpaceType = Literal["SPACE", "DIRECT_MESSAGE", "GROUP_CHAT"]


class SpaceSummary(_Strict):
    space_id: SpaceId
    type: SpaceType
    display_name: str


class DirectMessageResult(_Strict):
    space_id: SpaceId


class SendMessageInput(_Strict):
    space_id: SpaceId
    text: Annotated[str, StringConstraints(min_length=1, max_length=4096)]
    thread_name: ThreadName | None = None


class SendMessageResult(_Strict):
    message_id: MessageId
    space_id: SpaceId
    thread_id: ThreadName


class GetMessagesInput(_Strict):
    space_id: SpaceId
    since: datetime | None = None
    limit: Annotated[int, Field(ge=1, le=100)] = 20


class ChatMessage(_Strict):
    message_id: MessageId
    sender_user_id: UserId
    sender_email: EmailStr | None
    sender_display_name: str | None
    text: str
    timestamp: datetime
    thread_id: ThreadName


# ---------- Chat API response shapes ----------
# Pydantic validators for raw Google JSON. `extra="forbid"` catches additions
# from Google — surfaced as an ApiValidationError and logged.


class _ChatBase(BaseModel):
    model_config = ConfigDict(extra="forbid", populate_by_name=True)


class _ChatUser(_ChatBase):
    name: UserId
    type_: Literal["HUMAN", "BOT"] | None = Field(default=None, alias="type")
    display_name: str | None = Field(default=None, alias="displayName")
    domain_id: str | None = Field(default=None, alias="domainId")
    is_anonymous: bool | None = Field(default=None, alias="isAnonymous")


class _ChatThread(_ChatBase):
    name: ThreadName
    thread_key: str | None = Field(default=None, alias="threadKey")


class _ChatMessageResponse(_ChatBase):
    name: MessageId
    sender: _ChatUser
    create_time: datetime = Field(alias="createTime")
    last_update_time: datetime | None = Field(default=None, alias="lastUpdateTime")
    text: str = ""
    formatted_text: str | None = Field(default=None, alias="formattedText")
    thread: _ChatThread
    space: _ChatBase | None = None  # opaque; we don't use it
    argument_text: str | None = Field(default=None, alias="argumentText")
    fallback_text: str | None = Field(default=None, alias="fallbackText")
    thread_reply: bool | None = Field(default=None, alias="threadReply")
    client_assigned_message_id: str | None = Field(default=None, alias="clientAssignedMessageId")


class _ChatSpaceResponse(_ChatBase):
    name: SpaceId
    type_: Literal["SPACE", "DIRECT_MESSAGE", "GROUP_CHAT"] = Field(alias="type")
    display_name: str | None = Field(default=None, alias="displayName")
    space_type: str | None = Field(default=None, alias="spaceType")
    single_user_bot_dm: bool | None = Field(default=None, alias="singleUserBotDm")
    threaded: bool | None = None
    external_user_allowed: bool | None = Field(default=None, alias="externalUserAllowed")
    space_threading_state: str | None = Field(default=None, alias="spaceThreadingState")
    space_details: dict[str, object] | None = Field(default=None, alias="spaceDetails")
    space_history_state: str | None = Field(default=None, alias="spaceHistoryState")
    import_mode: bool | None = Field(default=None, alias="importMode")
    create_time: datetime | None = Field(default=None, alias="createTime")
    admin_installed: bool | None = Field(default=None, alias="adminInstalled")
    membership_count: dict[str, int] | None = Field(default=None, alias="membershipCount")
    access_settings: dict[str, object] | None = Field(default=None, alias="accessSettings")
    space_uri: str | None = Field(default=None, alias="spaceUri")
    predefined_permission_settings: str | None = Field(
        default=None, alias="predefinedPermissionSettings"
    )
    permission_settings: dict[str, object] | None = Field(default=None, alias="permissionSettings")
    customer: str | None = None


class _ChatSpacesListResponse(_ChatBase):
    spaces: list[_ChatSpaceResponse] = Field(default_factory=list)
    next_page_token: str | None = Field(default=None, alias="nextPageToken")
