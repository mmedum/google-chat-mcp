package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

// TestStringFallsBackToBuildInfo covers the path a `go install` binary
// takes: no ldflags, so the module version Go recorded is the answer.
func TestStringFallsBackToBuildInfo(t *testing.T) {
	Version = "v1.2.3"
	if String() != "v1.2.3" {
		t.Fatalf("ldflags win: %s", String())
	}
	Version = "dev"
	got := String()
	want := "dev"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		want = info.Main.Version
	}
	if got != want {
		t.Fatalf("build info fallback: got %q want %q", got, want)
	}
	if !strings.Contains(Info(), got) {
		t.Fatalf("Info should carry the resolved version: %s", Info())
	}
}
