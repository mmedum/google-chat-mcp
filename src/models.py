"""Pydantic models for tool I/O and Chat API responses.

Two tiers, facing opposite directions. ``_Strict`` (tool I/O) is our own
contract: ``extra="forbid"`` + ``strict=True``, so an unrecognised key from a
calling model is rejected. ``_ChatBase`` (Chat API responses) is Google's wire
format, which we do not control and which grows without notice:
``extra="allow"``, with unknown keys reported rather than rejected. The runbook
covers the failure mode.
"""

from __future__ import annotations

from datetime import UTC, datetime
from typing import Annotated, Any, Literal

from pydantic import (
    BaseModel,
    ConfigDict,
    EmailStr,
    Field,
    StringConstraints,
    field_validator,
    model_validator,
)

from .observability import logger, mcp_schema_drift_total


class _Strict(BaseModel):
    # `use_attribute_docstrings` is what makes the docstrings below this class
    # reach the MCP tool schemas. Without it every one of them is dev-only
    # commentary — the calling model sees a bare `{"type": "integer"}` and none
    # of the guidance we wrote for it.
    model_config = ConfigDict(
        extra="forbid", strict=True, frozen=True, use_attribute_docstrings=True
    )


# ---------- tool I/O ----------

# Google Chat resource names use IDs of dots, dashes, underscores, and
# alphanumerics. The middle `[A-Za-z0-9]` is mandatory — at least one
# alphanumeric character must appear somewhere in the segment. This closes
# a path-traversal vector where bare `.` or `..` segments would pass the
# regex and let httpx normalize them via RFC 3986 §5.2.4 dot-segment
# resolution, rewriting the upstream URL to target a different resource
# (e.g. `spaces/T/messages/..` → `DELETE /v1/spaces/T`). Pydantic v2's
# Rust regex engine doesn't support lookarounds, so the constraint is
# expressed positionally rather than via `(?=...)`.
_ID = r"[A-Za-z0-9._-]*[A-Za-z0-9][A-Za-z0-9._-]*"
SpaceId = Annotated[str, StringConstraints(pattern=rf"^spaces/{_ID}$")]
ThreadName = Annotated[str, StringConstraints(pattern=rf"^spaces/{_ID}/threads/{_ID}$")]
MessageId = Annotated[str, StringConstraints(pattern=rf"^spaces/{_ID}/messages/{_ID}$")]
UserId = Annotated[str, StringConstraints(pattern=rf"^users/{_ID}$")]
GroupId = Annotated[str, StringConstraints(pattern=rf"^groups/{_ID}$")]
MembershipName = Annotated[str, StringConstraints(pattern=rf"^spaces/{_ID}/members/{_ID}$")]

# Input filter — only values Google accepts as a `spaces.list` filter.
SpaceType = Literal["SPACE", "DIRECT_MESSAGE", "GROUP_CHAT"]
# Output — Google also returns `SPACE_TYPE_UNSPECIFIED`, and can add members to
# the enum. Anything we don't recognise maps here at the tool boundary rather
# than failing the row: `type` is on every space, so a closed Literal on the
# response model would take down every space-shaped tool at once.
SpaceTypeOut = Literal["SPACE", "DIRECT_MESSAGE", "GROUP_CHAT", "SPACE_TYPE_UNSPECIFIED"]
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
    type: SpaceTypeOut
    display_name: str


class ListSpacesInput(_Strict):
    space_type: SpaceType | None = None
    limit: Annotated[int, Field(ge=1, le=200)] = 50


class DirectMessageResult(_Strict):
    space_id: SpaceId


class CreateGroupChatInput(_Strict):
    """Create an unnamed multi-person DM (`spaceType=GROUP_CHAT`).

    Google doesn't allow `displayName` on this space type, so the caller
    cannot set one. `member_emails` excludes the caller — Google adds the
    authenticated user implicitly. Bounds are a UX cap, not Google's
    (Google allows 49 members in addition to the caller).
    """

    member_emails: Annotated[list[EmailStr], Field(min_length=2, max_length=20)]
    dry_run: bool = False


