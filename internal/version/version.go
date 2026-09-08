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
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return Version
}

// Info returns a one-line description suitable for --version output.
func Info() string {
	return fmt.Sprintf("google-chat-mcp %s (%s %s/%s)", String(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
