// Package redact keeps a person out of what leaves this process.
//
// It is the same package, with the same two functions, in all four of
// these servers: a session working on one should not have to discover
// that another already solved this, and the last round of drift produced
// four different answers to the same question.
package redact

import (
	"regexp"
	"strings"
)

// address matches an email anywhere in free text. The "…" is in the
// local-part class on purpose: an address masked once must still match,
// or a redactor running downstream of this one stops seeing it and the
// domain survives into an artifact. That happened.
var address = regexp.MustCompile(`[A-Za-z0-9._%+\-…]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// Account is an address with the local part removed and the domain kept.
//
// The domain is the half a diagnosis uses: shared drives are a Workspace
// feature and a personal account cannot create one, so @gmail.com and a
// Workspace domain are two different sets of behaviour to explain. The
// local part answers nothing — it is never an input to any command here.
func Account(addr string) string {
	local, domain, ok := strings.Cut(addr, "@")
	// Not an address: left alone rather than mangled, so a strange value
	// stays legible to whoever is debugging it.
	if !ok || local == "" || domain == "" {
		return addr
	}
	return "…@" + domain
}

// Accounts masks every address in text this server did not write.
//
// A 403 names the account it refused, and that message is repeated into
// an error string reaching stderr — which the MCP stdio transport says
// clients may capture and forward — and a tool response. Masked where
// the text is parsed rather than at each print, so a print added later
// is safe without its author knowing the rule, and because a writer
// wrapper could split an address across two Write calls and miss it.
func Accounts(s string) string { return address.ReplaceAllStringFunc(s, Account) }