class CreateGroupChatResult(_Strict):
    space_id: SpaceId | None = None
    """None on a dry-run result (nothing created yet); set on a real post."""
    member_count: Annotated[int, Field(ge=0)]
    """Members POSTed to spaces.setup — does NOT include the caller."""
    dry_run: bool = False
    rendered_payload: dict[str, Any] | None = None
    """Dry-run only: the exact body that would be POSTed to spaces.setup."""


class CreateSpaceInput(_Strict):
    """Create a named space (`spaceType=SPACE`, `displayName` required).

    `member_emails` excludes the caller. Google allows an empty member
    list on SPACE creation, but we require at least one — a space of one
    is not a useful primitive from an agent flow.
    """

    member_emails: Annotated[list[EmailStr], Field(min_length=1, max_length=20)]
    display_name: Annotated[str, StringConstraints(min_length=1, max_length=128)]
    dry_run: bool = False


class CreateSpaceResult(_Strict):
    space_id: SpaceId | None = None
    display_name: str
    member_count: Annotated[int, Field(ge=0)]
    dry_run: bool = False
    rendered_payload: dict[str, Any] | None = None


class AddMemberInput(_Strict):
    space_id: SpaceId
    user_email: EmailStr
    dry_run: bool = False


class AddMemberResult(_Strict):
    membership_name: MembershipName | None = None
    """None on dry-run; set on a real post."""
    space_id: SpaceId
    user_email: EmailStr
    dry_run: bool = False
    rendered_payload: dict[str, Any] | None = None


class RemoveMemberInput(_Strict):
    """Single shape only (membership_name). Email-filter path is not exposed
    because Google's People API returns email=null for non-self users in
    practice (see memory/project_people_api_resolution.md), which would
    silently fail the match and return removed=false even when present.
    Callers fetch membership_name via list_members first.
    """

    membership_name: MembershipName
    dry_run: bool = False


class RemoveMemberResult(_Strict):
    membership_name: MembershipName
    removed: bool
    """False when the membership was already gone (404/NOT_FOUND idempotency)."""
    dry_run: bool = False


PeopleSearchSource = Literal["DIRECTORY", "CONTACTS"]


def _default_search_sources() -> list[PeopleSearchSource]:
    return ["DIRECTORY", "CONTACTS"]


class SearchPeopleInput(_Strict):
    query: Annotated[str, StringConstraints(min_length=1, max_length=200)]
    limit: Annotated[int, Field(ge=1, le=100)] = 10
    sources: Annotated[
        list[PeopleSearchSource],
        Field(min_length=1, max_length=2, default_factory=_default_search_sources),
    ]
    """Which upstream to query. DIRECTORY hits searchDirectoryPeople
    (Workspace domain); CONTACTS hits searchContacts (caller's own
    contacts). Default runs both in parallel so both Workspace and
    consumer Gmail deployers get useful results."""


# Addresses we RECEIVE from Google are typed `str`, not `EmailStr`, wherever they
# reach a tool result. Read that as a statement about where validation belongs,
# not as "these can be junk".
#
# **None of these come from Chat.** The Chat API's `User` object has no email
# field at all — it returns `name`, `displayName`, `domainId`, `type` and
# `isAnonymous`, and the docs say only the canonical `users/{id}` is returned.
# So `sender_email` and `Member.email` are People API lookups of that ID, and
# `WhoamiResult.email` is the OIDC `/userinfo` claim. Chat guarantees nothing
# about them because Chat never supplies them.
#
# And People documents no format contract: `EmailAddress.value` is described as
# "The email address", with no stated validation or formatting requirement.
# Declaring `EmailStr` on our side would assert a guarantee the upstream
# explicitly does not make. `searchContacts` is the sharp end — it searches the
# caller's own grouped contacts, i.e. cards a human typed — and a contact saved
# as `bob@nas.local` used to fail the entire `search_people` call.
#
# The deeper reason is that `EmailStr` HERE enforces nothing. The address
# already exists upstream; validating on the way out cannot make it valid, only
# cost us the row or blank the field. Enforcement happens where it is
# meaningful — on *inputs*, which keep `EmailStr`, so an unusable address is
# rejected by `add_member` / `create_space` / `find_direct_message` before it
# reaches Google, with a message naming the problem. Permissive out, strict in.
#
# Same trade `_ChatBase` makes with `extra="allow"`: an upstream we do not
# control must not be able to turn its own data into our outage.
#
# If a stronger output contract is ever wanted (every non-null email guaranteed
# parseable), the place to do it is `_directory.primary_email` — the single
# point every People-sourced address passes through — by nulling invalid values
# rather than raising. Do NOT reintroduce per-call-site checks: that shape was
# already missed on the cache-hit path once.


