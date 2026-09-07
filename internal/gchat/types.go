package gchat

// Wire types for the Chat and People APIs.
//
// These mirror Google's JSON, not the tool surface. A field here is
// named as Google names it. The tool-facing shapes live in
// internal/tools and are deliberately separate: an upstream field
// addition must not leak into what a model sees, and a tool's
// presentation choices must not push back into the wire types.
//
// Every field is optional on the wire except the identifiers a response
// cannot be useful without. Those are declared without omitempty so a
// field Google removes or renames shows up as an empty required value
// rather than passing silently.

// Space is a Chat space, group chat or direct message.
type Space struct {
	Name          string         `json:"name"`
	Type          string         `json:"type,omitempty"` // deprecated by Google in favour of SpaceType
	SpaceType     string         `json:"spaceType,omitempty"`
	SingleUserBot *bool          `json:"singleUserBotDm,omitempty"`
	Threaded      bool           `json:"threaded,omitempty"`
	DisplayName   string         `json:"displayName,omitempty"`
	ExternalUser  *bool          `json:"externalUserAllowed,omitempty"`
	Threading     string         `json:"spaceThreadingState,omitempty"`
	Details       *SpaceDetails  `json:"spaceDetails,omitempty"`
	HistoryState  string         `json:"spaceHistoryState,omitempty"`
	ImportMode    bool           `json:"importMode,omitempty"`
	CreateTime    string         `json:"createTime,omitempty"`
	LastActive    string         `json:"lastActiveTime,omitempty"`
	AdminInstall  bool           `json:"adminInstalled,omitempty"`
	Membership    *MembershipCnt `json:"membershipCount,omitempty"`
	Access        *AccessSet     `json:"accessSettings,omitempty"`
	URI           string         `json:"spaceUri,omitempty"`
	Predefined    string         `json:"predefinedPermissionSettings,omitempty"`
	Permissions   *PermSettings  `json:"permissionSettings,omitempty"`
	CustomerID    string         `json:"customer,omitempty"`
}

// SpaceDetails is a space's description and guidelines.
type SpaceDetails struct {
	Description string `json:"description,omitempty"`
	Guidelines  string `json:"guidelines,omitempty"`
}

// MembershipCnt counts joined members by kind.
type MembershipCnt struct {
	JoinedDirectHumanUserCount int `json:"joinedDirectHumanUserCount,omitempty"`
	JoinedGroupCount           int `json:"joinedGroupCount,omitempty"`
}

// AccessSet says who can discover and join a space.
type AccessSet struct {
	AccessState string `json:"accessState,omitempty"`
	Audience    string `json:"audience,omitempty"`
	Permissions *struct {
		Discover *PermSetting `json:"discoverSpaceSetting,omitempty"`
		Join     *PermSetting `json:"joinSpaceSetting,omitempty"`
	} `json:"accessPermissionSettings,omitempty"`
}

// PermSettings is who may do what in a space.
type PermSettings struct {
	ManageMembersAndGroups *PermSetting `json:"manageMembersAndGroups,omitempty"`
	ModifySpaceDetails     *PermSetting `json:"modifySpaceDetails,omitempty"`
	ToggleHistory          *PermSetting `json:"toggleHistory,omitempty"`
	UseAtMentionAll        *PermSetting `json:"useAtMentionAll,omitempty"`
	ManageApps             *PermSetting `json:"manageApps,omitempty"`
	ManageWebhooks         *PermSetting `json:"manageWebhooks,omitempty"`
	PostMessages           *PermSetting `json:"postMessages,omitempty"`
	ReplyMessages          *PermSetting `json:"replyMessages,omitempty"`
}

// PermSetting is one permission's audience.
type PermSetting struct {
	ManagersAllowed bool `json:"managersAllowed,omitempty"`
	MembersAllowed  bool `json:"membersAllowed,omitempty"`
}

// ListSpacesResponse is spaces.list.
type ListSpacesResponse struct {
	Spaces        []Space `json:"spaces,omitempty"`
	NextPageToken string  `json:"nextPageToken,omitempty"`
}

