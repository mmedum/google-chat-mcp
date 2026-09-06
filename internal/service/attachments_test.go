package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmedum/google-chat-mcp/internal/config"
	"github.com/mmedum/google-chat-mcp/internal/directory"
	"github.com/mmedum/google-chat-mcp/internal/gchat"
)

// localDir is a throwaway directory with its links already resolved,
// which is the form every path this service reports back comes in:
// files() resolves GCM_LOCAL_DIR once, so a test comparing against the
// raw name of t.TempDir() compares two spellings of one directory.
// Both non-Linux runners hand out such a name — macOS puts temporary
// directories under /var, a link to /private/var, and Windows uses 8.3
// short names like C:\Users\RUNNER~1.
func localDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the temp dir: %v", err)
	}
	return dir
}

// newTransferService is newService with a local directory, which is
// what the file-transfer tools need before they will do anything.
func newTransferService(t *testing.T, dir string, handler http.HandlerFunc) *Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := gchat.New(gchat.Options{
		HTTP:       srv.Client(),
		ChatBase:   srv.URL + "/v1",
		PeopleBase: srv.URL + "/people",
		OIDCBase:   srv.URL + "/oidc",
		Tokens:     staticToken("test"),
	})
	cache := directory.NewCache("", time.Hour, nil)
	cfg := config.Config{LocalDir: dir}
	return New(client, directory.NewResolver(client, cache, nil), cfg, slog.New(slog.DiscardHandler))
}

// messageWithAttachment answers a get_message with one uploaded file on
// it, and serves that file's bytes from the media endpoint.
func messageWithAttachment(body, contentName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/media/") {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, body)
			return
		}
		fmt.Fprintf(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg1",
		  "sender":{"name":"users/1"},"createTime":"2026-01-02T03:04:05Z","text":"here it is",
		  "attachment":[{"name":"spaces/AAAAspace1/messages/AAAAmsg1/attachments/AAAAattach1",
		    "contentName":%q,"contentType":"text/plain","source":"UPLOADED_CONTENT",
		    "attachmentDataRef":{"resourceName":"spaces/AAAAspace1/attachments/AAAAmedia1"}}]}`, contentName)
	}
}

func TestDownloadAttachmentWritesTheFile(t *testing.T) {
	dir := localDir(t)
	s := newTransferService(t, dir, messageWithAttachment("the standup notes", "notes.txt"))

	got, err := s.DownloadAttachment(context.Background(), DownloadAttachmentInput{
		Message: "spaces/AAAAspace1/messages/AAAAmsg1",
	})
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	if want := filepath.Join(dir, "notes.txt"); got.Path != want {
		t.Errorf("path = %q, want %q", got.Path, want)
	}
	on, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatalf("read what was written: %v", err)
	}
	if string(on) != "the standup notes" {
		t.Errorf("wrote %q", on)
	}
	if got.Bytes != int64(len("the standup notes")) {
		t.Errorf("bytes = %d", got.Bytes)
	}
	sum := sha256.Sum256([]byte("the standup notes"))
	if got.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 = %q", got.SHA256)
	}
	// The attachment's own type wins. Google serves every media
	// download as application/octet-stream, so taking the transfer's
	// answer would throw away the only useful one.
	if got.ContentType != "text/plain" {
		t.Errorf("content type = %q", got.ContentType)
	}
}

// A second download of the same file lands beside the first. The file
// already there belongs to whoever put it there.
// Google serves a media download as application/octet-stream whatever
// the file is. The attachment resource knows better, and that is the
// answer to report.
func TestDownloadAttachmentPrefersTheAttachmentsOwnType(t *testing.T) {
	dir := localDir(t)
	s := newTransferService(t, dir, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/media/") {
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, "the notes")
			return
		}
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg1","sender":{"name":"users/1"},
		  "createTime":"2026-01-02T03:04:05Z",
		  "attachment":[{"name":"spaces/AAAAspace1/messages/AAAAmsg1/attachments/AAAAattach1",
		    "contentName":"notes.txt","contentType":"text/plain; charset=utf-8",
		    "attachmentDataRef":{"resourceName":"spaces/AAAAspace1/attachments/AAAAmedia1"}}]}`)
	})
	got, err := s.DownloadAttachment(context.Background(), DownloadAttachmentInput{
		Message: "spaces/AAAAspace1/messages/AAAAmsg1",
	})
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	if got.ContentType != "text/plain; charset=utf-8" {
		t.Errorf("content type = %q, want the attachment's own", got.ContentType)
	}
}

