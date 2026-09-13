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

// TestOneSpellingWhicheverWayItWasBuilt is the drift a reader found by
// comparing five servers side by side: goreleaser stamps its own
// {{.Version}}, which has the leading v stripped, while `go install`
// leaves the fallback to read "v1.1.0" out of the build info. The same
// release reported two different strings depending on how it was
// installed, and the README promises it "reports the release it came
// from either way".
func TestOneSpellingWhicheverWayItWasBuilt(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1.1.0", "v1.1.0"},  // goreleaser's stamping
		{"v1.1.0", "v1.1.0"}, // the build-info fallback
		{"1.1.0-rc.1", "v1.1.0-rc.1"},
		{"dev", "dev"}, // an untagged build says so
		{"", ""},
	} {
		if got := canonical(tc.in); got != tc.want {
			t.Errorf("canonical(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
