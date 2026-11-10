package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolateGit keeps the machine's git configuration from every git the
// test runs, the gates' own included, so a personal excludes file or hook
// cannot change what a gate sees, and gives a commit an author.
//
// It also turns automatic maintenance off. A commit starts it detached, and
// it holds .git/objects/maintenance.lock after git returns, so a copy of the
// repository races it (https://github.com/git/git/blob/v2.51.0/builtin/gc.c#L1663).
func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"XDG_CONFIG_HOME": t.TempDir(), "GIT_AUTHOR_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@example.com",
		"GIT_COMMITTER_NAME": "Fixture", "GIT_COMMITTER_EMAIL": "fixture@example.com",
		// Command-scope pairs, which every config file yields to:
		// https://git-scm.com/docs/git-config#Documentation/git-config.txt-GITCONFIGCOUNT
		"GIT_CONFIG_COUNT": "3",
		// No command starts git maintenance run --auto after a write:
		// https://git-scm.com/docs/git-maintenance#Documentation/git-maintenance.txt-maintenanceauto
		"GIT_CONFIG_KEY_0": "maintenance.auto", "GIT_CONFIG_VALUE_0": "false",
		// A porcelain command that runs git gc --auto itself finds no work:
		// https://git-scm.com/docs/git-gc#Documentation/git-gc.txt-gcauto
		"GIT_CONFIG_KEY_1": "gc.auto", "GIT_CONFIG_VALUE_1": "0",
		// A fixture commit is never signed, whatever key the machine has.
		"GIT_CONFIG_KEY_2": "commit.gpgsign", "GIT_CONFIG_VALUE_2": "false"} {
		t.Setenv(k, v)
	}
}

// applyEdits applies edits to files in place: an edit with no old text
// writes the file, one with neither removes it, and one whose old text the
// file does not hold fails the test.
func applyEdits(t *testing.T, files map[string]string, edits ...docsEdit) map[string]string {
	t.Helper()
	for _, e := range edits {
		switch body := files[e.file]; {
		case e.old == "" && e.new == "":
			delete(files, e.file)
		case e.old == "":
			files[e.file] = e.new
		case !strings.Contains(body, e.old):
			t.Fatalf("the fixture's %s does not contain %q", e.file, e.old)
		default:
			files[e.file] = strings.Replace(body, e.old, e.new, 1)
		}
	}
	return files
}

// putAll writes files under root.
func putAll(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		put(t, root, rel, body)
	}
}

// docsTree writes files to a fresh directory and returns it.
func docsTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	putAll(t, root, files)
	return root
}

// repoPath is a slash-separated path of this repository, from its root.
func repoPath(t *testing.T, path string) string {
	t.Helper()
	return filepath.Join(parityRepoRoot, filepath.FromSlash(path))
}

// repoFile is one of this repository's files.
func repoFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(repoPath(t, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
