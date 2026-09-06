package gchat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadMediaAsksGoogleForTheBytes(t *testing.T) {
	var gotPath, gotQuery, gotAccept, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotAccept, gotAuth = r.Header.Get("Accept"), r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.7 and the rest"))
	}))
	defer srv.Close()

	media, err := newTestClient(t, srv).DownloadMedia(context.Background(),
		"spaces/AAAAspace1/messages/AAAAmsg1/attachments/AAAAmedia1")
	if err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	defer func() { _ = media.Body.Close() }()

	// The literal comes before the name, which is the one endpoint here
	// shaped that way.
	if want := "/v1/media/spaces/AAAAspace1/messages/AAAAmsg1/attachments/AAAAmedia1"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotQuery != "alt=media" {
		t.Errorf("query = %q, want alt=media", gotQuery)
	}
	// Asking for JSON here would be asking Google for the wrong thing.
	if gotAccept != "*/*" || gotAuth != "Bearer test-token" {
		t.Errorf("headers = %q / %q", gotAccept, gotAuth)
	}
	if media.ContentType != "application/pdf" || media.Length != 21 {
		t.Errorf("media = %+v", media)
	}
	body, err := io.ReadAll(media.Body)
	if err != nil {
		t.Fatalf("read the body: %v", err)
	}
	if string(body) != "%PDF-1.7 and the rest" {
		t.Errorf("body = %q", body)
	}
}

// A transfer retries while it is still asking, and stops retrying the
// moment the caller has bytes. Repeating it then would start a second
// download over the top of a half-written file.
func TestATransferRetriesOnlyBeforeTheBodyIsHandedOver(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Length", "40")
		_, _ = w.Write([]byte("nine only"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()

	media, err := newTestClient(t, srv).DownloadMedia(context.Background(), "spaces/AAAAspace1/attachments/AAAAmedia1")
	if err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	defer func() { _ = media.Body.Close() }()
	if got := calls.Load(); got != 2 {
		t.Fatalf("%d calls, want the 503 retried once", got)
	}

	// The body failing is the caller's to see, and the client does not
	// go back for more.
	if _, err := io.ReadAll(media.Body); err == nil {
		t.Error("a cut-off body read clean")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("%d calls, want no retry once the bytes were flowing", got)
	}
}

// A connection that stops sending is cut. The timeout covers the
// headers and then each individual Read, so a large file is not held to
// the deadline that bounds a metadata call.
func TestAStalledTransferIsCut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("the first block, then nothing"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer srv.Close()
	defer close(release)

	c := newTestClient(t, srv, func(o *Options) {
		o.Timeout = 100 * time.Millisecond
		o.MaxRetries = 0
	})
	media, err := c.DownloadMedia(context.Background(), "spaces/AAAAspace1/attachments/AAAAmedia1")
	if err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	defer func() { _ = media.Body.Close() }()

	start := time.Now()
	if _, err := io.ReadAll(media.Body); err == nil {
		t.Fatal("a stalled body read clean")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("the stall guard took %s to fire", waited)
	}
}

// No headers inside the timeout is its own failure, and the message
// says so rather than reporting a cancelled context.
func TestATransferWithNoAnswerSaysSo(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	c := newTestClient(t, srv, func(o *Options) {
		o.Timeout = 100 * time.Millisecond
		o.MaxRetries = 0
	})
	_, err := c.DownloadMedia(context.Background(), "spaces/AAAAspace1/attachments/AAAAmedia1")
	if err == nil {
		t.Fatal("a server that never answered was reported as a success")
	}
	if !strings.Contains(err.Error(), "nothing moved for") {
		t.Errorf("error = %q, want it to name the deadline", err)
	}
}

func TestMimeOnly(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"text/plain; charset=utf-8", "text/plain"},
		{"application/pdf", "application/pdf"},
		{"", ""},
	} {
		if got := mimeOnly(tc.in); got != tc.want {
			t.Errorf("mimeOnly(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An upload is bounded too. It had no timeout of any kind when it was
// its own send path — the same bug the OAuth refresh had — so this
// pins the discipline rather than the code that happens to provide it.
func TestAnUploadThatNeverGetsAnAnswerIsCut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-release
	}))
	defer srv.Close()
	defer close(release)

	c := newTestClient(t, srv, func(o *Options) {
		o.Timeout = 100 * time.Millisecond
		o.MaxRetries = 0
	})
	start := time.Now()
	_, err := c.UploadAttachment(context.Background(), "spaces/AAAAspace1", "notes.txt",
		"text/plain", strings.NewReader("the notes"), 9)
	if err == nil {
		t.Fatal("an upload that was never answered was reported as a success")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("the guard took %s to fire", waited)
	}
}

// A write may not run under a dry run, whichever send path it takes.
// The transfer paths used to be outside that guard.
func TestAnUploadCannotRunUnderADryRun(t *testing.T) {
	var reached atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached.Store(true)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).UploadAttachment(WithoutWrites(context.Background()),
		"spaces/AAAAspace1", "notes.txt", "text/plain", strings.NewReader("the notes"), 9)
	if !errors.Is(err, ErrWriteForbidden) {
		t.Fatalf("error = %v, want ErrWriteForbidden", err)
	}
	if reached.Load() {
		t.Error("the upload reached the server under a context that forbids writes")
	}
}

// slowReader hands over one block at a time, pausing between them. It
// never stalls: every read makes progress well inside the limit.
type slowReader struct {
	blocks int
	pause  time.Duration
	block  []byte
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.blocks == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.pause)
	r.blocks--
	return copy(p, r.block), nil
}

// An upload slower than the timeout is not a stalled upload. The
// deadline covers waiting for Google, not the time bytes spend moving
// — http.Client.Do does not return until the whole request body has
// been written, so a deadline that ran across it killed every upload
// that took longer than one metadata call.
func TestASlowUploadIsNotCutOff(t *testing.T) {
	var got int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"attachmentDataRef":{"attachmentUploadToken":"AAAAuploadtoken1"}}`)
	}))
	defer srv.Close()

	// Twenty blocks, 25 ms apart: half a second of transfer under a
	// 200 ms deadline, with no read anywhere near stalling.
	body := &slowReader{blocks: 20, pause: 25 * time.Millisecond, block: make([]byte, 1024)}
	c := newTestClient(t, srv, func(o *Options) {
		o.Timeout = 200 * time.Millisecond
		o.MaxRetries = 0
	})
	ref, err := c.UploadAttachment(context.Background(), "spaces/AAAAspace1", "notes.bin",
		"application/octet-stream", body, 20*1024)
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if ref.AttachmentUploadToken != "AAAAuploadtoken1" {
		t.Errorf("ref = %+v", ref)
	}
	if got != 20*1024+int64(len("notes.bin")) && got < 20*1024 {
		t.Errorf("server read %d bytes, want at least the 20 KiB body", got)
	}
}

// The other half of the same deadline: once the body is written, the
// wait for Google's answer is bounded again. Without the hand-back an
// upload whose bytes all landed would wait forever for a reply.
func TestAnUploadThatSendsAndIsThenIgnoredIsCut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-release
	}))
	defer srv.Close()
	defer close(release)

	c := newTestClient(t, srv, func(o *Options) {
		o.Timeout = 150 * time.Millisecond
		o.MaxRetries = 0
	})
	start := time.Now()
	_, err := c.UploadAttachment(context.Background(), "spaces/AAAAspace1", "notes.txt",
		"text/plain", strings.NewReader("the notes"), 9)
	if err == nil {
		t.Fatal("an upload that was never answered was reported as a success")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("the guard took %s to fire", waited)
	}
}
