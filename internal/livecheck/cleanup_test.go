//go:build live

package livecheck

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The driver's own cleanup, held without a login: fakeChat answers the
// tool calls in place of the binary and Google, and a recorder takes the
// reports in place of a testing.T, so a test can watch the run fail
// without failing itself.

// fakeSpace is the scratch space fakeChat holds.
const fakeSpace = "spaces/AAAAspace1"

// fakeChat holds one space whose access state update_space changes. A
// tool can be made to refuse, or to lose the connection.
type fakeChat struct {
	// access is what get_space reports: PRIVATE or DISCOVERABLE.
	access string
	// stuck makes update_space succeed and leave the space as it was.
	stuck   bool
	refused map[string]bool
	dropped map[string]bool
	// calls is every tool called, in order.
	calls []string
}

func (f *fakeChat) callTool(_ context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	f.calls = append(f.calls, p.Name)
	if f.dropped[p.Name] {
		return nil, errors.New("connection closed")
	}
	if f.refused[p.Name] {
		// Google's error text can name the space and what is in it.
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{
			&mcp.TextContent{Text: "[unavailable] " + fakeSpace + "/threads/t1 is busy"},
		}}, nil
	}
	args, _ := p.Arguments.(map[string]any)
	switch p.Name {
	case "update_space":
		if args["audience"] == "private" && !f.stuck {
			f.access = "PRIVATE"
		}
	case "get_space":
		return &mcp.CallToolResult{StructuredContent: map[string]any{"access_state": f.access}}, nil
	}
	return &mcp.CallToolResult{StructuredContent: map[string]any{}}, nil
}

// recorder stands in for a testing.T. Fatal ends the goroutine, as
// t.Fatal does, so driver code runs under untilStopped.
type recorder struct {
	testing.TB
	failed bool
	lines  []string
}

func (r *recorder) Helper()                           {}
func (r *recorder) Failed() bool                      { return r.failed }
func (r *recorder) Log(args ...any)                   { r.note(fmt.Sprint(args...)) }
func (r *recorder) Logf(format string, args ...any)   { r.note(fmt.Sprintf(format, args...)) }
func (r *recorder) Error(args ...any)                 { r.fail(fmt.Sprint(args...)) }
func (r *recorder) Errorf(format string, args ...any) { r.fail(fmt.Sprintf(format, args...)) }
func (r *recorder) Fatal(args ...any)                 { r.fail(fmt.Sprint(args...)); runtime.Goexit() }
func (r *recorder) Fatalf(format string, args ...any) {
	r.fail(fmt.Sprintf(format, args...))
	runtime.Goexit()
}

func (r *recorder) note(s string) { r.lines = append(r.lines, s) }
func (r *recorder) fail(s string) { r.failed = true; r.note(s) }
func (r *recorder) text() string  { return strings.Join(r.lines, "\n") }

// untilStopped runs f in a goroutine of its own and waits for it, so a
// Fatal ends f and not the test.
func untilStopped(f func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	<-done
}

// seededDriver is a driver holding the scratch space as seed leaves it,
// plus a thread in it, with fake answering its calls.
func seededDriver(t *testing.T, fake *fakeChat) (*driver, *recorder) {
	t.Helper()
	rec := &recorder{TB: t}
	d := &driver{t: rec, ctx: context.Background(), callTool: fake.callTool, made: map[string]bool{}}
	d.space, d.name = fakeSpace, spacePrefix+" 1 (safe to delete)"
	d.record(fakeSpace)
	d.record(fakeSpace + "/threads/t1")
	return d, rec
}

// opening is the audience step as far as cleanup can tell: it opens the
// space, then does whatever then does.
func opening(fake *fakeChat, then func(*driver)) step {
	return step{"the space opens", "set_space_audience", func(d *driver) {
		fake.access = "DISCOVERABLE"
		then(d)
	}}
}

// loud is how the run starts the report of a space it could not close.
const loud = "THE SCRATCH SPACE MAY STILL BE OPEN TO THE ORGANIZATION"