// SearchSpacesResponse is spaces.search.
//
// The rows arrive in Results. Spaces is the field Google deprecated in
// favour of it and still sends, so it is modelled to keep it out of the
// drift log and read only when Results is empty.
//
// NextPageToken and TotalSize come back only under admin access. A
// user-authenticated search returns one page and no count, which is why
// search_spaces says so rather than offering a page token that is
// always empty.
type SearchSpacesResponse struct {
	Results       []SearchSpaceResult `json:"results,omitempty"`
	Spaces        []Space             `json:"spaces,omitempty"`
	NextPageToken string              `json:"nextPageToken,omitempty"`
	TotalSize     int                 `json:"totalSize,omitempty"`
}

// SearchSpaceResult is one row of a search.
type SearchSpaceResult struct {
	Space *Space `json:"space,omitempty"`
}

// Found returns the spaces a search matched, from whichever field
// carried them.
func (r *SearchSpacesResponse) Found() []Space {
	if len(r.Results) == 0 {
		return r.Spaces
	}
	out := make([]Space, 0, len(r.Results))
	for _, row := range r.Results {
		if row.Space != nil {
			out = append(out, *row.Space)
		}
	}
	return out
}

// FindGroupChatsResponse is spaces.findGroupChats.
type FindGroupChatsResponse struct {
	Spaces        []Space `json:"spaces,omitempty"`
	NextPageToken string  `json:"nextPageToken,omitempty"`
}

// User is a Chat user reference.
type User struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	DomainID    string `json:"domainId,omitempty"`
	Type        string `json:"type,omitempty"`
	IsAnonymous bool   `json:"isAnonymous,omitempty"`
}

// Membership is a person's or group's place in a space.
type Membership struct {
	Name         string `json:"name"`
	State        string `json:"state,omitempty"`
	Role         string `json:"role,omitempty"`
	Member       *User  `json:"member,omitempty"`
	GroupMember  *Group `json:"groupMember,omitempty"`
	CreateTime   string `json:"createTime,omitempty"`
	DeleteTime   string `json:"deleteTime,omitempty"`
	MembershipID string `json:"membershipId,omitempty"`
	// Affiliation is how the person relates to the organisation:
	// INTERNAL, EXTERNAL or MANAGED_EXTERNAL, output only. It is the
	// field a live doctor run reported as drift twice before anything
	// modelled it, and it is worth reading — it says whether someone in
	// a space is a colleague or a guest.
	Affiliation string `json:"affiliation,omitempty"`
}

// Group is a Google Group in a space.
type Group struct {
	Name string `json:"name"`
}

// ListMembershipsResponse is spaces.members.list.
type ListMembershipsResponse struct {
	Memberships   []Membership `json:"memberships,omitempty"`
	NextPageToken string       `json:"nextPageToken,omitempty"`
}

// Thread is a message thread.
type Thread struct {
	Name      string `json:"name,omitempty"`
	ThreadKey string `json:"threadKey,omitempty"`
}

// Message is a Chat message.
type Message struct {
	Name             string             `json:"name"`
	Sender           *User              `json:"sender,omitempty"`
	CreateTime       string             `json:"createTime,omitempty"`
	LastUpdateTime   string             `json:"lastUpdateTime,omitempty"`
	DeleteTime       string             `json:"deleteTime,omitempty"`
	Text             string             `json:"text,omitempty"`
	FormattedText    string             `json:"formattedText,omitempty"`
	Thread           *Thread            `json:"thread,omitempty"`
	Space            *Space             `json:"space,omitempty"`
	Fallback         string             `json:"fallbackText,omitempty"`
	ArgumentText     string             `json:"argumentText,omitempty"`
	Attachments      []Attachment       `json:"attachment,omitempty"`
	Annotations      []Annotation       `json:"annotations,omitempty"`
	ThreadReply      bool               `json:"threadReply,omitempty"`
	ClientAssignedID string             `json:"clientAssignedMessageId,omitempty"`
	EmojiReactions   []ReactionSummary  `json:"emojiReactionSummaries,omitempty"`
	DeletionMetadata *DeletionMetadata  `json:"deletionMetadata,omitempty"`
	QuotedMessage    *QuotedMessageMeta `json:"quotedMessageMetadata,omitempty"`
	AttachedGifs     []AttachedGif      `json:"attachedGifs,omitempty"`
	AccessoryWidgets []any              `json:"accessoryWidgets,omitempty"`
	MarkupSyntax     string             `json:"markupSyntax,omitempty"`
	PrivateMessageTo *User              `json:"privateMessageViewer,omitempty"`
}

