package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/mmedum/google-chat-mcp/internal/gchat"
)

// DownloadAttachmentInput names one attachment on one message.
type DownloadAttachmentInput struct {
	Message string
	// Attachment picks one when the message carries several. It is the
	// attachment's resource name, which the message read reports.
	Attachment string
}

// DownloadAttachmentResult is the file that landed.
type DownloadAttachmentResult struct {
	Path        string
	Bytes       int64
	ContentType string
	ContentName string
	// SHA256 is of the bytes written, hex encoded. Chat publishes no
	// checksum of its own, so there is nothing to compare it against
	// here; it is reported so the file can be compared with a copy
	// somewhere else.
	SHA256 string
}

// DownloadAttachment writes one of a message's attachments into
// GCM_LOCAL_DIR.
func (s *Service) DownloadAttachment(ctx context.Context, in DownloadAttachmentInput) (*DownloadAttachmentResult, error) {
	files, err := s.files()
	if err != nil {
		return nil, err
	}
	name, err := requireMessage(in.Message)
	if err != nil {
		return nil, err
	}

	msg, err := s.client.GetMessage(ctx, name)
	if err != nil {
		return nil, Classify(err)
	}
	att, err := pickAttachment(msg.Attachments, in.Attachment)
	if err != nil {
		return nil, err
	}

	// The file is made before the transfer starts. Everything it needs
	// is known by now, and a name collision or an unwritable directory
	// found afterwards would abandon a download already part way down
	// the wire.
	file, path, err := files.Create(attachmentFileName(att))
	if err != nil {
		return nil, err
	}

	media, err := s.client.DownloadMedia(ctx, att.AttachmentDataRef.ResourceName)
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, Classify(err)
	}
	defer func() { _ = media.Body.Close() }()

	written, sum, err := writeStream(file, path, media.Body)
	if err != nil {
		return nil, err
	}
	// Google states the length; a file that does not match it is short,
	// whatever the transfer thought. Keeping it would leave something on
	// disk that looks like a download that worked.
	if media.Length >= 0 && written != media.Length {
		_ = os.Remove(path)
		return nil, Failf(ClassUpstream, "The download stopped early: Google said %d bytes and %d arrived. "+
			"Nothing was kept. Try again.", media.Length, written)
	}

	return &DownloadAttachmentResult{
		Path:  path,
		Bytes: written,
		// The attachment's own type first, and the transfer's only as a
		// fallback. Google's media endpoint serves every download as
		// application/octet-stream — checked live 2026-09-06 on a file
		// whose attachment metadata said text/plain — so preferring
		// what the transfer said threw the better answer away.
		ContentType: cmp.Or(att.ContentType, media.ContentType),
		ContentName: att.ContentName,
		SHA256:      sum,
	}, nil
}

// pickAttachment chooses which of a message's attachments to fetch, and
// says what to pass when the message alone does not decide it.
func pickAttachment(all []gchat.Attachment, want string) (*gchat.Attachment, error) {
	if len(all) == 0 {
		return nil, Failf(ClassNotFound, "That message carries no attachment.")
	}
	want = strings.TrimSpace(want)
	var chosen *gchat.Attachment
	switch {
	case want != "":
		for i := range all {
			if all[i].Name == want {
				chosen = &all[i]
				break
			}
		}
		if chosen == nil {
			return nil, Failf(ClassNotFound, "That message has no attachment named %s. It has: %s.",
				want, strings.Join(attachmentNames(all), ", "))
		}
	case len(all) == 1:
		chosen = &all[0]
	default:
		return nil, Invalidf("That message carries %d attachments. Pass attachment_name as one of: %s.",
			len(all), strings.Join(attachmentNames(all), ", "))
	}

	if chosen.AttachmentDataRef == nil || chosen.AttachmentDataRef.ResourceName == "" {
		if chosen.DriveDataRef != nil && chosen.DriveDataRef.DriveFileID != "" {
			return nil, Failf(ClassUnsupported, "%s is a Google Drive file, not a file uploaded to Chat. "+
				"Chat cannot serve its bytes; open it in Drive as file id %s.",
				attachmentFileName(chosen), chosen.DriveDataRef.DriveFileID)
		}
		return nil, Failf(ClassUnsupported, "Google gave no downloadable reference for %s.",
			attachmentFileName(chosen))
	}
	return chosen, nil
}