class PersonHit(_Strict):
    """One hit from a search_people call.

    `user_id` is set (`users/{id}`) only when the upstream hit resolves to
    a Workspace profile ID that shares the Chat API namespace. Contact-ID
    hits from searchContacts surface email+display_name but user_id=None
    since the `people/c{id}` namespace doesn't round-trip to
    `sender.name` in Chat messages.
    """

    user_id: UserId | None
    email: str | None
    display_name: str | None
    source: PeopleSearchSource


class UpdateMessageInput(_Strict):
    """Edit the text of a message you previously sent.

    Text-only — cards / attachments are out of scope (Google's
    `messages.patch` accepts those only via app-auth, not user-auth).
    The 4096-char cap mirrors `SendMessageInput` for project-wide
    consistency; Google's real limit is higher but a uniform bound
    keeps agent flows predictable.
    """

    message_name: MessageId
    text: Annotated[str, StringConstraints(min_length=1, max_length=4096)]
    dry_run: bool = False


class UpdateMessageResult(_Strict):
    message_name: MessageId
    text: str
    dry_run: bool = False
    rendered_payload: dict[str, Any] | None = None


class UpdateSpaceInput(_Strict):
    """Rename a space or update its description via `spaces.patch`.

    At least one of `display_name` / `description` must be supplied —
    Google rejects an empty `updateMask`. The validator enforces this so
    we never send a no-op patch upstream.

    Field bounds match Google's documented limits: `display_name` is
    1-128 characters; `description` is up to 150. Both are text-only;
    permission settings and other space fields are out of scope for v0.4.0.

    **Caveat on `description`:** Google's `spaces.patch` `updateMask`
    accepts only top-level field paths — no `spaceDetails.description`
    sub-path. Updating description means patching the whole
    `spaceDetails` sub-object, so any existing `guidelines` (rules of
    the room) that aren't re-supplied in the same call will be cleared.
    Most spaces don't set guidelines; if yours does, edit via the Chat
    web UI instead of this tool until v1.1 adds a `guidelines` field.
    """

    space_id: SpaceId
    display_name: Annotated[str, StringConstraints(min_length=1, max_length=128)] | None = None
    description: Annotated[str, StringConstraints(max_length=150)] | None = None
    dry_run: bool = False

    @model_validator(mode="after")
    def _require_one_field(self) -> UpdateSpaceInput:
        if self.display_name is None and self.description is None:
            raise ValueError("At least one of `display_name` or `description` must be set.")
        return self


class UpdateSpaceResult(_Strict):
    space_id: SpaceId
    display_name: str | None = None
    description: str | None = None
    dry_run: bool = False
    rendered_payload: dict[str, Any] | None = None
    update_mask: str | None = None
    """Comma-joined `updateMask` actually applied (e.g.
    `displayName,spaceDetails`). Surfaced for observability — callers
    can confirm which fields the patch targeted without re-deriving
    from the input. Google accepts only top-level mask paths for
    `spaces.patch`, so a description update sends `spaceDetails` (whole
    sub-object), not `spaceDetails.description`."""


class DeleteMessageInput(_Strict):
    message_name: MessageId
    dry_run: bool = False


