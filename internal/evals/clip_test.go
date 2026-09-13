//go:build evals

package evals

import (
	"strings"
	"testing"
)

// TestClipMasksBeforeItTruncates holds what the transcript gate cannot.
//
// The gate says a value reached the transcript through clip. It cannot
// say clip still redacts — gut the body and the gate stays green, which
// is exactly how this function was wrong before: it only truncated, and
// truncating reads as safe. The first 300 characters of a tool response
// are where an address is, not a place it is absent from.
//
// Masking before truncating is the other half: cut first and an address
// can be halved, leaving the pattern nothing to match.
func TestClipMasksBeforeItTruncates(t *testing.T) {
	const line = "the caller ase.fiktivsen@example.com was refused"
	got := clip(line, 400)
	if strings.Contains(got, "ase.fiktivsen@example.com") {
		t.Errorf("clip left the address in: %s", got)
	}
	if !strings.Contains(got, "…@example.com") {
		t.Errorf("clip should keep the domain: %s", got)
	}
	// Truncation still happens, and the address is masked even when the
	// cut would have landed inside it.
	long := strings.Repeat("x", 30) + " ase.fiktivsen@example.com " + strings.Repeat("y", 400)
	if cut := clip(long, 60); strings.Contains(cut, "ase.fiktivsen") {
		t.Errorf("an address survived a cut that crossed it: %s", cut)
	}
}