// attachmentNames lists what a caller may pass as attachment_name.
func attachmentNames(all []gchat.Attachment) []string {
	out := make([]string, 0, len(all))
	for _, a := range all {
		out = append(out, a.Name)
	}
	return out
}

// attachmentFileName is what to call an attachment on disk and in a
// message to the caller.
func attachmentFileName(a *gchat.Attachment) string {
	return cmp.Or(a.ContentName, "attachment")
}

// downloadBuffer is the block a download is copied in. io.Copy's own
// 32 KiB default costs eight times as many reads, writes and stall-timer
// resets on a large file, and a transfer is where that adds up.
const downloadBuffer = 256 << 10

// writeStream copies a download into a file localFiles already created,
// digesting it as it goes.
//
// A failed copy takes the part-written file with it: half a file is
// worse than none, because it looks like a download that worked.
func writeStream(file *os.File, path string, body io.Reader) (int64, string, error) {
	sum := sha256.New()
	written, copyErr := io.CopyBuffer(io.MultiWriter(file, sum), body, make([]byte, downloadBuffer))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return 0, "", Failf(ClassUpstream, "The download failed partway through: %s. Nothing was kept.", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return 0, "", Invalidf("Cannot finish writing %s: %s", path, closeErr)
	}
	return written, hex.EncodeToString(sum.Sum(nil)), nil
}

// UploadAttachmentInput is one local file to put in a space.
type UploadAttachmentInput struct {
	Space string
	// Path is a file inside GCM_LOCAL_DIR, absolute or relative to it.
	Path string
	// FileName overrides the name the file is given in Chat. Empty
	// keeps the name it has on disk.
	FileName string
	DryRun   bool
}

// UploadAttachmentResult is the reference send_message takes.
type UploadAttachmentResult struct {
	// UploadToken is empty after a dry run: nothing was uploaded, so
	// there is nothing to attach.
	UploadToken string
	FileName    string
	Bytes       int64
	ContentType string
	DryRun      bool
	Rendered    map[string]any
}

// UploadAttachment sends a local file to a space and returns the token
// send_message attaches it with.
//
// The upload is not the post. Nobody sees the file until a message
// carries the token, and the token is good for one message.
func (s *Service) UploadAttachment(ctx context.Context, in UploadAttachmentInput) (*UploadAttachmentResult, error) {
	space, err := requireSpace(in.Space)
	if err != nil {
		return nil, err
	}
	files, err := s.files()
	if err != nil {
		return nil, err
	}
	file, info, err := files.Open("local_path", in.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	// Measured before a byte is read: a rejection after 200 MB have
	// gone up the wire is a slow way to learn a file is too big.
	if info.Size() > gchat.MaxAttachmentBytes {
		return nil, Invalidf("%s is %d bytes; Google's limit for an attachment is %d.",
			filepath.Base(file.Name()), info.Size(), gchat.MaxAttachmentBytes)
	}

	filename := safeName(cmp.Or(strings.TrimSpace(in.FileName), filepath.Base(file.Name())))
	contentType := mime.TypeByExtension(filepath.Ext(filename))

	out := &UploadAttachmentResult{
		FileName:    filename,
		Bytes:       info.Size(),
		ContentType: contentType,
		DryRun:      in.DryRun,
	}
	if in.DryRun {
		// The metadata part is the whole request body Google reads; the
		// file itself follows it on the wire.
		if out.Rendered, err = renderBody(gchat.UploadAttachmentRequest{Filename: filename}); err != nil {
			return nil, err
		}
		return out, nil
	}

	ref, err := s.client.UploadAttachment(ctx, space, filename, contentType, file, info.Size())
	if err != nil {
		return nil, Classify(err)
	}
	out.UploadToken = ref.AttachmentUploadToken
	if out.UploadToken == "" {
		return nil, Failf(ClassUpstream, "Google accepted the file but returned no upload token, "+
			"so there is nothing to attach it with.")
	}
	return out, nil
}
