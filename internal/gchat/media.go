package gchat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmedum/google-chat-mcp/v2/internal/scopes"
)

// MaxAttachmentBytes is the largest attachment Google accepts, from the
// Chat discovery document's own maxSize for media.upload.
const MaxAttachmentBytes = 200 << 20

// Media is a byte stream from Google and what the response said about
// it. The caller closes Body, which also releases the request.
type Media struct {
	Body io.ReadCloser
	// ContentType is what Google said the bytes are.
	ContentType string
	// Length is Content-Length, or -1 when the response did not say.
	Length int64
}

// DownloadMedia streams an attachment's bytes.
//
// resourceName is the opaque value from an attachment's
// attachmentDataRef, not the attachment's own resource name. An
// attachment stored in Drive has no media resource name at all: its
// bytes are Drive's, and the Chat media endpoint cannot serve them.
func (c *Client) DownloadMedia(ctx context.Context, resourceName string) (*Media, error) {
	return c.doStream(ctx, request{
		method: http.MethodGet,
		prefix: "media",
		name:   resourceName,
		query:  url.Values{"alt": []string{"media"}},
		scope:  scopes.MessagesReadonly,
		accept: "*/*",
	})
}

// doStream performs a request and hands the response body back unread.
//
// It goes through send like every other call, so the dry-run guard, the
// limiter and the retry rule are the same ones. What it does not share
// is what happens to the answer: do reads the whole body into memory,
// which is right for a JSON answer and wrong for a 200 MB file.
//
// Retries happen only before the body is handed over. Once the caller
// has bytes, a failure is the caller's to see — repeating the request
// would start a second download over the top of a half-written file.
func (c *Client) doStream(ctx context.Context, r request) (*Media, error) {
	return send(c, ctx, r, true, func(endpoint, path, token string) (*Media, time.Duration, error) {
		return c.transfer(ctx, r, endpoint, path, nil, 0, token)
	})
}

// transfer makes one call whose bytes do not fit in memory, in either
// direction, and returns the live response.
//
// The timeout covers the response headers and then stands down: neither
// a 200 MB download nor a 200 MB upload can finish inside the deadline
// that bounds a metadata call, and holding them to it would fail every
// large transfer. What replaces it is a stall guard on the body — each
// Read must make progress within the same timeout — so a connection
// that stops sending or stops reading is still cut, and one that is
// merely slow is not.
//
// Both directions come through here so that neither can be written
// without that discipline. The upload path had no timeout of any kind
// when it was its own function, which is the same bug the OAuth refresh
// had a commit earlier.
func (c *Client) transfer(ctx context.Context, r request, endpoint, path string, body io.Reader, length int64, token string) (*Media, time.Duration, error) {
	ctx, cancel := context.WithCancel(ctx)

	// waiting is the deadline for the part of a transfer where this
	// server is doing nothing but waiting for Google: before the first
	// byte of an upload moves, and again between the last byte and the
	// answer. It hands over to the body's own guard while bytes are
	// actually moving, and takes over again when they stop.
	//
	// It is not simply "the whole request": http.Client.Do does not
	// return until the request body has been written, so a deadline
	// running across Do would kill any upload slower than the timeout,
	// however fast its bytes were moving. That is what it did.
	//
	// answered closes the other half of it. Stopping a time.AfterFunc
	// does not un-fire one that has already gone off, so a response
	// arriving a hair inside the deadline could be cancelled after it
	// had succeeded — the caller then sees a download that failed
	// partway through, for a transfer that was fine.
	var mu sync.Mutex
	answered := false
	waiting := time.AfterFunc(c.timeout, func() {
		mu.Lock()
		defer mu.Unlock()
		if !answered {
			cancel()
		}
	})
	arrived := func() {
		mu.Lock()
		answered = true
		mu.Unlock()
		waiting.Stop()
	}

	if body != nil {
		// The request body gets its own stall guard, and takes the
		// waiting deadline over on its first read: from then on the
		// rule is "each read makes progress", not "the whole thing
		// finishes". At the end of the body it hands the deadline back,
		// so the wait for Google's answer is bounded too.
		body = newStallGuard(io.NopCloser(body), c.timeout, cancel, nil, waiting)
	}
	req, err := c.newRequest(ctx, r, endpoint, path, body, token)
	if err != nil {
		arrived()
		cancel()
		return nil, 0, err
	}
	if length > 0 {
		// Set so the request is not chunked: Google's upload endpoint
		// wants a length, and the caller measured the file before
		// opening it.
		req.ContentLength = length
	}

	// The stream client is the configured one without its whole-request
	// timeout, which would otherwise cut a transfer off partway through.
	resp, err := c.stream.Do(req)
	arrived()
	if err != nil {
		cancel()
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return nil, 0, fmt.Errorf("chat api: %s %s: nothing moved for %s", r.method, path, c.timeout)
		}
		return nil, 0, fmt.Errorf("chat api: %s %s: %w", r.method, path, withoutURL(err))
	}
	if resp.StatusCode >= 300 {
		// Bounded: Google's error envelope is a few hundred bytes, and
		// this is the one read here that is not the caller's file.
		answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
		cancel()
		return nil, parseRetryAfter(resp.Header.Get("Retry-After")), r.apiError(resp.StatusCode, answer, path)
	}

	media := &Media{
		Body:        newStallGuard(resp.Body, c.timeout, cancel, cancel, nil),
		ContentType: mimeOnly(resp.Header.Get("Content-Type")),
		Length:      -1,
	}
	if n, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil && n >= 0 {
		media.Length = n
	}
	return media, 0, nil
}

