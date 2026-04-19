"""Pydantic models for tool I/O and Chat API responses.

Every model sets ``extra="forbid"`` and ``strict=True``. Schema drift in Google's
API surfaces as validation errors instead of silent field drops — the runbook
covers the failure mode.
"""

from __future__ import annotations

from datetime import UTC, datetime
from typing import Annotated, Literal

from pydantic import (
    BaseModel,
    ConfigDict,
    EmailStr,
    Field,
    StringConstraints,
    field_validator,
    model_validator,
)


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
GroupId = Annotated[str, StringConstraints(pattern=rf"^groups/{_ID}$")]
MembershipName = Annotated[str, StringConstraints(pattern=rf"^spaces/{_ID}/members/{_ID}$")]

SpaceType = Literal["SPACE", "DIRECT_MESSAGE", "GROUP_CHAT"]
MemberKind = Literal["HUMAN", "GROUP"]
MemberRole = Literal["ROLE_UNSPECIFIED", "ROLE_MEMBER", "ROLE_MANAGER"]
MemberState = Literal["MEMBERSHIP_STATE_UNSPECIFIED", "JOINED", "INVITED", "NOT_A_MEMBER"]

# Google's Chat API still returns pre-GA space-type names on spaces.list
# alongside the current ones. Map them to the current literals so downstream
# code (tools, tests, schema) can assume the canonical names.
_LEGACY_SPACE_TYPE_ALIASES: dict[str, str] = {
    "ROOM": "SPACE",
    "DM": "DIRECT_MESSAGE",
    "GROUP_DM": "GROUP_CHAT",
}


class SpaceSummary(_Strict):
    space_id: SpaceId
    type: SpaceType
    display_name: str


class ListSpacesInput(_Strict):
    space_type: SpaceType | None = None
    limit: Annotated[int, Field(ge=1, le=200)] = 50


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

    @field_validator("since", mode="before")
    @classmethod
    def _parse_iso_since(cls, v: object) -> object:
        # `_Strict` is strict=True, so Pydantic rejects JSON-string datetimes
        # outright. LLM clients pass ISO strings — parse them here and treat
        # naive timestamps as UTC (safer than the server's local TZ).
        if isinstance(v, str):
            normalized = v[:-1] + "+00:00" if v.endswith("Z") else v
            try:
                dt = datetime.fromisoformat(normalized)
            except ValueError as exc:
                raise ValueError(f"`since` must be ISO-8601; got {v!r}") from exc
            return dt if dt.tzinfo else dt.replace(tzinfo=UTC)
        return v


class GetSpaceInput(_Strict):
    space_id: SpaceId


class SpaceDetails(_Strict):
    space_id: SpaceId
    type: SpaceType
    display_name: str
    single_user_bot_dm: bool | None = None
    external_user_allowed: bool | None = None
    create_time: datetime | None = None


class ListMembersInput(_Strict):
    space_id: SpaceId
    limit: Annotated[int, Field(ge=1, le=200)] = 50


class Member(_Strict):
    kind: MemberKind
    # users/{id} for humans, groups/{id} for Google Groups.
    member_id: str
    display_name: str | None
    # Populated for humans via People API (cached). Groups may surface an
    # email if Google returns one; often None.
    email: EmailStr | None
    role: MemberRole
    state: MemberState


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
    # The embedded space reference varies in shape — `_ChatBase` with
    # `extra="forbid"` rejects it outright. We don't read any of its fields.
    space: dict[str, object] | None = None
    argument_text: str | None = Field(default=None, alias="argumentText")
    fallback_text: str | None = Field(default=None, alias="fallbackText")
    thread_reply: bool | None = Field(default=None, alias="threadReply")
    client_assigned_message_id: str | None = Field(default=None, alias="clientAssignedMessageId")
    # Opaque fields — we don't render them, but `extra="forbid"` would
    # otherwise reject any message carrying them. Keep one-per-field so
    # unexpected drift still surfaces (CLAUDE.md rule).
    attachment: list[dict[str, object]] | None = None
    cards_v2: list[dict[str, object]] | None = Field(default=None, alias="cardsV2")
    cards: list[dict[str, object]] | None = None
    emoji_reaction_summaries: list[dict[str, object]] | None = Field(
        default=None, alias="emojiReactionSummaries"
    )
    accessory_widgets: list[dict[str, object]] | None = Field(
        default=None, alias="accessoryWidgets"
    )
    attached_gifs: list[dict[str, object]] | None = Field(default=None, alias="attachedGifs")
    annotations: list[dict[str, object]] | None = None
    slash_command: dict[str, object] | None = Field(default=None, alias="slashCommand")
    action_response: dict[str, object] | None = Field(default=None, alias="actionResponse")
    matched_url: dict[str, object] | None = Field(default=None, alias="matchedUrl")
    deletion_metadata: dict[str, object] | None = Field(default=None, alias="deletionMetadata")
    quoted_message_metadata: dict[str, object] | None = Field(
        default=None, alias="quotedMessageMetadata"
    )
    private_message_viewer: dict[str, object] | None = Field(
        default=None, alias="privateMessageViewer"
    )
    delete_time: datetime | None = Field(default=None, alias="deleteTime")


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
    last_active_time: datetime | None = Field(default=None, alias="lastActiveTime")

    @model_validator(mode="before")
    @classmethod
    def _prefer_space_type_over_type(cls, data: object) -> object:
        # Google's deprecated `type` field often returns "ROOM" as a catch-all
        # regardless of whether the space is actually a DM or group chat; the
        # canonical classification lives in `spaceType`. When both are present,
        # overwrite `type` so `type_` reflects reality.
        if not isinstance(data, dict):
            return data
        space_type = data.get("spaceType")  # ty: ignore[invalid-argument-type]
        if isinstance(space_type, str) and space_type:
            return {**data, "type": space_type}
        return data

    @field_validator("type_", mode="before")
    @classmethod
    def _normalize_legacy_space_type(cls, v: object) -> object:
        # Covers the older shape where only `type` was returned, using the
        # pre-GA aliases (ROOM / DM / GROUP_DM) before the Literal rejects them.
        return _LEGACY_SPACE_TYPE_ALIASES.get(v, v) if isinstance(v, str) else v


class _ChatSpacesListResponse(_ChatBase):
    spaces: list[_ChatSpaceResponse] = Field(default_factory=list)
    next_page_token: str | None = Field(default=None, alias="nextPageToken")


class _ChatGroup(_ChatBase):
    # Google Group as a space member. `name` is `groups/{id}`.
    name: GroupId
    display_name: str | None = Field(default=None, alias="displayName")


class _ChatMembershipResponse(_ChatBase):
    name: MembershipName
    state: MemberState = Field(alias="state")
    role: MemberRole | None = None
    create_time: datetime | None = Field(default=None, alias="createTime")
    delete_time: datetime | None = Field(default=None, alias="deleteTime")
    # Exactly one of `member` (human) or `groupMember` (Google Group) is
    # populated per membership, per Google's response shape.
    member: _ChatUser | None = None
    group_member: _ChatGroup | None = Field(default=None, alias="groupMember")


class _ChatMembershipsListResponse(_ChatBase):
    memberships: list[_ChatMembershipResponse] = Field(default_factory=list)
    next_page_token: str | None = Field(default=None, alias="nextPageToken")