class DeleteMessageResult(_Strict):
    message_name: MessageId
    deleted: bool
    """False on idempotent re-delete (404 NOT_FOUND or non-scope 403)."""
    dry_run: bool = False


class SearchPeopleResult(_Strict):
    people: list[PersonHit]
    total_returned: Annotated[int, Field(ge=0)]
    """Unique hits after cross-source dedupe (people.len())."""
    sources_attempted: list[PeopleSearchSource]
    sources_succeeded: list[PeopleSearchSource]
    """When a source is in `attempted` but not `succeeded`, the upstream
    returned an error (missing scope, empty directory, etc) and the hit
    list was built from the remaining source only. Non-fatal — an empty
    result is preferable to a ToolError since LLM callers will retry
    with broader queries."""


class SendMessageInput(_Strict):
    space_id: SpaceId
    text: Annotated[str, StringConstraints(min_length=1, max_length=4096)]
    thread_name: ThreadName | None = None
    dry_run: bool = False
    """Render the payload without posting. For ungated Agent-SDK loops and
    MCP clients running with `bypassPermissions`: preview, inspect, then
    re-invoke without `dry_run` to actually post."""


class SendMessageResult(_Strict):
    # `message_id` and `thread_id` are None on a dry-run result — nothing was
    # created, so there's no resource name yet. On a real post both are set.
    message_id: MessageId | None = None
    space_id: SpaceId
    thread_id: ThreadName | None = None
    dry_run: bool = False
    rendered_payload: dict[str, Any] | None = None
    """On dry-run, the exact JSON body that would have been POSTed to
    spaces.messages.create. On a real post, None."""


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


class SpaceDetails(_Strict):
    space_id: SpaceId
    type: SpaceTypeOut
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
    email: str | None
    role: MemberRole
    state: MemberState


class ChatMessage(_Strict):
    message_id: MessageId
    sender_user_id: UserId
    sender_email: str | None
    sender_display_name: str | None
    text: str
    timestamp: datetime
    thread_id: ThreadName


class GetThreadInput(_Strict):
    space_id: SpaceId
    thread_name: ThreadName
    limit: Annotated[int, Field(ge=1, le=100)] = 50


class ReactionSummary(_Strict):
    emoji: str
    count: Annotated[int, Field(ge=0)]


# Reaction resources are named like spaces/{s}/messages/{m}/reactions/{r}.
ReactionName = Annotated[
    str, StringConstraints(pattern=rf"^spaces/{_ID}/messages/{_ID}/reactions/{_ID}$")
]


class AddReactionInput(_Strict):
    message_name: MessageId
    emoji: Annotated[
        str,
        StringConstraints(min_length=1, max_length=16, pattern=r'^[^"\\\s]+$'),
    ]
    """Unicode emoji. Custom-emoji reactions are out of scope for v2 — pass the
    unicode glyph (single char or ZWJ sequence). The pattern excludes `"`,
    `\\`, and whitespace to close an AIP-160 filter-injection vector in
    `remove_reaction`'s lookup path: those characters could break out of the
    quoted filter string `emoji.unicode = "{value}"` and broaden the match."""


class AddReactionResult(_Strict):
    reaction_name: ReactionName
    emoji: str
    user_id: UserId


class RemoveReactionInput(_Strict):
    """Either `reaction_name` (exact delete) or (`message_name` + `emoji` + `user_email`)
    (lookup-and-delete). Mutually exclusive."""

    reaction_name: ReactionName | None = None
    message_name: MessageId | None = None
    emoji: (
        Annotated[
            str,
            StringConstraints(min_length=1, max_length=16, pattern=r'^[^"\\\s]+$'),
        ]
        | None
    ) = None
    user_email: EmailStr | None = None

    @model_validator(mode="after")
    def _require_one_shape(self) -> RemoveReactionInput:
        has_direct = self.reaction_name is not None
        has_lookup = (
            self.message_name is not None and self.emoji is not None and self.user_email is not None
        )
        if has_direct == has_lookup:
            raise ValueError(
                "Provide either reaction_name OR (message_name + emoji + user_email), not both."
            )
        return self