// DeletionMetadata says who deleted a message.
type DeletionMetadata struct {
	DeletionType string `json:"deletionType,omitempty"`
}

// QuotedMessageMeta is a quoted or forwarded message reference.
type QuotedMessageMeta struct {
	Name       string `json:"name,omitempty"`
	LastUpdate string `json:"lastUpdateTime,omitempty"`
	QuoteType  string `json:"quoteType,omitempty"`
	// Snapshot is what the quoted message said at the time it was
	// quoted. Google added it after this server shipped, and the drift
	// report on a live account is how it was noticed: a reply was read
	// without the thing it was replying to, which is most of what a
	// quote is for.
	Snapshot *QuotedMessageSnapshot `json:"quotedMessageSnapshot,omitempty"`
	// Forwarded names the space a forwarded message came from, and is
	// populated only for the FORWARD quote type.
	Forwarded *ForwardedMeta `json:"forwardedMetadata,omitempty"`
}

// QuotedMessageSnapshot is the quoted message's content, frozen at the
// moment it was quoted.
//
// Sender is a resource name rather than a User: Google documents it as
// the author's name, "users/{user}", not the object the parent message
// carries.
type QuotedMessageSnapshot struct {
	Sender        string       `json:"sender,omitempty"`
	Text          string       `json:"text,omitempty"`
	FormattedText string       `json:"formattedText,omitempty"`
	Annotations   []Annotation `json:"annotations,omitempty"`
	Attachments   []Attachment `json:"attachment,omitempty"`
}

// ForwardedMeta is where a forwarded message came from.
type ForwardedMeta struct {
	SpaceName string `json:"spaceName,omitempty"`
}

// AttachedGif is a GIF in a message.
type AttachedGif struct {
	URI string `json:"uri,omitempty"`
}

// Attachment is a file on a message.
type Attachment struct {
	Name              string             `json:"name,omitempty"`
	ContentName       string             `json:"contentName,omitempty"`
	ContentType       string             `json:"contentType,omitempty"`
	Source            string             `json:"source,omitempty"`
	Thumbnail         string             `json:"thumbnailUri,omitempty"`
	Download          string             `json:"downloadUri,omitempty"`
	AttachmentDataRef *AttachmentDataRef `json:"attachmentDataRef,omitempty"`
	DriveDataRef      *DriveDataRef      `json:"driveDataRef,omitempty"`
}

// AttachmentDataRef points at an uploaded file.
type AttachmentDataRef struct {
	ResourceName          string `json:"resourceName,omitempty"`
	AttachmentUploadToken string `json:"attachmentUploadToken,omitempty"`
}

// DriveDataRef points at a Drive file.
type DriveDataRef struct {
	DriveFileID string `json:"driveFileId,omitempty"`
}

// Annotation marks a span of message text, such as a mention.
type Annotation struct {
	Type            string           `json:"type,omitempty"`
	StartIndex      int              `json:"startIndex,omitempty"`
	Length          int              `json:"length,omitempty"`
	UserMention     *UserMention     `json:"userMention,omitempty"`
	SlashCommand    any              `json:"slashCommand,omitempty"`
	RichLinkMeta    any              `json:"richLinkMetadata,omitempty"`
	CustomEmojiMeta *CustomEmojiMeta `json:"customEmojiMetadata,omitempty"`
}

// UserMention is an @mention.
type UserMention struct {
	User *User  `json:"user,omitempty"`
	Type string `json:"type,omitempty"`
}

// CustomEmojiMeta is a custom emoji inside message text.
type CustomEmojiMeta struct {
	CustomEmoji *CustomEmoji `json:"customEmoji,omitempty"`
}

// ListMessagesResponse is spaces.messages.list.
type ListMessagesResponse struct {
	Messages      []Message `json:"messages,omitempty"`
	NextPageToken string    `json:"nextPageToken,omitempty"`
}

// SearchMessagesResponse is spaces.messages.search.
type SearchMessagesResponse struct {
	Results       []SearchMessageResult `json:"results,omitempty"`
	NextPageToken string                `json:"nextPageToken,omitempty"`
}

// SearchMessageResult is one hit, with the read and mute state Google
// attaches under the full view when the caller granted the scopes for
// them.
//
// Read is a pointer because false is the interesting value and Google
// omits the field entirely under the basic view: flattening it would
// report every message as unread. SpaceMuteSetting is Google's enum,
// not a boolean.
type SearchMessageResult struct {
	Message          *Message `json:"message,omitempty"`
	Read             *bool    `json:"read,omitempty"`
	SpaceMuteSetting string   `json:"spaceMuteSetting,omitempty"`
}

