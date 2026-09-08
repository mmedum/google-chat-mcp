package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mmedum/google-chat-mcp/v2/internal/config"
	"github.com/mmedum/google-chat-mcp/v2/internal/userconfig"
)

// The smoke gate drives the built binary the way a host does: over
// stdio, with no credentials, against a config directory holding
// nothing.
//
// It is the only gate that runs the whole server, so it is what holds
// the parts no unit test has a seam for — the serve loop, the stdio
// transport, the shutdown path — and the rule that stdout carries
// nothing but JSON-RPC frames.

// smokeTimeout bounds one run. A server that hangs would otherwise hold
// the job until its own timeout, twenty minutes later.
const smokeTimeout = 20 * time.Second

// smokeRequests is what a host sends on connect: initialize, the
// notification that it finished, then the requests themselves.
//
// Written as wire text rather than built from structs, because it is the
// wire that is under test — a struct would be encoded by the same
// library the server decodes with, and the two would agree with each
// other about a mistake.
var smokeRequests = []string{
	`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}`,
	`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	`{"jsonrpc":"2.0","id":4,"method":"resources/templates/list"}`,
	`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_spaces","arguments":{}}}`,
}

// abruptRequests is the same connect cut short: one tool call, and stdin
// closed the instant it is written. Nothing reads its transcript, so it
// reuses the wire text above rather than keeping a second copy of it.
var abruptRequests = []string{smokeRequests[0], smokeRequests[1], smokeRequests[4]}

// want is one thing the transcript has to say.
//
// It names the response it belongs on rather than searching the whole
// transcript, because the two are not the same claim: `"isError":true`
// on some other line is not an answer to the call that was made. An
// empty text asks only that the response arrived at all.
type want struct {
	id   int
	text string
	// why says what is wrong, not what was searched for.
	why string
}

var smokeWants = []want{
	{id: 1, why: "the server did not answer initialize"},
	{id: 2, text: `"name":"list_spaces"`, why: "list_spaces is missing from tools/list"},
	{id: 2, text: `"name":"whoami"`, why: "whoami is missing from tools/list"},
	{id: 4, text: "gchat://spaces/{space_id}", why: "the space resource template is missing from resources/templates/list"},
	{id: 3, text: `"isError":true`, why: "a tool call with no credentials has to come back as a tool error, not a protocol error"},
	{id: 3, text: "[auth]", why: "the tool error has to carry the [auth] class, which is what tells a caller to log in"},
}

// smoke runs the exchange twice and reports what was wrong with it.
func smoke(args []string, stdout, stderr io.Writer) int {
	bin := binaryArg(args)

	tmp, err := os.MkdirTemp("", "gates-smoke")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: temp directory: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	// A directory that does not exist yet, so the run also covers the
	// server creating one.
	configDir := filepath.Join(tmp, "cfg")

	var problems []string
	transcript, err := runSmoke(bin, configDir, smokeRequests, awaitedIDs())
	if err != nil {
		problems = append(problems, "the server exited with an error: "+err.Error())
	}
	problems = append(problems, checkTranscript(transcript, smokeWants)...)

	// The same exchange again, with stdin closed the instant the last
	// request is written rather than a second later.
	//
	// A client that goes away mid-call is ordinary — every host restart
	// does it — but it reaches the server as the SDK's own "server is
	// closing" error rather than as an EOF, which is a different branch
	// from the one above. Getting it wrong exits non-zero, and every
	// host records a normal disconnect as a crash. The first run's
	// second of grace is enough to hide it, which is the whole reason
	// this second run exists.
	//
	// Only the exit code is asserted here. A client that leaves mid-call
	// can leave a response half-written, so holding this transcript to
	// the same reading would fail on timing rather than on behaviour.
	if _, err := runSmoke(bin, configDir, abruptRequests, nil); err != nil {
		problems = append(problems, "a client that closes stdin mid-call must still exit 0: "+err.Error())
	}

	if len(problems) > 0 {
		for _, p := range problems {
			_, _ = fmt.Fprintln(stderr, "gates: "+p)
		}
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "stdio smoke ok")
	return 0
}

// checkTranscript reads the server's stdout and reports everything wrong
// with it rather than the first thing. A run costs a second or two, and
// one finding per run is a slow way to learn there were three.
func checkTranscript(transcript string, wants []want) []string {
	var problems []string
	byID := map[string]string{}
	for _, line := range strings.Split(transcript, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		id, isFrame := frameID(line)
		if !isFrame {
			problems = append(problems, "stdout carries a line that is not a JSON-RPC frame: "+abbrev(line, 200))
			continue
		}
		// A notification carries no id and answers nothing.
		if id != "" {
			byID[id] = line
		}
	}

	for _, w := range wants {
		line, ok := byID[strconv.Itoa(w.id)]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s: nothing came back with id %d", w.why, w.id))
		case w.text != "" && !strings.Contains(line, w.text):
			problems = append(problems, fmt.Sprintf("%s: the response with id %d does not carry %s", w.why, w.id, w.text))
		}
	}
	return problems
}