class RemoveReactionResult(_Strict):
    reaction_name: ReactionName | None
    removed: bool
    """False when the lookup-by-(emoji, user) path matched zero reactions."""


class ListReactionsInput(_Strict):
    message_name: MessageId
    limit: Annotated[int, Field(ge=1, le=200)] = 50
    page_token: str | None = None


class SearchMessagesInput(_Strict):
    space_id: SpaceId
    """Required. The server will NOT search across spaces — the model should
    direct the user to the Chat web UI for cross-space history."""

    query: str | None = None
    """Exact-substring (case-insensitive) match. Mutually exclusive with regex."""

    regex: str | None = None
    """Python regex (re.search). Mutually exclusive with query."""

    created_after: datetime | None = None
    """Lower bound on message createTime. Strongly recommended — an unbounded
    scan of a large space hits the page cap and returns a partial result."""

    limit: Annotated[int, Field(ge=1, le=100)] = 50
    """Maximum matches returned. Scanning continues until this many hits or
    the page cap is reached."""

    max_pages: Annotated[int, Field(ge=1, le=50)] | None = None
    """Hard ceiling on pages fetched. None → operator default (GCM_SEARCH_MAX_PAGES,
    default 10)."""

    @field_validator("created_after", mode="before")
    @classmethod
    def _parse_iso_created_after(cls, v: object) -> object:
        if isinstance(v, str):
            normalized = v[:-1] + "+00:00" if v.endswith("Z") else v
            try:
                dt = datetime.fromisoformat(normalized)
            except ValueError as exc:
                raise ValueError(f"`created_after` must be ISO-8601; got {v!r}") from exc
            return dt if dt.tzinfo else dt.replace(tzinfo=UTC)
        return v

    @model_validator(mode="after")
    def _exclusive_query_shape(self) -> SearchMessagesInput:
        if (self.query is None) == (self.regex is None):
            raise ValueError("Provide exactly one of `query` (exact substring) OR `regex`.")
        if self.query is not None and not self.query.strip():
            raise ValueError("`query` must be non-empty after strip.")
        return self


class SearchMatch(_Strict):
    message_id: MessageId
    thread_id: ThreadName
    sender_user_id: UserId
    text: str
    timestamp: datetime
    snippet: str
    """Up to ~200 characters of `text` centered on the first match."""


class SearchMessagesResult(_Strict):
    matches: list[SearchMatch]
    scanned: Annotated[int, Field(ge=0)]
    """Total messages fetched from Google before filtering."""
    cap_reached: bool
    """True when scanning stopped at `max_pages` before finding `limit` matches.
    Caller should either narrow the query, raise `created_after`, or accept
    the partial result."""
    unparsed: Annotated[int, Field(ge=0)] = 0
    """Messages counted in `scanned` that failed validation and were never
    searched. Non-zero means the server's models are stale against the Chat API
    and `matches` is INCOMPLETE — this is not the same as "no matches". Report
    it rather than concluding the space has nothing."""


class ReactionEntry(_Strict):
    reaction_name: ReactionName
    emoji: str
    user_id: UserId


class ListReactionsResult(_Strict):
    reactions: list[ReactionEntry]
    next_page_token: str | None = None


class MessageDetails(_Strict):
    """Single message with reaction summaries hydrated inline."""

    message_id: MessageId
    space_id: SpaceId
    thread_id: ThreadName
    sender_user_id: UserId
    sender_email: str | None
    sender_display_name: str | None
    text: str
    timestamp: datetime
    last_update_time: datetime | None = None
    reactions: list[ReactionSummary] = Field(default_factory=list)
    reactions_paged: bool = False
    """True when inline reactions were omitted (listing would be too large);
    client should follow up with `list_reactions`. Inlined summaries stay
    small — this signal is a forward-compatibility hook for messages with
    many distinct reactions."""


