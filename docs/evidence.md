# Evidence

Each entry records what was checked about an upstream system, against
which source, and what a live call showed. An entry supports a decision
in [architecture.md](architecture.md) or the code that cites it, or
refutes an assumption of ours so nobody proposes it again.

## Google Chat API: messages and search

### E-001 The message-search filter spells the time field `create_time`

Supports: none
Source says: the REST reference's own examples spell the field `createTime`, and the guide spells it `create_time`.
Observed: Google answers "Invalid filter query" to `createTime` sent as the only clause, and accepts `create_time`; Chat API v1, 2026-09-05.
Verdict: contradicted — the reference's examples use a spelling Google rejects.
Held by: live

### E-002 The space-event filter spells its field `event_types`

Supports: none
Source says: the [spaces.spaceEvents.list](https://developers.google.com/workspace/chat/api/reference/rest/v1/spaces.spaceEvents/list) prose calls the field `event_type`, and its examples write `event_types`.
Observed: Google takes the examples' plural spelling; the prose spelling is wrong; Chat API v1, 2026-09-06. With E-001 the reference is wrong in both directions, so neither its prose nor its examples can be trusted unrun.
Verdict: contradicted — the prose names a field Google does not read.
Held by: live

### E-003 A message search of one space still names `spaces/-` as its parent

Supports: none
Source says: nothing; the reference does not say a single space is refused as the parent.
Observed: Google refuses one space as the parent with "Invalid parent. Specify 'spaces/-'", and says to limit the search with `space.name` or `space.display_name` in the filter instead; Chat API v1, 2026-09-05.
Verdict: confirmed — Google's own error names the fix, and the server puts the space in the filter.
Held by: live

### E-004 Every media download arrives as `application/octet-stream`

Supports: none
Source says: nothing; the attachment resource declares its own `contentType`, and the media endpoint does not say what it serves.
Observed: a file whose attachment said `text/plain` downloaded as `application/octet-stream`; Chat API v1 media endpoint, 2026-09-06.
Verdict: confirmed — undocumented; the transfer's type says nothing, so `download_attachment` reports the attachment's declared type first.
Held by: live

### E-005 An empty page can still carry a next page token

Supports: none
Source says: nothing; the list references do not say a page can be empty before the end.
Observed: a space holding two messages answered `pageSize=1` with no messages and a page token, because Google applies the page size before it filters; Chat API v1, 2026-09-07.
Verdict: confirmed — undocumented; a caller that stops on an empty page reports a space with messages in it as quiet, so the server instructions say an empty page is not the end.
Held by: live

### E-006 A link in a message lives in an annotation, and a link to one message is `CHAT_SPACE`

Supports: none
Source says: the [Chat discovery document](https://chat.googleapis.com/$discovery/rest?version=v1) puts a link's target in `annotations[].richLinkMetadata`, with `richLinkType` one of `DRIVE_FILE`, `CHAT_SPACE`, `GMAIL_MESSAGE`, `MEET_SPACE` and `CALENDAR_EVENT`, and a message in `chatSpaceLinkData.message`.
Observed: a link pasted in the Chat client read back through the shipped binary as `CHAT_SPACE` naming the space, thread and message, spanning the whole body, while `text` held only the anchor words; Chat API v1, 2026-09-17.
Verdict: confirmed — there is no `CHAT_MESSAGE`, and dropping the annotation leaves a message that reads as a bare word.
Held by: live

### E-007 Chat makes no rich link for a link posted through the API

Supports: none
Source says: nothing.
Observed: a message whose body is a message URL, posted through the API, came back with no annotations, both for a URL of the live driver's shape and for a permalink copied byte for byte; Chat API v1, 2026-09-17.
Verdict: confirmed — undocumented; the Chat client makes the chip, so only a link a person pastes produces one, and the live suite cannot make that case itself.
Held by: live

### E-008 A message permalink is `room/{space}/{thread}/{message}?cls=10`

Supports: none
Source says: nothing.
Observed: read off a real Copy link in the Chat client, 2026-09-17. A message created through the API is named `{id}.{id}`, and the URL carries the half before the dot, which is also its thread's id.
Verdict: confirmed — undocumented; the live driver builds this shape, so the chip question it asks is the one a person's paste raises.
Held by: live

### E-009 A message search returns the matched message, annotations included

Supports: none
Source says: the [Chat discovery document](https://chat.googleapis.com/$discovery/rest?version=v1) has `view` gate the metadata around a hit, `read` and `spaceMuteSetting`, and describes the basic view as "only the matched messages … but no additional metadata".
Observed: not settled live: the hit the run searched for carried no annotations for either route to drop, because the API had made none (E-007); Chat API v1, 2026-09-17.
Verdict: confirmed — by the discovery document alone, so a search hit's links come from the same annotations a read gives.
Held by: review

### E-010 A quoted message's `sender` is a display name

Supports: none
Source says: the reference calls it "the quoted message's author name".
Observed: a real reply-quote and a real forward, read through the shipped binary, each carried a person's name rather than `users/{id}`; Chat API v1, 2026-09-17.
Verdict: refuted — our reading of it as a resource name. It is passed through verbatim and never looked up, since `people/Jane Doe` is a malformed id that costs a junk entry in the People batch.
Held by: live

### E-011 A forward names the space it came from, and a direct message by the other person

Supports: none
Source says: the reference has `forwardedMetadata` carry the source space and its display name, which for a direct message is the other participant.
Observed: a real forward carried both; whether a forward also carries the links and files the reference promises is unverified, since the one read had neither; Chat API v1, 2026-09-17.
Verdict: confirmed — for the space and its name.
Held by: live

### E-012 A reply-quote's snapshot carries only the sender and the text

Supports: none
Source says: the reference documents `formattedText`, `annotations` and `attachments` on a quote's snapshot as populated for `FORWARD` alone, and `forwardedMetadata` only on a forward.
Observed: the reference, Chat API v1, read 2026-09-17.
Verdict: confirmed — so an empty link list on a reply-quote is Google saying nothing, and the tool schemas say which field is which. For a forward the snapshot is the only copy a caller can reach, since the source space is usually one they are not in.
Held by: review

### E-013 A person's token can quote a message in its own space, and cannot forward

Supports: none
Source says: the create-messages guide quotes with `chat.messages.create` under user authentication; a quote must carry the quoted message's latest `lastUpdateTime`; Google's release notes make `FORWARD` generally available.
Observed: a `REPLY` quote posted and read back. `FORWARD` was refused, "Can't forward a message", from main chat into its own space, between threads of one space, and into another space; Chat API v1, 2026-10-08.
Verdict: contradicted — a person's token cannot forward, so `send_message` quotes within one space, reading the timestamp first.
Held by: live

### E-014 A search hit may leave out `read` when it is false

Supports: none
Source says: Google's JSON leaves a false boolean out, so an unread hit and a hit fetched without the read-state scope both arrive with no `read`.
Observed: not settled: every message in a scratch space is the caller's own, and a hit in a space just marked unread still came back read; Chat API v1, 2026-10-08.
Verdict: confirmed — for the omitted boolean. The server takes one hit marked read, or `unread_only`, as proof the silent hits are unread, and says null otherwise.
Held by: review

### E-015 A person's token can post Markdown

Supports: none
Source says: the REST reference calls `markupSyntax` output only. Discovery revision 20261005 calls it "Optional. Specifies how the server interprets the message `text`", and Google's release notes make Markdown generally available for messages created through the API.
Observed: a Markdown post came back with its bold word in Chat's markup, and its plain text without the asterisks; Chat API v1, 2026-10-08.
Verdict: contradicted — the REST reference's "output only" is wrong.
Held by: live

### E-016 The media upload protocol is documented only in the discovery document

Supports: none
Source says: the upload guide does not say how; the [Chat discovery document](https://chat.googleapis.com/$discovery/rest?version=v1) makes `media.upload` a POST to the `upload/v1` base with a JSON metadata part, and documents `downloadUri` as not for downloading.
Observed: read in the discovery document, Chat API v1, recorded by 2026-09-06.
Verdict: confirmed — the guide is silent, so the client derives the upload base from the Chat base, and never fetches a `downloadUri`.
Held by: live

## Google Chat API: deletes and members

### E-017 A repeat message delete finds a tombstone, not 404

Supports: D-34
Source says: nothing; the [messages.delete](https://developers.google.com/workspace/chat/api/reference/rest/v1/spaces.messages/delete) reference does not describe a message already deleted.
Observed: reading a deleted message answers 200 with a tombstone body, not 404; Chat API v1, 2026-09-05.
Verdict: contradicted — the reference does not describe the tombstone, so a repeat delete must read the answer rather than the status.
Held by: live

### E-018 A deleted space answers 403, not 404

Supports: D-34
Source says: nothing.
Observed: a repeated space delete and a read of the deleted space each got 403, as the caller on 2026-09-05 and under admin access on 2026-10-31; Chat API v1.
Verdict: confirmed — undocumented; "deleted" cannot be told from "not yours", and the refusal message does not claim to know which.
Held by: live

### E-019 A forced delete takes a thread's replies, and a deleted root is untested

Supports: D-23
Source says: discovery revision 20261008, `messages.delete`: "When `true`, deleting a message also deletes its threaded replies. When `false`, if a message has threaded replies, deletion fails." It says nothing of a root already deleted.
Observed: that state cannot be made through the API, since a root with replies goes only with `force`, which takes them too; Chat API v1, 2026-10-23.
Verdict: confirmed — for a live root; for a deleted one, `delete_thread` reports whatever Google answers.
Held by: TestDeleteThreadReportsARefusal

### E-020 A Google Group brings its outside members into a space that admits outsiders, at the add and after it

Supports: D-21, D-32
Source says: [Add a Google Group to a space](https://support.google.com/chat/answer/13535086): "If Allow people outside your organization to join is enabled, the group and its members are invited to the space"; external members are "excluded in spaces with guest access turned off"; "Any changes to the group's membership ... are also automatically reflected in the space".
Observed: the help page, read 2026-10-23; not probed live, since that would put real people in a scratch space.
Verdict: confirmed — by the help page alone.
Held by: review

### E-021 Google ignores a role given when it adds a member

Supports: D-22
Source says: nothing; the reference does not say a role on create is dropped.
Observed: an add that asked for `ROLE_MANAGER` answered 200 and recorded `ROLE_MEMBER`, saying nothing; the result read the role off the answer, which made it visible; Chat API v1, 2026-09-05.
Verdict: confirmed — undocumented; `add_member` takes no role and reports the role Google recorded.
Held by: live

### E-022 Chat fills `User.email` for senders and members, external people included

Supports: D-35
Source says: discovery revision 20260922: filled under user authentication for a message's `sender`, a mention and a `Membership`, "provided the user is a member of the space or has prior affinity".
Observed: across one Workspace account's spaces, with this server's scopes, every same-domain human member and nearly every sender carried it, as did external people in a space admitting guests. Apps never did. Humans without one, mostly in direct messages, did not resolve in People either; Chat API v1, 2026-09-27.
Verdict: confirmed — Chat's address comes first, and People fills a gap.
Held by: live

## Google Chat API: spaces

### E-023 A space's audience goes in a patch of its own

Supports: D-20
Source says: discovery revision 20260928, `spaces.patch`: `access_settings.audience` needs every other mask left out, a named space, a space manager, user authentication and no import mode.
Observed: the mask `accessSettings.audience` with `audiences/default` opened a space, and the same mask with no value made it private, each read back through `get_space`; Chat API v1, 2026-10-03.
Verdict: confirmed — and masking the audience with no value removes it.
Held by: TestBuildUpdateSpaceAudienceMasksOnlyTheAudience, live

### E-024 Who may set a space's audience is documented two ways

Supports: D-20
Source says: the guide "Make a space discoverable to a target audience" asks for a super administrator; the [Chat discovery document](https://chat.googleapis.com/$discovery/rest?version=v1) asks only for a space manager.
Observed: not settled: the live run signs in as an administrator, so it cannot tell the two rules apart; Chat API v1, 2026-10-03.
Verdict: contradicted — the guide and discovery disagree; the tool description names only the space-manager rule the reference states.
Held by: review

### E-025 No API lists target audiences

Supports: D-20
Source says: the target-audience guide gives an audience's id from the Admin console URL, `admin.google.com/ac/targetaudiences/{id}`, and names `audiences/default` as the organization's default.
Observed: the guide and the Chat discovery document, read 2026-10-03, name no method that lists audiences.
Verdict: confirmed — so `audience` takes an id and cannot offer the caller a list of choices.
Held by: review

### E-026 A space search without admin access finds only joined spaces

Supports: D-28
Source says: the [spaces.search](https://developers.google.com/workspace/chat/api/reference/rest/v1/spaces/search) reference limits results to "spaces where the calling user is a joined member"; the guide says only "spaces they have access to".
Observed: a search with no name filter returned exactly the named spaces `list_spaces` returned; Chat API v1, 2026-10-08.
Verdict: confirmed — the reference is right and the guide is vague; reaching other spaces is `admin_search_spaces`.
Held by: live

### E-027 Google pages and counts a space search only under admin access

Supports: D-28
Source says: discovery revision 20261026, `SearchSpacesResponse`: `nextPageToken` and `totalSize` are each "Only populated when `useAdminAccess` is set to `true`".
Observed: the discovery document, read 2026-10-31.
Verdict: confirmed — so `search_spaces`, which has no admin access, takes no `page_token` and never sets `next_page_token` or `total_size`; `admin_search_spaces` pages.
Held by: review

### E-028 Google counts a space's name, description and guidelines in UTF-16 units

Supports: none
Source says: the Space reference allows a description of 4,096 characters and guidelines of 5,000; Google's refusal of a long name says 128.
Observed: 4,096 two-byte letters were kept in a description, and 2,049 emoji, 4,098 UTF-16 units, were refused with "Maximum length of the field is 4096". Guidelines and names went the same way at 5,000 and 128; Chat API v1, 2026-10-31.
Verdict: contradicted — Google counts UTF-16 units, not characters or bytes, so an emoji counts as two.
Held by: TestGuidelinesAreBoundedByGooglesLimit, live

### E-029 Who may reply changes only in a space where members cannot post

Supports: none
Source says: nothing; neither the Space reference nor the Chat Help Center says when `replyMessages` can change.
Observed: where members post, Google refused it to the space's own manager as "the permission settings are immutable", even for a patch that changed nothing, and under admin access as "permission denied". In an `ANNOUNCEMENT_SPACE` it changed both ways. A patch without `managersAllowed` is "Invalid combination of permissions"; Chat API v1, 2026-10-31.
Verdict: confirmed — undocumented; the server refuses `reply_messages` where members may post, before anything is sent.
Held by: TestReplySettingChangesOnlyWhereMembersCannotPost, live

### E-030 Google writes a time it does not have as the Unix epoch

Supports: none
Source says: nothing.
Observed: an administrator's `spaces.get` answered `lastActiveTime` `1970-01-01T00:00:00Z` for a space whose search row gave a real time; Chat API v1, 2026-10-31.
Verdict: confirmed — undocumented; it is a protobuf Timestamp of zero, so every timestamp the server reads takes the epoch as absent.
Held by: TestGetSpaceReadsTheEpochAsNoTime

### E-031 `spaces.findDirectMessage` answers with the one direct message between the caller and a person

Supports: D-17
Source says: the `spaces.findDirectMessage` reference returns the direct message between the caller and the user named.
Observed: a write to a direct message that named the wrong person's address was refused `[stale]` by the check built on it; live run, Chat API v1, 2026-10-27.
Verdict: confirmed — the space Google finds for the address given as `space_display_name` has to be the space written to.
Held by: TestCheckSpaceHoldsTheNameToTheSpace, live

## Google Chat API: administrator access

### E-032 Only the spaces and members methods take `useAdminAccess`

Supports: D-28, D-30
Source says: discovery revision 20261026 gives the parameter to spaces get, search, patch and delete, and to members list, get, create, patch and delete, and the admin-overview guide names the same methods. Reads take `chat.admin.spaces.readonly` or `chat.admin.memberships.readonly`, or the umbrella beside each; writes take `chat.admin.spaces` or `chat.admin.memberships`.
Observed: read 2026-10-31.
Verdict: confirmed — nothing reaches messages as an administrator, and nothing creates a space.
Held by: TestAdminAccessRunsTheNineAdminMethodsAndRefusesTheRest

### E-033 An administrator's space delete needs `chat.admin.delete`

Supports: D-33
Source says: the [spaces.delete](https://developers.google.com/workspace/chat/api/reference/rest/v1/spaces/delete) reference and discovery revision 20261026 agree: an administrator's delete takes `chat.admin.delete`, and `chat.admin.spaces` cannot delete.
Observed: read 2026-10-31; `admin_delete_space` deleted a space in the live run that day.
Verdict: confirmed — it is the one restricted administrator scope, so only the destructive flag asks for it.
Held by: TestLoginAsksForTheAdminDeleteOnlyWithDeletesOn, live

### E-034 An administrator's space search must name the customer

Supports: D-30
Source says: the [spaces.search](https://developers.google.com/workspace/chat/api/reference/rest/v1/spaces/search) reference: "`customer` is required when `useAdminAccess` is set to `true`", and `customers/my_customer` is its only value.
Observed: a search with the clause, every administrator filter and each order succeeded; live run, Chat API v1, 2026-10-31.
Verdict: confirmed — every administrator search names `customer = "customers/my_customer"`.
Held by: TestAdminSearchSaysWhoseSpaces, live

### E-035 An administrator's member listing needs a member type filter

Supports: D-30
Source says: the [members.list](https://developers.google.com/workspace/chat/api/reference/rest/v1/spaces.members/list) reference requires `member.type = "HUMAN"` or `member.type != "BOT"` under admin access, lists no apps, and does not say whether `showInvited` works there.
Observed: a listing with the filter succeeded; `showInvited` under admin access is untested; live run, Chat API v1, 2026-10-31.
Verdict: confirmed — the server sends `member.type != "BOT"`, the one that keeps Google Group memberships, since a filter drops a group that does not match it.
Held by: TestAdminMemberListingCarriesTheFilterGoogleRequires, live

### E-036 An administrator's patch may change a name, the details and the permission settings

Supports: D-29
Source says: the [spaces.patch](https://developers.google.com/workspace/chat/api/reference/rest/v1/spaces/patch) reference refuses `space_type`, `space_history_state`, `access_settings.audience` and `access_settings.access_permission_settings` under admin access, and says nothing of the other fields.
Observed: one patch changed a name, a description with the guidelines kept, and several permission settings, written as `permission_settings.{key}` and `permissionSettings.{key}` alike; Chat API v1, 2026-10-31.
Verdict: confirmed — beyond what the refusals imply; `viewSpaceMembership` must go with `access_settings.access_permission_settings`, so it is left out.
Held by: live

### E-037 An administrator reaches a space the caller is not in, and Google deletes a space left empty

Supports: D-28, D-49
Source says: nothing; no page says Google deletes an empty space.
Observed: a space the run made and then left empty took an administrator's read, edit and member listing, and was gone 5 to 10 seconds after the leave. A space with a second person in it stayed after an administrator removed the caller, and `admin_delete_space` deleted it; Chat API v1, 2026-10-31.
Verdict: confirmed — undocumented; the live driver never leaves a space, and `admin_remove_member` is excused from it.
Held by: live

### E-038 Members see an administrator's change, but not who made it

Supports: D-28
Source says: the admin-overview guide, "What users see in Chat": "The organization's administrator performed" the action, and it "does not identify the administrator". The Reports API's Chat events record `actor_type` `ADMIN` for a space's details, name, deletion and memberships, and no event covers a permission change.
Observed: both read 2026-10-31.
Verdict: confirmed — by the guide and the Reports API reference.
Held by: review

### E-039 One Admin console privilege covers an administrator's reads and writes

Supports: D-33
Source says: [Admin Help 13369245](https://support.google.com/a/answer/13369245): "Super admins have the manage chat and spaces privilege by default". The privilege list, [Admin Help 1219251](https://support.google.com/a/answer/1219251), has no read-only form of it.
Observed: both read 2026-10-31.
Verdict: confirmed — reads and writes part at the OAuth scope, not at the privilege.
Held by: review

## OAuth and scopes

### E-040 An umbrella scope satisfies every narrower scope split out of it

Supports: D-37
Source says: the [Chat discovery document](https://chat.googleapis.com/$discovery/rest?version=v1) lists each method's accepted scopes, with the umbrella scope beside the narrower one split out of it.
Observed: the umbrella table in `internal/scopes` checked against discovery, Chat API v1, recorded by 2026-09-06.
Verdict: confirmed — without the table the server refuses calls Google allows, and refuses them for the people who paid the most for consent.
Held by: TestAdminUmbrellasCoverTheirReads, TestImpliesOnlyNamesRequestedScopes

### E-041 `chat.bot` cannot be granted to a per-user client

Supports: D-39
Source says: Google's Chat scope reference: "This scope only supports app authentication with service accounts. You can't authenticate with user credentials or with domain-wide delegation using this scope."
Observed: read 2026-09-08; `chat.bot` is the only scope `spaces.messages.attachments.get` accepts.
Verdict: confirmed — that method is out of reach and nothing is lost by it: a message carries its attachment's `attachmentDataRef`, and the bytes come from `media.download`, which accepts `chat.messages.readonly`.
Held by: api-coverage

### E-042 A desktop client's login can grant some scopes and not others

Supports: D-37
Source says: Google's native-app OAuth guide, step 6: "users may not grant your app access to all of them", except "apps marked as Trusted" and Workspace Enterprise apps with domain-wide delegation, which "either receive all requested scopes or none". The granular-permissions page ties `enable_granular_consent` to client ids "created before 2019".
Observed: both read 2026-10-31.
Verdict: refuted — our earlier reading that a desktop client gets the choice only with `enable_granular_consent`, which login does not send.
Held by: TestGrantedScopesReadsWhatGoogleReturned

### E-043 An Internal app's consent screen needs no scope list

Supports: none
Source says: Google's configure-oauth-consent guide: "you only need to list scopes for apps used by people outside your Google Workspace organization", and for internal apps "scopes aren't listed on the consent screen".
Observed: an Internal app's token held scopes its Data Access page does not list, among them `chat.spaces`, `chat.delete`, `contacts.readonly` and the `chat.users` scopes, with no broader listed scope to account for them; 2026-10-31.
Verdict: refuted — the setup guide's claim that a scope left off the screen is never granted; it asks for the list on External apps only.
Held by: live

### E-081 An External app in Testing status gets refresh tokens that expire after seven days

Supports: none
Source says: [Using OAuth 2.0 to Access Google APIs](https://developers.google.com/identity/protocols/oauth2), "Refresh token expiration": a Google Cloud project with an OAuth consent screen configured for an external user type and a publishing status of "Testing" is issued a refresh token expiring in 7 days.
Observed: the page, read 2026-11-03; not observed live.
Verdict: confirmed — by Google's documentation; a token that stops refreshing a week after login answers `invalid_grant`, which signing in again fixes.
Held by: review

## Cloud Identity

### E-044 Chat names a group by its Cloud Identity name, which `groups.lookup` finds from an address

Supports: D-21
Source says: Chat discovery revision 20260928: a group member's name is "a group in Cloud Identity Groups API. Format: groups/{group}", `spaces.setup` says to find it with `groups.lookup`, and a group's email is not accepted. [Cloud Identity discovery](https://cloudidentity.googleapis.com/$discovery/rest?version=v1) revision 20260930: `GET v1/groups:lookup?groupKey.id=<email>` answers `{"name": "groups/{id}"}`, and `groups.get` gives `displayName` and the address in `groupKey.id`.
Observed: both discovery documents, read 2026-10-03.
Verdict: confirmed — `find_group` asks Cloud Identity and hands the name to `add_member` unchanged.
Held by: TestLookupGroupAsksCloudIdentity

### E-045 `cloud-identity.groups.readonly` is the narrowest scope that reads a group

Supports: D-37
Source says: Cloud Identity discovery revision 20260930: `groups.lookup` and `groups.get` accept it, `cloud-identity.groups` and `cloud-platform`. Google's scopes page gives it no tier, and the setup guide says an ordinary user may call the API with their own OAuth sign-in.
Observed: read 2026-10-03.
Verdict: confirmed — the other two scopes are wider, so a group read asks for this one.
Held by: review

### E-046 A missing group and a hidden one both answer 403

Supports: none
Source says: Google's help on group settings says a group's "Who can see group" decides who can find it, and lists "Group members" first; no method says which groups a caller can see.
Observed: `groups.lookup` answered 403, not 404, for an address no group has; Cloud Identity v1, 2026-10-03.
Verdict: confirmed — undocumented per method; `find_group` says it cannot tell a missing group from a hidden one, rather than claiming either.
Held by: TestFindGroupRefusals, live

### E-047 A group's direct members come from `groups.memberships.list`

Supports: none
Source says: Cloud Identity discovery revision 20260930: it accepts `cloud-identity.groups.readonly` and returns each member's address in `preferredMemberKey.id`, its roles and its type. `searchTransitiveMemberships` reaches nested members, but only Enterprise and Cloud Identity Premium accounts may call it, and the rest get a 403.
Observed: read 2026-10-08.
Verdict: confirmed — Google shows a group's members only to someone the group's settings allow, so a refusal costs that group's row its members and nothing else.
Held by: TestListMembersSaysWhyAGroupWasNotExpanded

### E-048 The `customers.userinvitations` methods publish no scope

Supports: D-39
Source says: Cloud Identity discovery revision 20260930 lists no scope for any `customers.userinvitations` method.
Observed: the discovery document, read 2026-10-03.
Verdict: confirmed — no requested scope can authorize such a method, so the coverage gate marks it `unreachable` with the reason `none published`, since there is no scope to name.
Held by: api-coverage

### E-049 Every Google API takes the `fields` system parameter

Supports: D-08
Source says: [System parameters](https://cloud.google.com/apis/docs/system-parameters): `fields` selects the fields a response carries, on every Google API.
Observed: a group read with `fields=name,groupKey,displayName` decodes with no drift; without it each read logged the group's parent, labels and timestamps as drift; Cloud Identity v1, 2026-10-03.
Verdict: confirmed — a read that models few fields asks for those alone, and raises no false alarm.
Held by: review

### E-050 A disabled API answers 403 `SERVICE_DISABLED`

Supports: none
Source says: the AIP-193 `ErrorInfo` reason for it is `SERVICE_DISABLED`; Google's older frontend says `accessNotConfigured`.
Observed: the first Cloud Identity call a person makes before enabling that API on the OAuth client's project answers it; Cloud Identity v1, recorded 2026-10-03.
Verdict: confirmed — reported as a plain permission refusal it points at the wrong fix, so it has its own message naming the setup page.
Held by: review

## Retries and quotas

### E-051 Chat's quota is 15 reads and 1 write per second per user

Supports: D-38
Source says: the [Chat API usage limits](https://developers.google.com/workspace/chat/limits) page gives 15 reads and 1 write per second for each user.
Observed: the limits page, read 2026-09-05.
Verdict: confirmed — the limiter defaults stay inside it for one process, and a 429 from Google is still handled with backoff.
Held by: TestTheReadLimiterIsApplied

### E-052 A 503 does not prove Google turned a write away

Supports: D-38
Source says: [google/rpc/code.proto](https://github.com/googleapis/googleapis/blob/master/google/rpc/code.proto) defines `UNAVAILABLE`, which maps to HTTP 503, as transient, and adds "it is not always safe to retry non-idempotent operations".
Observed: read 2026-10-01.
Verdict: refuted — our earlier rule that a 503 meant Google turned the request away. A write with no idempotency key retries on 429 alone, and a 503 is as ambiguous as a 500.
Held by: TestANonIdempotentWriteIsNotRetriedOnAnAmbiguous5xx

## MCP clients

### E-053 `destructiveHint` false means the tool only adds

Supports: D-11
Source says: the [MCP schema](https://modelcontextprotocol.io/specification/2025-11-25/schema), `ToolAnnotations`: "If false, the tool performs only additive updates", and the default is true.
Observed: the 2025-11-25 schema, read 2026-10-31.
Verdict: confirmed — a tool that replaces or removes anything says true, a message edit and a removed reaction included, though it needs no flag to be registered.
Held by: TestOnlyToolsThatAddSayTheyAreNotDestructive

### E-054 VS Code prompts by `readOnlyHint`, and Codex by `destructiveHint`

Supports: D-11
Source says: claude.ai's and ChatGPT's documentation say `destructiveHint` decides their prompt.
Observed: VS Code's source never reads `destructiveHint`, and prompts on every tool without `readOnlyHint: true`. Codex's source prompts by `destructiveHint` in its default tool mode. Both read 2026-10-23.
Verdict: confirmed — `readOnlyHint` goes only on a tool that changes nothing, so `download_attachment`, which writes a local file, does not carry it.
Held by: TestReadOnlyToolsAreAnnotated

### E-055 A remembered approval covers a whole tool, never an argument

Supports: D-16, D-28
Source says: nothing in the MCP specification; each client documents its own approvals.
Observed: claude.ai, Codex, VS Code and Copilot remember "always allow" per tool or per server. Claude Code's permission rules match an MCP argument only in a `--disallowedTools` deny rule. Hooks in Claude Code, Codex, VS Code and Copilot CLI, and Gemini CLI policy rules, see arguments, but only where an operator writes one; client documentation and source, 2026-10-23.
Verdict: confirmed — a risky variant carried as an argument rides unseen on its tool's approval.
Held by: review

### E-056 A client can run a registered tool with nobody approving it

Supports: D-12
Source says: the [MCP tools specification](https://modelcontextprotocol.io/specification/2025-11-25/server/tools) has clients treat tool annotations as untrusted.
Observed: every widely used client examined but ChatGPT has such a mode or setting: Claude Code's bypassPermissions, allow rules and auto mode, claude.ai's Always allow, Codex `--yolo`, VS Code's Allow all, Copilot CLI `--yolo`, Gemini CLI YOLO and Cursor's Run Everything; client documentation, 2026-10-23.
Verdict: confirmed — only a marked tool is held, and only in Claude Code, so a destructive tool is not registered unless the operator turns it on.
Held by: TestADestructiveToolIsRegisteredOnlyWhenAllowed

### E-057 Claude Code holds a tool marked `anthropic/requiresUserInteraction` on every call

Supports: D-25
Source says: the MCP specification never names the key, and says "the protocol itself does not mandate any specific user interaction model". Anthropic documents it for "tools whose permission prompt is itself the point".
Observed: Claude Code 2.1.199 or later prompts on every call in every interactive mode, past allow rules and hooks, and from 2.1.246 offers no "don't ask again". It denies the call under dontAsk, `-p` without a permission host and `--permission-prompt-tool`; an Agent SDK host's `canUseTool` can approve it; 2026-10-23.
Verdict: confirmed — no other client reads it.
Held by: TestOnlyTheMarkedSetAsksForAPerson

### E-058 Claude Code shows the model only `structuredContent`

Supports: D-13
Source says: [anthropics/claude-code#55677](https://github.com/anthropics/claude-code/issues/55677), filed with a repro and closed as not planned.
Observed: asked through the client's own transcript, Claude Code put only the structured half in front of the model, as google-docs-mcp found too; Claude Code, 2026-09-06.
Verdict: confirmed — the rendering is invisible there, while claude.ai and ChatGPT show the text half, so both halves are sent.
Held by: evals

### E-059 A question through form elicitation is no consent control

Supports: D-15
Source says: the MCP specification gives per-call approval to the client, and lets it gather an elicitation's answer "from the user or other sources".
Observed: a Claude Code `Elicitation` hook answers with no dialog, and Codex in full access accepts an empty form by itself. User reports have Claude Desktop's Code tab and the VS Code extension decline it undrawn, and Cowork leave it to time out. A per-call question expired during a bulk delete; 2026-10-15 to 2026-10-23.
Verdict: refuted — the idea that the server should confirm a write with the person itself.
Held by: forbidigo

### E-060 A tool's output schema does not reach the model in the Messages API

Supports: D-06
Source says: a Messages API custom tool takes `name`, `description`, `input_schema` and `input_examples`, with no output-schema field. MCP says "Clients SHOULD validate structured results against this schema".
Observed: Anthropic's API documentation, read 2026-09-17.
Verdict: refuted — the assumption that output-schema bytes cost the model's context. They are protocol traffic and client-side validation; the model reads the tool description.
Held by: review

### E-061 The schema generator inlines a nested type wherever it appears

Supports: D-06
Source says: [google/jsonschema-go](https://github.com/google/jsonschema-go) models `$defs` and `$ref`, but `For[T]()` emits neither: it errors on a cycle and never de-duplicates.
Observed: read at v0.4.3, 2026-09-17; a quote carrying links and attachments cost 16.5 KB across the message tools.
Verdict: confirmed — a type used twice on one tool is serialized twice.
Held by: review

## Releasing

### E-062 cosign 3 needs `--bundle`

Supports: D-40
Source says: the cosign v3.0.1 release notes make `--bundle` required.
Observed: with cosign 3 the old `--output-signature` and `--output-certificate` produce nothing, and a sibling server's release failed with "create bundle file: open : no such file or directory"; recorded by 2026-09-06.
Verdict: confirmed — the release signs `checksums.txt` into one Sigstore bundle.
Held by: review

### E-063 Pinning an action does not pin what the action installs

Supports: D-40
Source says: nothing; each installer action takes its tool's version as an input of its own.
Observed: a sibling server's release workflow pinned `sigstore/cosign-installer` by SHA with no `cosign-release`, installed the newest cosign, whose signing format had changed, and published nothing; recorded by 2026-09-06.
Verdict: confirmed — a SHA pins the wrapper, not the tool, so `cosign-release` and `syft-version` are pinned beside the SHAs.
Held by: pins

### E-064 goreleaser's `mcp` block cannot publish an MCPB package

Supports: D-42
Source says: the MCP registry requires `fileSha256` for an MCPB package.
Observed: goreleaser's `mcp` block has exactly `registry_type`, `identifier` and `transport`, read out of its configuration at v2.18.0 and at v2.18.1; recorded by 2026-09-06.
Verdict: confirmed — the block has no field for the hash, so `scripts/gates server-json` writes the entry.
Held by: server-json

### E-065 The MCP registry enforces its MCPB rules in code, not in its schema

Supports: D-42
Source says: the registry's published `server.json` schema does not carry these rules.
Observed: the registry requires a hash, a github.com or gitlab.com release-asset URL carrying "mcp" and no `registryBaseUrl`, and it sends a HEAD to the URL before it accepts an entry; recorded by 2026-09-06.
Verdict: contradicted — the schema accepts entries the registry refuses, and an entry can be published only after the release exists.
Held by: server-json

### E-066 goreleaser checks for a dirty tree before its before hooks run

Supports: D-47
Source says: goreleaser v2.18.1 checks once, in `validate` in [internal/pipe/git/git.go](https://github.com/goreleaser/goreleaser/blob/v2.18.1/internal/pipe/git/git.go), and [internal/pipeline/pipeline.go](https://github.com/goreleaser/goreleaser/blob/v2.18.1/internal/pipeline/pipeline.go) runs that pipe before the before hooks.
Observed: the goreleaser source at v2.18.1, read 2026-11-02.
Verdict: refuted — the belief that goreleaser refuses a tree a before hook dirtied. Such a hook builds the tag from files the tag does not hold, and Go stamps the binary modified, so the hooks write only what git ignores.
Held by: TestReleaseHooksDoNotWriteIntoTheCheckout

### E-067 A commit footer's token is case-insensitive, except `BREAKING CHANGE`

Supports: D-07
Source says: [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/), item 15: units "MUST NOT be treated as case-sensitive by implementors, with the exception of BREAKING CHANGE which MUST be uppercase"; item 16: "BREAKING-CHANGE MUST be synonymous with BREAKING CHANGE".
Observed: the specification, read 2026-11-01.
Verdict: refuted — the belief that `SCHEMA-CHANGE:` must be written in capitals; `schema-ack` reads it in any case, and `BREAKING CHANGE:` and `BREAKING-CHANGE:` only in capitals.
Held by: schema-ack

### E-068 A commit footer's value may run over several lines

Supports: D-07
Source says: [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/), item 8: "Each footer MUST consist of a word token, followed by either a `:<space>` or `<space>#` separator"; item 10: "A footer's value MAY contain spaces and newlines, and parsing MUST terminate when the next valid footer token/separator pair is observed".
Observed: the specification, read 2026-11-02.
Verdict: refuted — the belief that a footer's value is one line. `schema-ack` reads a wrapped value; a `Token #value` footer counts toward the block but acknowledges nothing.
Held by: schema-ack

## Claude Desktop bundle

### E-069 A bundle install adds no approval behind the server's own

Supports: D-45
Source says: [anthropics/mcpb](https://github.com/anthropics/mcpb) at `70fe3b34` and v2.1.2 declare the manifest's `tools` list for display, and nothing reads it at run time. Claude's connector documentation has Desktop approve per tool by the annotations, and "Always allow" skips the prompt "for that tool from now on".
Observed: read 2026-10-24; Claude Desktop itself not probed.
Verdict: refuted — the belief that a bundle install has a second approval behind the server, as Claude Code's ask rules are.
Held by: mcpb-manifest

### E-070 A bundle that reaches external services must name a privacy policy

Supports: D-45
Source says: [MANIFEST.md](https://github.com/anthropics/mcpb/blob/70fe3b34/MANIFEST.md) makes `privacy_policies` "Required when the extension connects to external services (first- or third-party) that process user data".
Observed: neither the schema nor `mcpb validate` enforces it; the manifest names [Google's policy](https://policies.google.com/privacy), which answered 200 on 2026-10-24.
Verdict: refuted — the belief that `privacy_policies` is optional; the bundle gate refuses an empty list.
Held by: mcpb-manifest

### E-071 Only `${__dirname}` gives a bundle command a meaning the specification fixes

Supports: D-46
Source says: anthropics/mcpb v2.1.2 MANIFEST.md gives a binary the command `server/my-server` once, and says `${__dirname}` "is replaced with the absolute path to the extension's directory". It never says what a relative command is resolved against.
Observed: the reference host, `src/shared/config.ts` at v2.1.2, substitutes variables in `command`, resolves no path and spawns it as written; read 2026-10-24; Claude Desktop not probed.
Verdict: refuted — the belief that any command the specification shows runs from the bundle.
Held by: mcpb-manifest

### E-072 A platform override's `env` replaces `mcp_config.env`

Supports: D-46
Source says: the reference host, [src/shared/config.ts](https://github.com/anthropics/mcpb/blob/v2.1.2/src/shared/config.ts) at v2.1.2, does `result.env = platformConfig.env || result.env`.
Observed: the reference host's source, read 2026-10-24.
Verdict: refuted — the belief that an override's `env` is merged into the base. An override's `env`, an empty `{}` included, replaces it, so the settings would not reach that platform.
Held by: mcpb-manifest

### E-073 A platform override's `args` replace `mcp_config.args`, and `args` carry settings

Supports: D-46
Source says: the reference host at v2.1.2 does `result.args = platformConfig.args || result.args`, and substitutes `${user_config.x}` into `args` as into `env`.
Observed: the reference host's source, read 2026-10-24; Claude Desktop without the `args` key is not probed.
Verdict: refuted — the belief that an override's `args` are added to the base. An argument such as `--enable-destructive=true` reaches the server, and an override's `args`, an empty `[]` included, replaces the base.
Held by: mcpb-manifest

### E-074 A required setting's default reaches the server only as a value the person gave

Supports: D-45
Source says: the reference host at v2.1.2: `getMcpConfigForManifest` sets each key's `default` first, then `Object.assign(mergedConfig, userConfig)`; `hasRequiredConfigMissing` reads the person's values alone, and one missing skips the launch.
Observed: the reference host's source, read 2026-10-24; Claude Desktop's install form may fill the default in, and is not probed.
Verdict: refuted — the belief that a required key's default reaches the server unasked. The gate substitutes it anyway, holding the bundle to a person who accepts it unread.
Held by: mcpb-manifest

### E-075 An optional bundle setting with no default reaches the server unsubstituted

Supports: D-45
Source says: the reference host at v2.1.2 passes `${user_config.x}` through as written for an optional key the person left empty and the manifest gave no default.
Observed: the reference host's source, read 2026-10-24: a `""` default reaches the server as `""`, and a boolean as `"true"` or `"false"`.
Verdict: refuted — the belief that a setting left empty reaches the server empty. Every optional key has a default, and the gate builds the configuration from them.
Held by: mcpb-manifest

### E-076 A host may expand a variable inside a `user_config` default

Supports: D-45
Source says: anthropics/mcpb v2.1.2 MANIFEST.md says a default "supports variable substitution", and lists `${HOME}`, `${DESKTOP}` and `${DOCUMENTS}`. The reference host builds the system directories first and each key's value after, and replaces them in that order.
Observed: the specification and the reference host's source, read 2026-10-24; Claude Desktop not probed.
Verdict: refuted — the belief that a default reaches the server as written; the defaults check expands nothing, so the gate refuses a default holding `${`.
Held by: TestADefaultHoldingAReferenceIsRefused

## Code signing

### E-077 Smart App Control lets an unknown program run only when it is signed

Supports: D-47
Source says: Microsoft Learn, "Smart App Control": with no prediction it "will still allow an app to run if it is signed with a certificate issued by a certificate authority (CA) within the Trusted Root Program", and "unknown, unsigned code are blocked by default".
Observed: the page, read 2026-11-02.
Verdict: confirmed — every release is a new file with no reputation, so signing is what lets it run there.
Held by: review

### E-078 SignPath Foundation signs only a program whose version resource names the project and one version

Supports: D-47
Source says: [signpath.org/terms](https://signpath.org/terms), marked draft, sets every product name attribute to the project's name and every product version attribute to one value. SignPath's reference lists `product-name`, `product-version`, `file-version` and more for a `<pe-file>`, and does not say how a version is compared.
Observed: read 2026-11-02; telepresenceio/telepresence, a Go project SignPath Foundation signs, restricts `product-version` to the tag without its v.
Verdict: confirmed — the strings hold the release's version less the v, and the fixed part its three numbers and a 0; the first test-signing request settles it.
Held by: winres

### E-079 SignPath Foundation wants the code signing policy on the home page and every release page

Supports: D-47
Source says: [signpath.org/terms](https://signpath.org/terms), marked draft: a section or a link titled "Code signing policy", with the attribution, the team's roles and a privacy statement.
Observed: the terms, read 2026-11-02.
Verdict: confirmed — the README is the home page and holds the section, and the release notes' footer links to it.
Held by: review

### E-080 An ignored `.syso` leaves the build unmodified

Supports: D-47
Source says: Go stamps `vcs.modified` from `git status`, which leaves ignored files out.
Observed: a release rehearsal from a clean commit stamped `vcs.modified=false` with both `.syso` files in place, and a second rehearsal built byte-identical Windows programs; Go 1.27.3, 2026-11-02.
Verdict: confirmed — so the `.syso` files are gitignored, rather than committed or left untracked.
Held by: TestWinresWritesWhereGoreleaserBuilds

Retired: none
Next ID: E-082
