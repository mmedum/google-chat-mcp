package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/mmedum/google-chat-mcp/v2/internal/config"
	"github.com/mmedum/google-chat-mcp/v2/internal/redact"
	"github.com/mmedum/google-chat-mcp/v2/internal/scopes"
	"github.com/mmedum/google-chat-mcp/v2/internal/userconfig"
	"github.com/mmedum/google-chat-mcp/v2/internal/version"
)

// statusSchemaVersion is the version of the JSON object `status --json`
// prints. A caller may branch on it; it changes only when a field is
// removed or its meaning changes, never when one is added.
const statusSchemaVersion = 1

// statusReport is everything `status` knows, collected once and rendered
// either as the lines a person reads or as the object a script parses.
//
// One collector, two renderers, because the alternative drifts. The text
// answers "is this authorised" with a label, and a label is free to be
// reworded in any release; the object below is the part that is promised
// not to move.
//
// Nothing here contacts Google.
type statusReport struct {
	SchemaVersion int     `json:"schema_version"`
	Binary        string  `json:"binary"`
	Version       string  `json:"version"`
	Profile       string  `json:"profile"`
	ConfigDir     *string `json:"config_dir"`
	// Account is masked to the domain, and masked HERE: the JSON encoder
	// writes straight to the stream, so a collector that kept the full
	// address would put it on stdout.
	Account     *string           `json:"account"`
	Credentials statusCredentials `json:"credentials"`
	Scopes      statusScopes      `json:"scopes"`
	Settings    statusSettings    `json:"settings"`
}

type statusCredentials struct {
	// SignedIn is false when no profile has been written at all — the
	// state the text output reports by printing "not signed in" and
	// stopping. A truncated object would leave a caller unable to tell
	// that from a parse failure, so every field below is always present.
	SignedIn bool `json:"signed_in"`
	// Resolved is the field worth branching on: true means a refresh
	// token was found, false means every tool answers [auth] until
	// `login` succeeds.
	Resolved         bool    `json:"resolved"`
	TokenStore       *string `json:"token_store"`
	Reason           *string `json:"reason"`
	ClientSecretPath *string `json:"client_secret_path"`
}

// statusScopes carries what the last login granted and what this
// configuration still needs. Missing is the commonest reason a working
// setup starts refusing one tool.
type statusScopes struct {
	Granted []string `json:"granted"`
	Missing []string `json:"missing"`
}

type statusSettings struct {
	ReadOnly    bool     `json:"read_only"`
	Destructive bool     `json:"destructive"`
	Toolsets    []string `json:"toolsets"`
	LocalDir    *string  `json:"local_dir"`
	ChatAPIBase string   `json:"chat_api_base"`
	LogLevel    string   `json:"log_level"`
	LogFormat   string   `json:"log_format"`
}

// newStatusReport collects the state. The error it returns is a real
// failure to read the configuration, not an unauthorised account: not
// being signed in is a state this reports, not a reason to stop.
func newStatusReport(cfg config.Config) (statusReport, error) {
	r := statusReport{
		SchemaVersion: statusSchemaVersion,
		Binary:        "google-chat-mcp",
		Version:       version.String(),
		Profile:       cfg.Profile,
		Scopes:        statusScopes{Granted: []string{}, Missing: []string{}},
		Settings: statusSettings{
			ReadOnly:    cfg.ReadOnly,
			Destructive: !cfg.RefuseDeletes,
			Toolsets:    toolsetList(cfg.Toolsets),
			LocalDir:    orNil(cfg.LocalDir),
			ChatAPIBase: cfg.ChatAPIBase,
			LogLevel:    string(cfg.LogLevel),
			LogFormat:   string(cfg.LogFormat),
		},
	}
	if dir, err := userconfig.ProfileDir(cfg.Profile); err == nil {
		r.ConfigDir = orNil(dir)
	}

	stored, err := userconfig.Load(cfg.Profile)
	if errors.Is(err, userconfig.ErrNotFound) {
		r.Credentials.Reason = orNil("no profile written yet; run `google-chat-mcp login --client-secret <path>`")
		return r, nil
	}
	if err != nil {
		return r, err
	}
	r.Credentials.SignedIn = true
	r.Account = orNil(redact.Account(stored.AccountEmail))
	r.Credentials.ClientSecretPath = orNil(stored.ClientSecretPath)
	r.Scopes.Granted = orEmpty(stored.Scopes)
	r.Scopes.Missing = orEmpty(scopes.Missing(stored.Scopes, cfg.Enabled(config.ToolsetAdmin)))

	store, err := credentialStore(cfg, nil)
	if err != nil {
		return r, err
	}
	if _, source, err := store.Resolve(); err == nil {
		r.Credentials.Resolved = true
		r.Credentials.TokenStore = orNil(string(source))
	} else {
		// Through the redactor: this error is formatted elsewhere and an
		// address can arrive inside one nothing here wrote.
		r.Credentials.Reason = orNil(redact.Accounts(err.Error()))
	}
	return r, nil
}

