package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmedum/google-chat-mcp/internal/service"
)

// DownloadAttachmentInput names one attachment on one message.
type DownloadAttachmentInput struct {
	MessageName    string `json:"message_name" jsonschema:"the message the file is on, spaces/{space}/messages/{message}"`
	AttachmentName string `json:"attachment_name,omitempty" jsonschema:"which attachment, when the message carries more than one. get_message lists them as attachment_name"`
}

// DownloadAttachmentOutput is the file that landed on disk.
type DownloadAttachmentOutput struct {
	Path        string `json:"path" jsonschema:"where the file was written, inside the server's local directory"`
	FileName    string `json:"file_name" jsonschema:"the attachment's original name in Chat, which may differ from the name on disk"`
	Bytes       int64  `json:"bytes" jsonschema:"how many bytes were written"`
	ContentType string `json:"content_type" jsonschema:"the MIME type Google served it as"`
	SHA256      string `json:"sha256" jsonschema:"the SHA-256 of the bytes written, hex encoded. Chat publishes no checksum of its own, so there is nothing here to verify against: this is for comparing the file with a copy elsewhere"`
}

// Render is where the file landed.
func (o DownloadAttachmentOutput) Render() string {
	return block(
		meta("saved "+o.FileName, o.Path),
		meta(bytesOf(o.Bytes), o.ContentType, labelled("sha256", o.SHA256)),
	)
}

func registerAttachments(s *mcp.Server, d Deps) {
	register(s, d, spec{
		Name: "download_attachment",
		Description: "Save a message's attachment to the server's local directory. Name the message; if it " +
			"carries more than one file, pass attachment_name from get_message. The result says where the file " +
			"landed. A Google Drive attachment is refused with its file id: those bytes belong to Drive, not " +
			"Chat. This needs the server to have been started with GCM_LOCAL_DIR.",
		Kind: ReadWritesLocally,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DownloadAttachmentInput) (*mcp.CallToolResult, DownloadAttachmentOutput, error) {
		got, err := d.Service.DownloadAttachment(ctx, service.DownloadAttachmentInput{
			Message: in.MessageName, Attachment: in.AttachmentName,
		})
		if err != nil {
			return nil, DownloadAttachmentOutput{}, err
		}
		return nil, DownloadAttachmentOutput{
			Path:        got.Path,
			FileName:    got.ContentName,
			Bytes:       got.Bytes,
			ContentType: got.ContentType,
			SHA256:      got.SHA256,
		}, nil
	})

	// Write, so a person is asked first: this reads a file off the
	// machine and hands it to Google. Nobody in the space sees it until
	// send_message carries the token.
	register(s, d, spec{
		Name: "upload_attachment",
		Description: "Upload a local file to a Chat space and get back the token that attaches it. Post it with " +
			"send_message's attachment_upload_token, in the same space; nobody sees the file until then. The file " +
			"has to be inside the server's local directory, and Google's limit is 200 MB. Set dry_run to check the " +
			"file and see what would be sent. This needs the server to have been started with GCM_LOCAL_DIR.",
		Kind: Write,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in UploadAttachmentInput) (*mcp.CallToolResult, UploadAttachmentOutput, error) {
		got, err := d.Service.UploadAttachment(ctx, service.UploadAttachmentInput{
			Space: in.SpaceID, Path: in.LocalPath, FileName: in.FileName, DryRun: in.DryRun,
		})
		if err != nil {
			return nil, UploadAttachmentOutput{}, err
		}
		return nil, UploadAttachmentOutput{
			UploadToken:     nullable(got.UploadToken),
			FileName:        got.FileName,
			Bytes:           got.Bytes,
			ContentType:     got.ContentType,
			DryRun:          got.DryRun,
			RenderedPayload: rendered(got.Rendered),
		}, nil
	})
}

// UploadAttachmentInput is one local file to put in a space.
type UploadAttachmentInput struct {
	SpaceID   string `json:"space_id" jsonschema:"the space the file is for, spaces/{id}. The token only works for this space"`
	LocalPath string `json:"local_path" jsonschema:"the file to send, inside the server's local directory. A bare name is taken as being in that directory; a path outside it is refused"`
	FileName  string `json:"file_name,omitempty" jsonschema:"the name the file gets in Chat, with its extension. Omit to keep the name it has on disk"`
	DryRun    bool   `json:"dry_run,omitempty" jsonschema:"report the file and the request metadata without uploading; call again without it to upload"`
}

// UploadAttachmentOutput is the reference send_message attaches with.
type UploadAttachmentOutput struct {
	UploadToken     *string          `json:"upload_token" jsonschema:"pass this to send_message as attachment_upload_token. Null after a dry run, because nothing was uploaded"`
	FileName        string           `json:"file_name" jsonschema:"the name the file has in Chat"`
	Bytes           int64            `json:"bytes" jsonschema:"the file's size on disk"`
	ContentType     string           `json:"content_type" jsonschema:"the MIME type this server derived from the extension, or empty when it could not"`
	DryRun          bool             `json:"dry_run" jsonschema:"true when nothing was uploaded"`
	RenderedPayload *RenderedPayload `json:"rendered_payload" jsonschema:"on a dry run, the metadata that would go with the file; null on a real upload"`
}

// Render says what was sent and what to do with the token.
func (o UploadAttachmentOutput) Render() string {
	if o.DryRun {
		return previewBody("upload " + meta(o.FileName, bytesOf(o.Bytes), o.ContentType))
	}
	return block(
		meta("uploaded "+o.FileName, bytesOf(o.Bytes), o.ContentType),
		"attach it with send_message, attachment_upload_token: "+deref(o.UploadToken),
	)
}