func TestDownloadAttachmentNeverOverwrites(t *testing.T) {
	dir := localDir(t)
	s := newTransferService(t, dir, messageWithAttachment("the standup notes", "notes.txt"))

	in := DownloadAttachmentInput{Message: "spaces/AAAAspace1/messages/AAAAmsg1"}
	first, err := s.DownloadAttachment(context.Background(), in)
	if err != nil {
		t.Fatalf("first download: %v", err)
	}
	second, err := s.DownloadAttachment(context.Background(), in)
	if err != nil {
		t.Fatalf("second download: %v", err)
	}
	if second.Path == first.Path {
		t.Fatalf("both downloads landed on %s", first.Path)
	}
	if want := filepath.Join(dir, "notes-2.txt"); second.Path != want {
		t.Errorf("second path = %q, want %q", second.Path, want)
	}
}

// A name that would escape the directory, or name a hidden file beside
// it, is not a name this server writes to.
func TestDownloadAttachmentKeepsTheNameInsideTheDirectory(t *testing.T) {
	dir := localDir(t)
	s := newTransferService(t, dir, messageWithAttachment("x", "../../etc/passwd"))

	got, err := s.DownloadAttachment(context.Background(), DownloadAttachmentInput{
		Message: "spaces/AAAAspace1/messages/AAAAmsg1",
	})
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	if filepath.Dir(got.Path) != dir {
		t.Errorf("wrote to %s, outside %s", got.Path, dir)
	}
}

// Google states the length. A body that stops short of it is a failed
// download, not a short file, and nothing is left behind to look like
// one that worked.
func TestADownloadThatStopsShortKeepsNothing(t *testing.T) {
	dir := localDir(t)
	s := newTransferService(t, dir, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/media/") {
			w.Header().Set("Content-Length", "100")
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("only nine"))
			// Cutting the connection is what makes the short body an
			// error rather than a body Go pads out.
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg1","sender":{"name":"users/1"},
		  "createTime":"2026-01-02T03:04:05Z","attachment":[{"name":"spaces/AAAAspace1/messages/AAAAmsg1/attachments/AAAAattach1",
		    "contentName":"notes.txt","attachmentDataRef":{"resourceName":"spaces/AAAAspace1/attachments/AAAAmedia1"}}]}`)
	})

	_, err := s.DownloadAttachment(context.Background(), DownloadAttachmentInput{
		Message: "spaces/AAAAspace1/messages/AAAAmsg1",
	})
	if err == nil {
		t.Fatal("a truncated download was reported as a success")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("read the directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("%d files left behind: %v", len(entries), entries)
	}
}

func TestDownloadAttachmentRefusals(t *testing.T) {
	driveFile := func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg1","sender":{"name":"users/1"},
		  "createTime":"2026-01-02T03:04:05Z","attachment":[{"name":"spaces/AAAAspace1/messages/AAAAmsg1/attachments/AAAAattach1",
		    "contentName":"budget.xlsx","source":"DRIVE_FILE","driveDataRef":{"driveFileId":"AAAAdrivefile1"}}]}`)
	}
	twoFiles := func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg1","sender":{"name":"users/1"},
		  "createTime":"2026-01-02T03:04:05Z","attachment":[
		    {"name":"spaces/AAAAspace1/messages/AAAAmsg1/attachments/AAAAattach1","contentName":"one.txt",
		     "attachmentDataRef":{"resourceName":"spaces/AAAAspace1/attachments/AAAAmedia1"}},
		    {"name":"spaces/AAAAspace1/messages/AAAAmsg1/attachments/AAAAattach2","contentName":"two.txt",
		     "attachmentDataRef":{"resourceName":"spaces/AAAAspace1/attachments/AAAAmedia2"}}]}`)
	}
	none := func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg1","sender":{"name":"users/1"},
		  "createTime":"2026-01-02T03:04:05Z","text":"no files here"}`)
	}

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		in      DownloadAttachmentInput
		class   Class
		says    string
	}{
		{
			name:    "a Drive file's bytes are not Chat's",
			handler: driveFile,
			in:      DownloadAttachmentInput{Message: "spaces/AAAAspace1/messages/AAAAmsg1"},
			class:   ClassUnsupported,
			says:    "AAAAdrivefile1",
		},
		{
			name:    "more than one attachment and none named",
			handler: twoFiles,
			in:      DownloadAttachmentInput{Message: "spaces/AAAAspace1/messages/AAAAmsg1"},
			class:   ClassInvalid,
			says:    "spaces/AAAAspace1/messages/AAAAmsg1/attachments/AAAAattach2",
		},
		{
			name:    "a name that is not on the message",
			handler: twoFiles,
			in: DownloadAttachmentInput{
				Message:    "spaces/AAAAspace1/messages/AAAAmsg1",
				Attachment: "spaces/AAAAspace1/messages/AAAAmsg1/attachments/AAAAnope",
			},
			class: ClassNotFound,
			says:  "AAAAattach1",
		},
		{
			name:    "no attachment at all",
			handler: none,
			in:      DownloadAttachmentInput{Message: "spaces/AAAAspace1/messages/AAAAmsg1"},
			class:   ClassNotFound,
			says:    "no attachment",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := localDir(t)
			s := newTransferService(t, dir, tc.handler)
			_, err := s.DownloadAttachment(context.Background(), tc.in)
			assertClass(t, err, tc.class)
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error = %q, want it to mention %q", err, tc.says)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("a refusal left %d files behind", len(entries))
			}
		})
	}
}

