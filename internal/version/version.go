// Package version exposes the build-time version string. Set with
// -ldflags "-X github.com/mmedum/google-chat-mcp/v2/internal/version.Version=v1.2.3".
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Version is the semantic version of the binary. Releases set it through
// ldflags; a local build leaves it "dev".
var Version = "dev"

// String is the version to report. `go install module@v1.2.3` applies no
// ldflags, so a binary installed the way the README suggests would call
// itself "dev" forever; Go records the module version it was built from,
// which is the honest answer in that case.
func String() string {
	if Version != "dev" {
		return canonical(Version)
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return canonical(v)
		}
	}
	return Version
}

// canonical is the one spelling of a release, whichever way the binary
// was built.
//
// There are two sources and they disagreed. goreleaser stamps Version
// with its own {{.Version}}, which has the leading v stripped, so a
// release archive said "1.1.0"; `go install` stamps nothing and the
// fallback reads the module version out of the build info, which is
// "v1.1.0". The same release therefore reported two different strings
// depending on how somebody installed it, and anything parsing
// --version got a different answer per install method. Reported from
// outside, by a reader comparing five servers side by side.
//
// The v stays, because that is how the tag, the module version and the
// release are all named; "dev" and any other non-release string are
// left exactly as they are.
func canonical(v string) string {
	if v == "" || v == "dev" {
		return v
	}
	if v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
}

// Info returns a one-line description suitable for --version output.
func Info() string {
	return fmt.Sprintf("google-chat-mcp %s (%s %s/%s)", String(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