func TestCleanupClosesAnOpenedSpaceBeforeKeepingOrDeletingIt(t *testing.T) {
	cases := []struct {
		name   string
		opened bool
		failed bool
		keep   string
		calls  string
	}{
		{name: "a passing run", opened: true, calls: "update_space get_space delete_space"},
		{name: "a failed run, which keeps the space", opened: true, failed: true, calls: "update_space get_space"},
		{name: "LIVE_KEEP, which keeps the space", opened: true, keep: "1", calls: "update_space get_space"},
		{name: "no step opened the space", calls: "delete_space"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LIVE_KEEP", tc.keep)
			fake := &fakeChat{access: "PRIVATE"}
			d, rec := seededDriver(t, fake)
			s := step{"a message posts", "send_message", func(*driver) {}}
			if tc.opened {
				s = opening(fake, func(*driver) {})
			}
			untilStopped(func() { d.runStep(rec, s) })
			rec.failed = tc.failed
			untilStopped(d.drop)

			if got, want := strings.Join(fake.calls, " "), tc.calls; got != want {
				t.Errorf("cleanup called %s, want %s", got, want)
			}
			if fake.access != "PRIVATE" {
				t.Error("cleanup left the space open")
			}
			if got := rec.text(); strings.Contains(got, loud) {
				t.Errorf("cleanup raised the alarm over a space it closed:\n%s", got)
			}
		})
	}
}

// The step closes the space itself; a refused close in cleanup is then
// no alarm, because the read-back is what says whether it is closed.
func TestCleanupTrustsTheReadBackOverTheClose(t *testing.T) {
	fake := &fakeChat{access: "PRIVATE", refused: map[string]bool{"update_space": true}}
	d, rec := seededDriver(t, fake)
	untilStopped(func() {
		d.runStep(rec, opening(fake, func(*driver) { fake.access = "PRIVATE" }))
	})
	untilStopped(d.drop)

	if got := rec.text(); rec.failed || strings.Contains(got, loud) {
		t.Errorf("a space that reads back private failed the run:\n%s", got)
	}
	if got, want := strings.Join(fake.calls, " "), "update_space get_space delete_space"; got != want {
		t.Errorf("cleanup called %s, want %s", got, want)
	}
}

func TestCleanupSaysSoLoudlyWhenTheSpaceStaysOpen(t *testing.T) {
	cases := []struct {
		name string
		fake fakeChat
	}{
		{name: "update_space is refused", fake: fakeChat{refused: map[string]bool{"update_space": true}}},
		{name: "the connection is lost", fake: fakeChat{dropped: map[string]bool{"update_space": true, "get_space": true}}},
		{name: "Google answers and leaves the space open", fake: fakeChat{stuck: true}},
		{name: "the space does not read back", fake: fakeChat{refused: map[string]bool{"get_space": true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LIVE_KEEP", "")
			fake := &tc.fake
			d, rec := seededDriver(t, fake)
			untilStopped(func() { d.runStep(rec, opening(fake, func(*driver) {})) })
			untilStopped(d.drop)

			got := rec.text()
			if !rec.failed || !strings.Contains(got, loud) {
				t.Errorf("the run did not fail loudly over a space it could not close:\n%s", got)
			}
			if !strings.Contains(got, "spaces/<redacted>") {
				t.Errorf("the report does not name the space by its redacted id:\n%s", got)
			}
			if strings.Contains(got, strings.TrimPrefix(fakeSpace, "spaces/")) {
				t.Errorf("the report names a space that may be open by its id:\n%s", got)
			}
		})
	}
}

// A step that stops part way leaves the space as open as it got, and
// what the step wrote on its copy of the driver is lost with it.
func TestCleanupClosesTheSpaceAfterTheOpeningStepStops(t *testing.T) {
	t.Setenv("LIVE_KEEP", "")
	fake := &fakeChat{access: "PRIVATE"}
	d, rec := seededDriver(t, fake)
	untilStopped(func() {
		d.runStep(rec, opening(fake, func(d *driver) { d.t.Fatal("the step stopped") }))
	})
	untilStopped(d.drop)

	if got, want := strings.Join(fake.calls, " "), "update_space get_space"; got != want {
		t.Errorf("cleanup called %s, want %s", got, want)
	}
	if fake.access != "PRIVATE" {
		t.Error("cleanup left the space open")
	}
}

func TestAMissingGroupStopsTheRunBeforeTheSpaceIsMade(t *testing.T) {
	t.Setenv("LIVE_GROUP_EMAIL", "")
	fake := &fakeChat{}
	rec := &recorder{TB: t}
	d := &driver{t: rec, ctx: context.Background(), callTool: fake.callTool, made: map[string]bool{}}
	untilStopped(d.seed)

	if got := rec.text(); !rec.failed || !strings.Contains(got, "LIVE_GROUP_EMAIL") {
		t.Errorf("seed did not stop for want of LIVE_GROUP_EMAIL:\n%s", got)
	}
	if len(fake.calls) != 0 {
		t.Errorf("seed made %d call(s) before it stopped; the space must not be made", len(fake.calls))
	}
}