// Emoji is a unicode or custom emoji.
type Emoji struct {
	Unicode     string       `json:"unicode,omitempty"`
	CustomEmoji *CustomEmoji `json:"customEmoji,omitempty"`
}

// CustomEmoji is an organisation's own emoji.
type CustomEmoji struct {
	Name         string        `json:"name,omitempty"`
	UID          string        `json:"uid,omitempty"`
	EmojiName    string        `json:"emojiName,omitempty"`
	TemporaryURI string        `json:"temporaryImageUri,omitempty"`
	Payload      *EmojiPayload `json:"payload,omitempty"`
}

// EmojiPayload is the image bytes when creating a custom emoji.
type EmojiPayload struct {
	FileContent string `json:"fileContent,omitempty"`
	Filename    string `json:"filename,omitempty"`
}

// ListCustomEmojisResponse is customEmojis.list.
type ListCustomEmojisResponse struct {
	CustomEmojis  []CustomEmoji `json:"customEmojis,omitempty"`
	NextPageToken string        `json:"nextPageToken,omitempty"`
}

// Reaction is one person's reaction to a message.
type Reaction struct {
	Name  string `json:"name,omitempty"`
	User  *User  `json:"user,omitempty"`
	Emoji *Emoji `json:"emoji,omitempty"`
}

// ReactionSummary counts one emoji's reactions on a message.
type ReactionSummary struct {
	Emoji         *Emoji `json:"emoji,omitempty"`
	ReactionCount int    `json:"reactionCount,omitempty"`
}

// ListReactionsResponse is spaces.messages.reactions.list.
type ListReactionsResponse struct {
	Reactions     []Reaction `json:"reactions,omitempty"`
	NextPageToken string     `json:"nextPageToken,omitempty"`
}

// MessagePin is a pinned message in a space.
type MessagePin struct {
	Name       string `json:"name,omitempty"`
	Message    string `json:"message,omitempty"`
	CreateTime string `json:"createTime,omitempty"`
	Creator    *User  `json:"creator,omitempty"`
}

// ListMessagePinsResponse is spaces.messagePins.list.
type ListMessagePinsResponse struct {
	MessagePins   []MessagePin `json:"messagePins,omitempty"`
	NextPageToken string       `json:"nextPageToken,omitempty"`
}

// Section is one group in a person's Chat sidebar.
type Section struct {
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Type        string `json:"type,omitempty"`
	// SortOrder is a pointer because zero is a real rank: a section
	// Google has not ranked and the first one in the sidebar must not
	// arrive as the same value.
	SortOrder  *int   `json:"sortOrder,omitempty"`
	SystemType string `json:"systemSectionType,omitempty"`
}

// ListSectionsResponse is users.sections.list.
type ListSectionsResponse struct {
	Sections      []Section `json:"sections,omitempty"`
	NextPageToken string    `json:"nextPageToken,omitempty"`
}

// SectionItem is a space filed under a sidebar section.
type SectionItem struct {
	Name      string `json:"name,omitempty"`
	Space     string `json:"space,omitempty"`
	SortOrder *int   `json:"sortOrder,omitempty"`
}

// ListSectionItemsResponse is users.sections.items.list.
type ListSectionItemsResponse struct {
	SectionItems  []SectionItem `json:"sectionItems,omitempty"`
	NextPageToken string        `json:"nextPageToken,omitempty"`
}

// SpaceReadState is how far a person has read in a space.
type SpaceReadState struct {
	Name         string `json:"name,omitempty"`
	LastReadTime string `json:"lastReadTime,omitempty"`
}

// ThreadReadState is how far a person has read in a thread.
type ThreadReadState struct {
	Name         string `json:"name,omitempty"`
	LastReadTime string `json:"lastReadTime,omitempty"`
}

// SpaceNotificationSetting is a person's notification choice for a space.
type SpaceNotificationSetting struct {
	Name                string `json:"name,omitempty"`
	NotificationSetting string `json:"notificationSetting,omitempty"`
	MuteSetting         string `json:"muteSetting,omitempty"`
}

