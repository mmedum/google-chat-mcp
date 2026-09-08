package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// module is this repository's import path, which prefixes every block in
// a coverage profile.
const module = "github.com/mmedum/google-chat-mcp/v2"

// defaultFloor is the statement coverage every package has to clear.
const defaultFloor = 80.0

// exempt is the one package with nothing worth covering: internal/version
// holds build stamps.
//
// An exception that is not in this list and cannot be, so it is written
// down instead: a package whose files are all behind a build tag has no
// files in a default build, so `go list` does not report it and the floor
// never sees it. internal/evals and internal/livecheck are those.
var exempt = map[string]bool{"internal/version": true}

// floors are the packages held to something other than defaultFloor.
//
// A floor of its own, not an exemption, and the difference is the point:
// an exempt package is invisible, and invisible is how cmd/ sat at 32%
// through a release. This one is printed on every run.
//
// What is left uncovered in cmd/ is the loopback OAuth flow, the serve
// loop, the live doctor walk and the userinfo lookup, each needing a
// network or process seam the shipped code does not have. They are
// covered by the stdio smoke test and by live runs instead. The number
// is set just under what the package holds, so it ratchets: it may go up
// and may not go down. Raising it means adding those seams, which is a
// change to shipped code and wants its own review.
var floors = map[string]float64{"cmd/google-chat-mcp": 55.0}

// coverage enforces the statement floor per package.
//
// The profile is built with -coverpkg over both trees that ship, so a
// block appears once per test binary that touched it and is
// de-duplicated here.
func coverage(args []string, stdout, stderr io.Writer) int {
	profile := "cov.out"
	if len(args) > 1 && args[1] != "" {
		profile = args[1]
	}

	blocks, err := readProfile(profile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}

	// Derived, not listed: a package added under cmd/ or internal/ is
	// under the floor from its first commit. A hand-kept list lets a new
	// package escape it silently, which is the opposite of what a floor
	// is for.
	//
	// cmd/ is in the list because it was missing from it — the floor
	// read ./internal/... only, so the command package sat at 32%
	// against a floor every other package cleared, and nothing could
	// report it because it was outside -coverpkg as well. A derived list
	// is only as trustworthy as the root it derives from, and this one
	// derived from one of the two trees that ship.
	packages, err := listPackages()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gates: %v\n", err)
		return 1
	}

	failed := false
	for _, pkg := range packages {
		if exempt[pkg] {
			continue
		}
		floor, named := floors[pkg]
		if !named {
			floor = defaultFloor
		}
		pct := percent(blocks, module+"/"+pkg)
		if named {
			_, _ = fmt.Fprintf(stdout, "%-22s %6.1f%%  (floor %.0f%%)\n", pkg, pct, floor)
		} else {
			_, _ = fmt.Fprintf(stdout, "%-22s %6.1f%%\n", pkg, pct)
		}
		if pct < floor {
			failed = true
		}
	}
	if failed {
		_, _ = fmt.Fprintln(stderr, "gates: coverage below its floor in at least one package; "+
			"each is printed above with the floor it has to clear")
		return 1
	}
	return 0
}

// block is one statement block in a coverage profile.
type block struct {
	dir       string // the package directory the block's file sits in
	id        string // file:range, which identifies the block across binaries
	statement int
	covered   bool
}

// readProfile parses a coverage profile. The first line is the mode.
func readProfile(path string) ([]block, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read the coverage profile: %w", err)
	}
	defer func() { _ = f.Close() }()

	var out []block
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if line == 1 || strings.TrimSpace(text) == "" {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 3 {
			return nil, fmt.Errorf("%s:%d: expected three fields, got %d", path, line, len(fields))
		}
		file, _, ok := strings.Cut(fields[0], ":")
		if !ok {
			return nil, fmt.Errorf("%s:%d: no file in %q", path, line, fields[0])
		}
		count, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: statement count %q: %w", path, line, fields[1], err)
		}
		hits, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: hit count %q: %w", path, line, fields[2], err)
		}
		out = append(out, block{
			dir: path2dir(file), id: fields[0], statement: count, covered: hits > 0,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no coverage blocks: this gate would be looking at nothing", path)
	}
	return out, nil
}

// path2dir is the directory part of a profile's file path.
func path2dir(file string) string {
	if i := strings.LastIndex(file, "/"); i >= 0 {
		return file[:i]
	}
	return file
}

// percent is the statement coverage of one package.
//
// The directory is compared exactly rather than by prefix. Matching on a
// prefix scores a package on its subpackages too, so the printed number
// describes neither — google-sheets-mcp had a package reported at 68.7%
// whose own coverage was 90.1%, once a subpackage grew. No package here
// has a child today, which is exactly why that would have gone unnoticed
// until one did.
//
// A block appears once per test binary that touched it, so it is counted
// once and is covered if any binary covered it.
func percent(blocks []block, dir string) float64 {
	statements := map[string]int{}
	hit := map[string]bool{}
	for _, b := range blocks {
		if b.dir != dir {
			continue
		}
		statements[b.id] = b.statement
		if b.covered {
			hit[b.id] = true
		}
	}
	total, covered := 0, 0
	for id, n := range statements {
		total += n
		if hit[id] {
			covered += n
		}
	}
	if total == 0 {
		return 0
	}
	return 100 * float64(covered) / float64(total)
}

// listPackages asks the toolchain which packages ship, so the floor
// covers a new one from its first commit.
func listPackages() ([]string, error) {
	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}}", "./cmd/...", "./internal/...").Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			pkgs = append(pkgs, strings.TrimPrefix(line, module+"/"))
		}
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("go list named no packages: this gate would be looking at nothing")
	}
	sort.Strings(pkgs)
	return pkgs, nil
}