class WhoamiResult(_Strict):
    """Identity of the authenticated user."""

    user_sub: str
    email: str | None
    display_name: str | None
    picture_url: str | None = None


# ---------- Chat API response shapes ----------
# Pydantic validators for raw Google JSON. `extra="allow"`: additions from
# Google are kept and reported (`schema_drift` + `mcp_schema_drift_total`)
# rather than rejected. A field we *read* changing shape still raises, and
# `invoke_tool` logs its path via `drift_fields`.


# Unknown wire keys already reported, so drift is logged once per process
# rather than once per row. Bounded by Google's field vocabulary, not by
# traffic.
_reported_drift: set[str] = set()


class _ChatBase(BaseModel):
    """Base for Chat API response shapes.

    `extra="allow"`, not `forbid`. Google adds response fields without notice —
    AIP-180 explicitly permits it — and every such field lands on *every* row of
    its resource, so `forbid` converted a routine, backwards-compatible change
    into a total outage twice. The invariant we actually want is that drift is
    **observable**, not that it fails the call, so unknown keys are accepted and
    reported instead.

    This does not make the models permissive about what we read: `name`,
    `sender`, `create_time` and `thread` have no defaults, so a field being
    removed still fails loudly. And a *renamed* field is caught here too — the
    new name arrives as an unknown key even though the old one merely goes
    missing.

    Tool I/O keeps `extra="forbid"` (see `_Strict`). That side is our own
    contract, and rejecting a hallucinated key from a calling model is a real
    safety property — a misspelled `dry_run` must not silently post for real.
    """

    model_config = ConfigDict(extra="allow", populate_by_name=True)

    @model_validator(mode="after")
    def _report_unknown_fields(self) -> _ChatBase:
        for key in self.model_extra or {}:
            location = f"{type(self).__name__}.{key}"
            # Count every occurrence but log only the first. The runbook tells
            # operators to alert on `rate(mcp_schema_drift_total)`; if the
            # counter were deduped too, the rate would spike once and then
            # self-resolve while the models were still stale. The log is deduped
            # because one line per row per page is noise, not signal.
            mcp_schema_drift_total.labels(location).inc()
            if location in _reported_drift:
                continue
            _reported_drift.add(location)
            # Key names only — never the value. Google's field names are safe
            # to log; the content behind them is message text and emails.
            logger.warning("schema_drift", location=location)
        return self