// Availability is a person's Chat presence and custom status.
type Availability struct {
	Name         string        `json:"name,omitempty"`
	State        string        `json:"state,omitempty"`
	CustomStatus *CustomStatus `json:"customStatus,omitempty"`
	DoNotDisturb *DNDMetadata  `json:"doNotDisturbMetadata,omitempty"`
}

// CustomStatus is the text and emoji beside a person's name.
//
// The text field is "text", not "statusText", and the expiry is
// "expireTime" — where do-not-disturb's is "expirationTime". Both were
// written the other way round here before the reference was read, which
// would have shown every custom status as empty and logged the real
// fields as drift.
type CustomStatus struct {
	Text       string `json:"text,omitempty"`
	Emoji      *Emoji `json:"emoji,omitempty"`
	ExpireTime string `json:"expireTime,omitempty"`
	TTL        string `json:"ttl,omitempty"`
}

// DNDMetadata says when do-not-disturb ends. Output only: the request
// that sets it carries its own expiry.
type DNDMetadata struct {
	ExpirationTime string `json:"expirationTime,omitempty"`
}

// DNDRequest is the body of users.availability.markAsDoNotDisturb.
// Exactly one of the two: Google models them as a union and requires
// one, at most a year out.
type DNDRequest struct {
	ExpireTime string `json:"expireTime,omitempty"`
	TTL        string `json:"ttl,omitempty"`
}

// UserInfo is the OpenID Connect userinfo response, which is how the
// server learns who the stored token belongs to.
type UserInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
	Name          string `json:"name,omitempty"`
	GivenName     string `json:"given_name,omitempty"`
	FamilyName    string `json:"family_name,omitempty"`
	Picture       string `json:"picture,omitempty"`
	Locale        string `json:"locale,omitempty"`
	HostedDomain  string `json:"hd,omitempty"`
}

// Person is a People API person, trimmed to what this server reads.
type Person struct {
	ResourceName  string        `json:"resourceName,omitempty"`
	Names         []PersonName  `json:"names,omitempty"`
	EmailAddrs    []PersonEmail `json:"emailAddresses,omitempty"`
	Photos        []PersonPhoto `json:"photos,omitempty"`
	Organizations []PersonOrg   `json:"organizations,omitempty"`
}

// PersonName is one of a person's names.
type PersonName struct {
	DisplayName string         `json:"displayName,omitempty"`
	GivenName   string         `json:"givenName,omitempty"`
	FamilyName  string         `json:"familyName,omitempty"`
	Metadata    *FieldMetadata `json:"metadata,omitempty"`
}

// PersonEmail is one of a person's addresses.
type PersonEmail struct {
	Value    string         `json:"value,omitempty"`
	Type     string         `json:"type,omitempty"`
	Metadata *FieldMetadata `json:"metadata,omitempty"`
}

// PersonPhoto is a profile picture.
type PersonPhoto struct {
	URL      string         `json:"url,omitempty"`
	Metadata *FieldMetadata `json:"metadata,omitempty"`
}

// PersonOrg is a person's job title and department.
type PersonOrg struct {
	Title      string         `json:"title,omitempty"`
	Department string         `json:"department,omitempty"`
	Name       string         `json:"name,omitempty"`
	Metadata   *FieldMetadata `json:"metadata,omitempty"`
}

// FieldMetadata marks which of several values is the primary one.
type FieldMetadata struct {
	Primary bool `json:"primary,omitempty"`
	Source  *struct {
		Type string `json:"type,omitempty"`
		ID   string `json:"id,omitempty"`
	} `json:"source,omitempty"`
}

// BatchGetPeopleResponse is people.getBatchGet. Each entry carries its
// own status, so one person the caller cannot see does not fail the
// rest of the batch.
type BatchGetPeopleResponse struct {
	Responses []PersonResponse `json:"responses,omitempty"`
}

// PersonResponse is one entry of a batch lookup.
type PersonResponse struct {
	RequestedResourceName string  `json:"requestedResourceName,omitempty"`
	Person                *Person `json:"person,omitempty"`
	Status                *struct {
		Code    int    `json:"code,omitempty"`
		Message string `json:"message,omitempty"`
	} `json:"status,omitempty"`
}

// SearchDirectoryPeopleResponse is people.searchDirectoryPeople.
type SearchDirectoryPeopleResponse struct {
	People        []Person `json:"people,omitempty"`
	NextPageToken string   `json:"nextPageToken,omitempty"`
	TotalSize     int      `json:"totalSize,omitempty"`
}