// frameID reads one line of the server's stdout: whether it is a
// JSON-RPC frame at all, and the id it answers when it has one.
//
// Parsed rather than matched on a prefix. The rule is that stdout
// carries JSON-RPC frames and nothing else, and a log line that happens
// to start with the right characters is exactly the failure worth
// catching.
func frameID(line string) (string, bool) {
	var frame struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(line), &frame); err != nil || frame.JSONRPC != "2.0" {
		return "", false
	}
	return strings.Trim(string(frame.ID), `"`), true
}

// awaitedIDs are the responses the first run waits for before it closes
// stdin, derived from the wants so that a want added tomorrow is waited
// for rather than raced against.
func awaitedIDs() []string {
	ids := make([]string, 0, len(smokeWants))
	for _, w := range smokeWants {
		ids = append(ids, strconv.Itoa(w.id))
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// runSmoke starts the binary, writes the requests, and returns whatever
// it printed on stdout.
//
// await is the set of response ids to see before closing stdin. Waiting
// on the answers rather than on a clock is the difference between this
// and the shell it replaces: a pipeline has no way to know when the
// server has replied, so it slept a second, which cost a second on every
// run and was still a guess on a slow machine. An empty await closes
// stdin the instant the last byte is written, which is an ordinary
// client disconnect and a different path through the server.
//
// A server that stays alive and answers nothing costs the full timeout.
// That is the timeout doing its job: the transcript comes back either
// way, so the run is still reported in full rather than only as a hang.
func runSmoke(bin, configDir string, requests, await []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), smokeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = smokeEnv(configDir)
	var logs bytes.Buffer
	cmd.Stderr = &logs
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", bin, err)
	}

	var writeErr error
	for _, r := range requests {
		if _, writeErr = io.WriteString(stdin, r+"\n"); writeErr != nil {
			break
		}
	}

	var transcript strings.Builder
	lines := bufio.NewScanner(stdout)
	// One tools/list response is well past the scanner's default limit.
	lines.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	keep := func() { transcript.WriteString(lines.Text()); transcript.WriteByte('\n') }

	waiting := make(map[string]bool, len(await))
	for _, id := range await {
		waiting[id] = true
	}
	for len(waiting) > 0 && lines.Scan() {
		keep()
		if id, ok := frameID(lines.Text()); ok {
			delete(waiting, id)
		}
	}
	_ = stdin.Close()
	// Whatever the server says on the way out is part of the transcript
	// too, a line that is not a frame included.
	for lines.Scan() {
		keep()
	}

	err = cmd.Wait()
	switch {
	case ctx.Err() != nil:
		err = fmt.Errorf("the server did not exit within %s", smokeTimeout)
	case err == nil && lines.Err() != nil:
		err = fmt.Errorf("reading the server's stdout: %w", lines.Err())
	case err == nil && writeErr != nil:
		// A write that failed while the run still succeeded means the
		// server stopped reading early, which the transcript alone
		// would show as missing answers.
		err = fmt.Errorf("writing to the server: %w", writeErr)
	}
	if err != nil && logs.Len() > 0 {
		err = fmt.Errorf("%w; stderr: %s", err, abbrev(logs.String(), 2000))
	}
	return transcript.String(), err
}

// smokeEnv is the environment for a smoke run: this one, with every GCM_
// variable dropped.
//
// Dropped rather than added to. The gate's claim is that a server with
// no credentials answers with an [auth] error, and a maintainer who
// exports a client secret or a refresh token has a server that can
// answer for real — so the gate would fail on a machine that is fine, or
// pass for a reason it did not mean.
func smokeEnv(configDir string) []string {
	parent := os.Environ()
	env := make([]string, 0, len(parent)+3)
	for _, kv := range parent {
		if !strings.HasPrefix(kv, config.EnvPrefix) {
			env = append(env, kv)
		}
	}
	return append(env,
		userconfig.EnvDir+"="+configDir,
		// A temporary directory is outside the home directory on most
		// platforms and inside it on Windows. BaseDir refuses the first
		// for a real override; this opts past the question entirely.
		userconfig.EnvAllowOutsideHome+"=1",
		config.EnvPrefix+"LOG_LEVEL="+string(config.LogError),
	)
}

// abbrev cuts s to about n bytes, on a rune boundary, so a message
// carrying a 140 KB tools/list response stays readable.
func abbrev(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for i := range s {
		if i >= n {
			return fmt.Sprintf("%s… (%d bytes)", s[:i], len(s))
		}
	}
	return s
}