// writeText writes the same lines, in the same order, that `status` has
// always printed.
func (r statusReport) writeText(w io.Writer) {
	_, _ = fmt.Fprintln(w, version.Info())
	_, _ = fmt.Fprintf(w, "profile:        %s\n", r.Profile)
	if r.ConfigDir != nil {
		_, _ = fmt.Fprintf(w, "config dir:     %s\n", *r.ConfigDir)
	}
	if !r.Credentials.SignedIn {
		_, _ = fmt.Fprintln(w, "account:        not signed in")
		_, _ = fmt.Fprintln(w, "\nRun `google-chat-mcp login --client-secret <path>` to sign in.")
		return
	}
	_, _ = fmt.Fprintf(w, "account:        %s\n", cmp.Or(deref(r.Account), "(none)"))
	_, _ = fmt.Fprintf(w, "client secret:  %s\n", cmp.Or(deref(r.Credentials.ClientSecretPath), "(none)"))
	if r.Credentials.Resolved {
		_, _ = fmt.Fprintf(w, "token store:    %s\n", deref(r.Credentials.TokenStore))
	} else {
		_, _ = fmt.Fprintf(w, "token store:    none (%s)\n", deref(r.Credentials.Reason))
	}
	if len(r.Scopes.Granted) > 0 {
		_, _ = fmt.Fprintf(w, "scopes:         %s\n", strings.Join(r.Scopes.Granted, " "))
	}
	_, _ = fmt.Fprintf(w, "read-only:      %t\n", r.Settings.ReadOnly)
	_, _ = fmt.Fprintf(w, "destructive:    %t\n", r.Settings.Destructive)
	_, _ = fmt.Fprintf(w, "toolsets:       %s\n", collapseToolsets(r.Settings.Toolsets))
	_, _ = fmt.Fprintf(w, "local dir:      %s\n", orUnset(deref(r.Settings.LocalDir)))
	_, _ = fmt.Fprintf(w, "chat api:       %s\n", r.Settings.ChatAPIBase)
	_, _ = fmt.Fprintf(w, "log:            %s %s\n", r.Settings.LogLevel, r.Settings.LogFormat)

	if len(r.Scopes.Missing) > 0 {
		_, _ = fmt.Fprintf(w, "\n%d scope(s) not granted; tools needing them report a [scope] error:\n", len(r.Scopes.Missing))
		for _, s := range r.Scopes.Missing {
			_, _ = fmt.Fprintf(w, "  %s\n", s)
		}
		_, _ = fmt.Fprintln(w, "\nAdd them to the consent screen, then run `google-chat-mcp login` again.")
	}
}

// writeJSON writes the object, indented and newline-terminated, so the
// whole of stdout is one JSON value.
func (r statusReport) writeJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// Paths are not HTML, and an escaped ampersand is a path a caller
	// cannot compare against its own.
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

// orNil turns an unset string into the JSON null that says so: an empty
// string is a value, and a caller cannot tell a value it does not
// recognise from one that is not there.
func orNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// orEmpty keeps a list a list; a nil slice marshals as null, and a
// caller counting it has to guard before it can count.
func orEmpty(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// toolsetList is the enabled toolsets by name, for the object. The text
// collapses a complete set to "all"; a caller reading JSON wants to know
// which ones, and can compare the list itself.
func toolsetList(ts []config.Toolset) []string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = string(t)
	}
	return names
}

// collapseToolsets reproduces the text output's "all", so the human
// lines are unchanged by the object having been added under them.
func collapseToolsets(names []string) string {
	if slices.Equal(names, toolsetNames()) {
		return "all"
	}
	return strings.Join(names, ", ")
}