class _ChatUser(_ChatBase):
    name: UserId
    # `str`, not `Literal` — see the rule on `_ChatMessageResponse`. Google's
    # User.Type also has `TYPE_UNSPECIFIED`, and `_ChatUser` is embedded in
    # every message's `sender` and every membership's `member`, so a Literal
    # here fails every row of both resources. Nothing reads this field:
    # `Member.kind` comes from which of `member`/`groupMember` is populated.
    type_: str | None = Field(default=None, alias="type")
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
    # The embedded space reference varies in shape and we read none of it,
    # so it stays an opaque dict rather than a nested model.
    space: dict[str, object] | None = None
    argument_text: str | None = Field(default=None, alias="argumentText")
    fallback_text: str | None = Field(default=None, alias="fallbackText")
    thread_reply: bool | None = Field(default=None, alias="threadReply")
    client_assigned_message_id: str | None = Field(default=None, alias="clientAssignedMessageId")
    # Opaque fields — declared but never rendered. Since `extra="allow"` they
    # would parse either way; declaring them keeps `schema_drift` quiet for
    # shapes we already know about. Enum-shaped ones are
    # `str`, never `Literal`: a value we don't know yet must not be able to
    # reject the message it arrives on.
    markup_syntax: str | None = Field(default=None, alias="markupSyntax")
    attachment: list[dict[str, object]] | None = None
    cards_v2: list[dict[str, object]] | None = Field(default=None, alias="cardsV2")
    cards: list[dict[str, object]] | None = None
    emoji_reaction_summaries: list[_ChatEmojiReactionSummary] | None = Field(
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
    # `str`, not `Literal` — `type` is present on every space, so a closed set
    # here fails every space-shaped tool at once the moment Google returns a
    # value we don't know (`SPACE_TYPE_UNSPECIFIED` already exists). The tool
    # boundary narrows it via `space_type_out`.
    type_: str = Field(alias="type")
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
        # pre-GA aliases (ROOM / DM / GROUP_DM). Still load-bearing now that
        # `type_` is a plain `str`: without it, `ROOM` would reach
        # `space_type_out`, bucket into SPACE_TYPE_UNSPECIFIED and raise a
        # false drift alert on every legacy space.
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
    # `str`, not `Literal`. `state` is required and present on every membership
    # row, so a closed set here is `affiliation` all over again the first time
    # Google extends MembershipState. The tool boundary narrows both via
    # `narrow_enum`, bucketing anything new into the `*_UNSPECIFIED` member
    # these enums already carry.
    state: str
    role: str | None = None
    create_time: datetime | None = Field(default=None, alias="createTime")
    delete_time: datetime | None = Field(default=None, alias="deleteTime")
    # Exactly one of `member` (human) or `groupMember` (Google Group) is
    # populated per membership, per Google's response shape.
    member: _ChatUser | None = None
    group_member: _ChatGroup | None = Field(default=None, alias="groupMember")
    # Opaque: whether the member joined directly or via their domain. `str`,
    # never `Literal`, for the same reason as `_ChatMessageResponse`'s.
    affiliation: str | None = None


class _ChatMembershipsListResponse(_ChatBase):
    memberships: list[_ChatMembershipResponse] = Field(default_factory=list)
    next_page_token: str | None = Field(default=None, alias="nextPageToken")


class _ChatCustomEmoji(_ChatBase):
    uid: str | None = None
    name: str | None = None


class _ChatEmoji(_ChatBase):
    """Emoji object in Chat API reactions. Either `unicode` or `custom_emoji` is set."""

    unicode: str | None = None
    custom_emoji: _ChatCustomEmoji | None = Field(default=None, alias="customEmoji")

    @property
    def display(self) -> str | None:
        """Pick the unicode glyph, falling back to a custom-emoji uid/name."""
        if self.unicode:
            return self.unicode
        if self.custom_emoji is not None:
            return self.custom_emoji.uid or self.custom_emoji.name
        return None


class _ChatEmojiReactionSummary(_ChatBase):
    """Entry in a message's `emojiReactionSummaries` array."""

    emoji: _ChatEmoji
    reaction_count: int = Field(default=0, alias="reactionCount")


class _ChatReactionResponse(_ChatBase):
    """One reaction resource (create / list element)."""

    name: str
    user: _ChatUser
    emoji: _ChatEmoji


class _ChatReactionsListResponse(_ChatBase):
    reactions: list[_ChatReactionResponse] = Field(default_factory=list)
    next_page_token: str | None = Field(default=None, alias="nextPageToken")


class _UserInfoResponse(_ChatBase):
    """OIDC /userinfo payload.

    Derives from `_ChatBase`, not bare `BaseModel`: it took the permissive half
    of the convention (`extra="allow"`) without the half that makes drift
    visible, so a new OIDC claim incremented no counter and logged nothing.
    HTTPS deployments have no `doctor` command, so that was the only signal
    they would ever get. The standard
    OIDC claims below are declared even though only four are read: `doctor`
    reports every unmodelled key it sees, and Google returns `given_name`,
    `family_name` and `locale` for any account with the `profile` scope plus
    `hd` for any Workspace account. Leaving them undeclared made `doctor`
    report drift on every run for every user — an alert that always fires is
    the same as no alert.

    `email` is a plain `str` for the same reason every Google-sourced address
    in this module is.
    """

    sub: str
    email: str | None = None
    email_verified: bool | None = None
    name: str | None = None
    picture: str | None = None
    given_name: str | None = None
    family_name: str | None = None
    locale: str | None = None
    hd: str | None = None