// SearchContactsResponse is people.searchContacts.
type SearchContactsResponse struct {
	Results []struct {
		Person *Person `json:"person,omitempty"`
	} `json:"results,omitempty"`
}

// SpaceEvent is one change in a space: a message posted, a member
// added, a reaction removed, the space itself edited.
//
// The payload is a union — exactly one of the fields below is set, and
// which one is what eventType says. Google returns a batch form of
// every type without being asked for it, so both forms are modelled;
// an unmodelled one would report drift on every listing.
type SpaceEvent struct {
	Name      string `json:"name,omitempty"`
	EventTime string `json:"eventTime,omitempty"`
	EventType string `json:"eventType,omitempty"`

	MessageCreated      *MessageEventData      `json:"messageCreatedEventData,omitempty"`
	MessageUpdated      *MessageEventData      `json:"messageUpdatedEventData,omitempty"`
	MessageDeleted      *MessageEventData      `json:"messageDeletedEventData,omitempty"`
	MessageBatchCreated *MessageBatchEventData `json:"messageBatchCreatedEventData,omitempty"`
	MessageBatchUpdated *MessageBatchEventData `json:"messageBatchUpdatedEventData,omitempty"`
	MessageBatchDeleted *MessageBatchEventData `json:"messageBatchDeletedEventData,omitempty"`

	MembershipCreated      *MembershipEventData      `json:"membershipCreatedEventData,omitempty"`
	MembershipUpdated      *MembershipEventData      `json:"membershipUpdatedEventData,omitempty"`
	MembershipDeleted      *MembershipEventData      `json:"membershipDeletedEventData,omitempty"`
	MembershipBatchCreated *MembershipBatchEventData `json:"membershipBatchCreatedEventData,omitempty"`
	MembershipBatchUpdated *MembershipBatchEventData `json:"membershipBatchUpdatedEventData,omitempty"`
	MembershipBatchDeleted *MembershipBatchEventData `json:"membershipBatchDeletedEventData,omitempty"`

	ReactionCreated      *ReactionEventData      `json:"reactionCreatedEventData,omitempty"`
	ReactionDeleted      *ReactionEventData      `json:"reactionDeletedEventData,omitempty"`
	ReactionBatchCreated *ReactionBatchEventData `json:"reactionBatchCreatedEventData,omitempty"`
	ReactionBatchDeleted *ReactionBatchEventData `json:"reactionBatchDeletedEventData,omitempty"`

	SpaceUpdated      *SpaceEventData      `json:"spaceUpdatedEventData,omitempty"`
	SpaceBatchUpdated *SpaceBatchEventData `json:"spaceBatchUpdatedEventData,omitempty"`
}

// MessageEventData is the message an event is about. A deleted
// message's is a tombstone: the name survives and the content does not.
type MessageEventData struct {
	Message *Message `json:"message,omitempty"`
}

// MessageBatchEventData is several message events of one type.
type MessageBatchEventData struct {
	Messages []MessageEventData `json:"messages,omitempty"`
}

// MembershipEventData is the membership an event is about.
type MembershipEventData struct {
	Membership *Membership `json:"membership,omitempty"`
}

// MembershipBatchEventData is several membership events of one type.
type MembershipBatchEventData struct {
	Memberships []MembershipEventData `json:"memberships,omitempty"`
}

// ReactionEventData is the reaction an event is about.
type ReactionEventData struct {
	Reaction *Reaction `json:"reaction,omitempty"`
}

// ReactionBatchEventData is several reaction events of one type.
type ReactionBatchEventData struct {
	Reactions []ReactionEventData `json:"reactions,omitempty"`
}

// SpaceEventData is the space an event is about.
type SpaceEventData struct {
	Space *Space `json:"space,omitempty"`
}

// SpaceBatchEventData is several space events of one type.
type SpaceBatchEventData struct {
	Spaces []SpaceEventData `json:"spaces,omitempty"`
}

// ListSpaceEventsResponse is one page of a space's events, oldest
// first.
type ListSpaceEventsResponse struct {
	SpaceEvents   []SpaceEvent `json:"spaceEvents,omitempty"`
	NextPageToken string       `json:"nextPageToken,omitempty"`
}