// maxErrorBodyBytes bounds the error body read from a failed transfer.
const maxErrorBodyBytes = 1 << 20

// mimeOnly drops the parameters from a Content-Type, so "text/plain;
// charset=utf-8" is reported as the type it is.
func mimeOnly(v string) string {
	base, _, _ := strings.Cut(v, ";")
	return strings.TrimSpace(base)
}

// stallGuard cancels a transfer whose next Read makes no progress inside
// the timeout. The clock runs only while a Read is outstanding, so a
// caller that is slow to ask for more bytes is never the one cut off.
//
// onClose is what Close does beyond closing the reader, and it is the
// difference between the two ends of a transfer. A response body owns
// the request, so closing it releases the request. A request body does
// not: the transport closes it as soon as the last byte is written,
// which is before the response has been read — cancelling there would
// kill the call that was about to succeed.
type stallGuard struct {
	rc      io.ReadCloser
	timer   *time.Timer
	limit   time.Duration
	onClose context.CancelFunc
	// waiting is the deadline this guard takes over from while bytes
	// are moving, and hands back when they stop. Only a request body
	// has one: the transport reads it, so its first read is the moment
	// an upload stops waiting and starts sending, and the end of it is
	// the moment the wait for the answer begins.
	waiting *time.Timer
	started bool
}

func newStallGuard(rc io.ReadCloser, limit time.Duration, onStall, onClose context.CancelFunc, waiting *time.Timer) *stallGuard {
	t := time.AfterFunc(limit, onStall)
	t.Stop()
	return &stallGuard{rc: rc, timer: t, limit: limit, onClose: onClose, waiting: waiting}
}

func (g *stallGuard) Read(p []byte) (int, error) {
	if g.waiting != nil && !g.started {
		g.started = true
		g.waiting.Stop()
	}
	g.timer.Reset(g.limit)
	n, err := g.rc.Read(p)
	g.timer.Stop()
	if err != nil && g.waiting != nil {
		// The body is done, for good or ill. What this call is waiting
		// for now is Google's answer, so the deadline goes back on.
		g.waiting.Reset(g.limit)
	}
	return n, err
}

func (g *stallGuard) Close() error {
	g.timer.Stop()
	err := g.rc.Close()
	if g.onClose != nil {
		g.onClose()
	}
	return err
}