// With no local directory the tool refuses, and the refusal names the
// variable that turns it on.
func TestFileTransferIsOffWithoutALocalDirectory(t *testing.T) {
	s := newService(t, messageWithAttachment("x", "notes.txt"))
	_, err := s.DownloadAttachment(context.Background(), DownloadAttachmentInput{
		Message: "spaces/AAAAspace1/messages/AAAAmsg1",
	})
	assertClass(t, err, ClassUnsupported)
	if !strings.Contains(err.Error(), "GCM_LOCAL_DIR") {
		t.Errorf("error = %q, want it to name the setting", err)
	}
}

func TestSafeName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"notes.txt", "notes.txt"},
		{"../../etc/passwd", "-..-etc-passwd"},
		{"a/b\\c:d", "a-b-c-d"},
		{".hidden", "hidden"},
		{"two\nlines.txt", "twolines.txt"},
		{"  spaced   out.txt  ", "spaced out.txt"},
		{"", "attachment"},
		{"...", "attachment"},
		{strings.Repeat("x", 300) + ".txt", strings.Repeat("x", 80)},
	} {
		if got := safeName(tc.in); got != tc.want {
			t.Errorf("safeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The file goes up in a multipart body whose first part is the metadata
// Google reads and whose second is the file itself.
func TestUploadAttachmentSendsTheFile(t *testing.T) {
	dir := localDir(t)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("the standup notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotPath, gotType, gotBody string
	var gotLength int64
	s := newTransferService(t, dir, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotType, gotLength = r.URL.Path, r.Header.Get("Content-Type"), r.ContentLength
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		fmt.Fprint(w, `{"attachmentDataRef":{"attachmentUploadToken":"AAAAuploadtoken1"}}`)
	})

	got, err := s.UploadAttachment(context.Background(), UploadAttachmentInput{
		Space: "spaces/AAAAspace1", Path: "notes.txt",
	})
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if got.UploadToken != "AAAAuploadtoken1" {
		t.Errorf("token = %q", got.UploadToken)
	}
	if got.FileName != "notes.txt" || got.Bytes != 17 || !strings.HasPrefix(got.ContentType, "text/plain") {
		t.Errorf("result = %+v", got)
	}
	if want := "/upload/v1/spaces/AAAAspace1/attachments:upload"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if !strings.HasPrefix(gotType, "multipart/related; boundary=") {
		t.Errorf("content type = %q", gotType)
	}
	// A length, not chunked: Google's upload endpoint wants one, and it
	// is knowable because the file was measured before it was opened.
	if gotLength != int64(len(gotBody)) {
		t.Errorf("content length = %d, body is %d bytes", gotLength, len(gotBody))
	}
	if !strings.Contains(gotBody, `{"filename":"notes.txt"}`) {
		t.Errorf("no metadata part in:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "the standup notes") {
		t.Errorf("no file part in:\n%s", gotBody)
	}
}

// A dry run reports the file and the metadata, and sends nothing.
func TestUploadAttachmentDryRunSendsNothing(t *testing.T) {
	dir := localDir(t)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTransferService(t, dir, func(http.ResponseWriter, *http.Request) {
		t.Error("a dry run reached Google")
	})
	got, err := s.UploadAttachment(gchat.WithoutWrites(context.Background()), UploadAttachmentInput{
		Space: "spaces/AAAAspace1", Path: "notes.txt", DryRun: true,
	})
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if got.UploadToken != "" {
		t.Errorf("a dry run returned a token: %q", got.UploadToken)
	}
	if got.Rendered["filename"] != "notes.txt" {
		t.Errorf("rendered = %v", got.Rendered)
	}
}

func TestUploadAttachmentRefusals(t *testing.T) {
	dir := localDir(t)
	outsideDir := localDir(t)
	write := func(path string, size int) string {
		t.Helper()
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	inside := write(filepath.Join(dir, "notes.txt"), 5)
	outside := write(filepath.Join(outsideDir, "secret.txt"), 5)
	empty := write(filepath.Join(dir, "empty.txt"), 0)
	// A link inside the directory pointing out of it. Resolving the
	// symlinks first is what refuses this; a check on the name alone
	// would pass it.
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	s := newTransferService(t, dir, func(http.ResponseWriter, *http.Request) {
		t.Error("a refused argument must not reach Google")
	})
	for _, tc := range []struct {
		name  string
		in    UploadAttachmentInput
		class Class
	}{
		{"no space", UploadAttachmentInput{Path: inside}, ClassInvalid},
		{"no file", UploadAttachmentInput{Space: "spaces/AAAAspace1"}, ClassInvalid},
		{"a file that is not there", UploadAttachmentInput{
			Space: "spaces/AAAAspace1", Path: filepath.Join(dir, "nope.txt")}, ClassNotFound},
		{"a file outside the directory", UploadAttachmentInput{
			Space: "spaces/AAAAspace1", Path: outside}, ClassForbidden},
		{"a link out of the directory", UploadAttachmentInput{
			Space: "spaces/AAAAspace1", Path: link}, ClassForbidden},
		{"a directory", UploadAttachmentInput{Space: "spaces/AAAAspace1", Path: dir}, ClassInvalid},
		{"an empty file", UploadAttachmentInput{Space: "spaces/AAAAspace1", Path: empty}, ClassInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.UploadAttachment(context.Background(), tc.in)
			assertClass(t, err, tc.class)
		})
	}
}

// The dry-run guard is structural: an upload that forgot its own
// preview branch is refused by the client rather than sending the file.
func TestAnUploadCannotEscapeAPreview(t *testing.T) {
	dir := localDir(t)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTransferService(t, dir, func(http.ResponseWriter, *http.Request) {
		t.Error("a write reached Google under a preview context")
	})
	_, err := s.UploadAttachment(gchat.WithoutWrites(context.Background()), UploadAttachmentInput{
		Space: "spaces/AAAAspace1", Path: "notes.txt",
	})
	if err == nil {
		t.Fatal("the upload was allowed to write")
	}
}

// An uploaded file reaches a space by being named in the message that
// carries it.
func TestSendMessageCarriesAnUploadedFile(t *testing.T) {
	var body map[string]any
	s := newService(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"name":"spaces/AAAAspace1/messages/AAAAmsg1",
		  "thread":{"name":"spaces/AAAAspace1/threads/AAAAthread1"}}`)
	})
	if _, err := s.SendMessage(context.Background(), SendMessageInput{
		Space: "spaces/AAAAspace1", Text: "here are the notes", UploadToken: "AAAAuploadtoken1",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	attachments, _ := body["attachment"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("attachment = %v", body["attachment"])
	}
	first, _ := attachments[0].(map[string]any)
	ref, _ := first["attachmentDataRef"].(map[string]any)
	if ref["attachmentUploadToken"] != "AAAAuploadtoken1" {
		t.Errorf("ref = %v", ref)
	}
	// The body is still the body: attaching a file changes nothing
	// about the text.
	if body["text"] != "here are the notes" {
		t.Errorf("text = %v", body["text"])
	}
}