// UploadAttachmentRequest is the metadata half of a media upload. The
// filename is required and Google wants the extension on it.
type UploadAttachmentRequest struct {
	Filename string `json:"filename"`
}

// UploadAttachmentResponse is what Google hands back: the reference a
// message carries to attach the file.
type UploadAttachmentResponse struct {
	AttachmentDataRef *AttachmentDataRef `json:"attachmentDataRef,omitempty"`
}

// UploadAttachment sends a file to a space's attachment storage and
// returns the reference send_message takes.
//
// The body is a multipart/related request written out here rather than
// through mime/multipart: the length has to be known in advance, and a
// pipe into a multipart.Writer would leave the client no choice but
// chunked encoding. Every byte of it is Google's documented shape — a
// JSON metadata part, then the file — and the boundary is 128 random
// bits, so the file cannot contain it.
//
// size is the file's length, checked by the caller. It is what makes
// this streamable: the request's Content-Length is known before a byte
// of the file is read, so a 200 MB attachment never sits in memory.
func (c *Client) UploadAttachment(ctx context.Context, space, filename, contentType string, file io.Reader, size int64) (*AttachmentDataRef, error) {
	metadata, err := json.Marshal(UploadAttachmentRequest{Filename: filename})
	if err != nil {
		return nil, fmt.Errorf("chat api: encode the upload metadata: %w", err)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// A nil reader reads as empty rather than panicking inside the
	// multipart body. Nothing here sends one — the service refuses an
	// empty file before it gets this far — but a client method that
	// segfaults on a zero value is a trap for the next caller.
	if file == nil {
		file = strings.NewReader("")
	}
	boundary := randomBoundary()
	head := "--" + boundary + "\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n" +
		string(metadata) + "\r\n--" + boundary + "\r\nContent-Type: " + contentType + "\r\n\r\n"
	tail := "\r\n--" + boundary + "--\r\n"

	r := request{
		api:         uploadAPI,
		method:      http.MethodPost,
		name:        space,
		path:        "attachments",
		verb:        "upload",
		query:       url.Values{"uploadType": []string{"multipart"}},
		contentType: "multipart/related; boundary=" + boundary,
		scope:       scopes.MessagesCreate,
	}
	body := io.MultiReader(strings.NewReader(head), file, strings.NewReader(tail))
	length := int64(len(head)) + size + int64(len(tail))

	var out UploadAttachmentResponse
	if err := c.doUpload(ctx, r, body, length, &out); err != nil {
		return nil, err
	}
	if out.AttachmentDataRef == nil {
		return nil, fmt.Errorf("chat api: the upload returned no attachment reference")
	}
	return out.AttachmentDataRef, nil
}

// randomBoundary is the separator between the parts of an upload.
func randomBoundary() string {
	var b [16]byte
	rand.Read(b[:])
	return "gcm" + hex.EncodeToString(b[:])
}

// doUpload sends a streamed body once and decodes the answer.
//
// Once, on purpose: retries is false. Every other write here may be
// retried on the answers that mean Google turned the request away
// before applying it, but those retries replay a body held in memory.
// This body is a file being read as it goes, and there is no second
// pass over it — so a failed upload is reported, and the caller sends
// the file again.
func (c *Client) doUpload(ctx context.Context, r request, body io.Reader, length int64, out any) error {
	answer, err := send(c, ctx, r, false, func(endpoint, path, token string) (*Media, time.Duration, error) {
		return c.transfer(ctx, r, endpoint, path, body, length, token)
	})
	if err != nil {
		return err
	}
	defer func() { _ = answer.Body.Close() }()

	_, path := c.endpointFor(r)
	// The answer is Google's JSON, not a file: it is small, and the
	// bound is there because nothing else bounds a response body on the
	// transfer path.
	payload, err := io.ReadAll(io.LimitReader(answer.Body, maxErrorBodyBytes))
	if err != nil {
		return fmt.Errorf("chat api: read %s %s: %w", r.method, path, err)
	}
	return decode(payload, out, c.driftReporter(path))
}
