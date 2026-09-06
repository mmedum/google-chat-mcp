package userconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// tempBase points the package at a throwaway directory. A test dir sits
// outside the home directory, so it opts past the guard BaseDir applies
// to a real override. No test ever touches the caller's own config.
// setHome points os.UserHomeDir() at dir. It reads HOME on unix and
// USERPROFILE on Windows, so setting only one leaves the other platform
// reading the real account's home — which is where the temporary
// directory lives, so a test meant to sit outside home sat inside it and
// passed for the wrong reason.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func tempBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv(EnvDir, base)
	t.Setenv(EnvAllowOutsideHome, "1")
	return base
}

func TestPathsAndRoundTrip(t *testing.T) {
	base := tempBase(t)

	if d, _ := ProfileDir(""); d != base {
		t.Fatalf("default profile dir = %s", d)
	}
	if d, _ := ProfileDir("work"); d != filepath.Join(base, "profiles", "work") {
		t.Fatalf("named profile dir = %s", d)
	}
	if p, _ := DefaultClientSecretPath("default"); p != filepath.Join(base, "client_secret.json") {
		t.Fatalf("client secret path = %s", p)
	}
	if p, _ := TokenFilePath("work"); p != filepath.Join(base, "profiles", "work", "token.json") {
		t.Fatalf("token path = %s", p)
	}

	if _, err := Load("work"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load before Save = %v", err)
	}
	in := Config{ClientSecretPath: "/x/secret.json", AccountEmail: "a@b.test", TokenStore: "keyring", Scopes: []string{"s1"}}
	if err := Save("work", in); err != nil {
		t.Fatal(err)
	}
	out, err := Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if out.ClientSecretPath != in.ClientSecretPath || out.AccountEmail != in.AccountEmail || out.TokenStore != "keyring" || len(out.Scopes) != 1 || out.UpdatedAt.IsZero() {
		t.Fatalf("round trip lost data: %+v", out)
	}
	p, _ := Path("work")
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX permission bits, so the 0600 the writer asks
	// for cannot be asserted there; the file's ACL is what protects it.
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("work"); err == nil {
		t.Fatal("corrupt file should error")
	}
	if err := Remove("work"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("work"); err != nil {
		t.Fatalf("second remove should be fine: %v", err)
	}
}

func TestBaseDirFallsBackToUserConfigDir(t *testing.T) {
	t.Setenv(EnvDir, "")
	d, err := BaseDir()
	if err != nil {
		t.Skip("no user config dir on this system")
	}
	if filepath.Base(d) != AppDir {
		t.Fatalf("base dir = %s", d)
	}
}

// The override is checked against the home directory because this
// package creates directories 0700 and writes 0600 files. A mistyped
// value pointed at something sensitive would re-permission it.
func TestBaseDirRefusesAPathOutsideHome(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv(EnvDir, t.TempDir()) // a sibling of home, not under it
	t.Setenv(EnvAllowOutsideHome, "")

	if _, err := BaseDir(); !errors.Is(err, ErrOutsideHome) {
		t.Fatalf("err = %v, want ErrOutsideHome", err)
	}
}

func TestBaseDirAllowsAPathOutsideHomeWithTheOptIn(t *testing.T) {
	outside := t.TempDir()
	setHome(t, t.TempDir())
	t.Setenv(EnvDir, outside)
	t.Setenv(EnvAllowOutsideHome, "1")

	got, err := BaseDir()
	if err != nil {
		t.Fatalf("BaseDir: %v", err)
	}
	if got != outside {
		t.Errorf("BaseDir = %q, want %q", got, outside)
	}
}

func TestBaseDirAllowsAPathUnderHome(t *testing.T) {
	home := t.TempDir()
	under := filepath.Join(home, ".config", AppDir)
	setHome(t, home)
	t.Setenv(EnvDir, under)
	t.Setenv(EnvAllowOutsideHome, "")

	got, err := BaseDir()
	if err != nil {
		t.Fatalf("a path under home must be allowed: %v", err)
	}
	if got != under {
		t.Errorf("BaseDir = %q, want %q", got, under)
	}
}

// The bug this holds: home and the override are two names for one
// place, and comparing the names refused a directory plainly inside it.
// Windows is where it showed up — an 8.3 short name against the long one
// — but a linked home does it on any system, which is what this builds.
func TestBaseDirAllowsAPathUnderALinkedHome(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	setHome(t, link)
	t.Setenv(EnvDir, filepath.Join(real, ".config", AppDir))
	t.Setenv(EnvAllowOutsideHome, "")

	if _, err := BaseDir(); err != nil {
		t.Errorf("a directory under home, named through the link, must be allowed: %v", err)
	}
}

// A config directory that is not there yet is the ordinary case: the
// first login creates it. Resolving has to reach past the missing part
// rather than give up and compare the unresolved name.
func TestRealPathResolvesPastWhatIsNotThereYet(t *testing.T) {
	// Resolved with the standard library rather than with realPath, so
	// the expectation does not come from the code under test. The
	// temporary directory needs it: it is handed out under a link on
	// macOS and under a short name on Windows, so its own path is
	// already one of the two spellings this is about.
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the temp dir: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	got := realPath(filepath.Join(link, "not", "created", "yet"))
	if want := filepath.Join(real, "not", "created", "yet"); got != want {
		t.Errorf("realPath = %q, want %q", got, want)
	}
	if got := realPath(filepath.Join(real, "plain")); got != filepath.Join(real, "plain") {
		t.Errorf("a path with nothing to resolve came back as %q", got)
	}
}

func TestWithinDir(t *testing.T) {
	for _, tc := range []struct {
		dir, path string
		want      bool
	}{
		{"/home/user", "/home/user", true},
		{"/home/user", "/home/user/.config/app", true},
		{"/home/user", "/home/other", false},
		{"/home/user", "/etc", false},
		{"/home/user", "/home/user/../other", false},
	} {
		if got := withinDir(tc.dir, tc.path); got != tc.want {
			t.Errorf("withinDir(%q, %q) = %v, want %v", tc.dir, tc.path, got, tc.want)
		}
	}
}

// Save then Load must round-trip every field, and the file must be
// readable only by its owner: it records which account is logged in.
func TestSaveRoundTripsAndIsOwnerOnly(t *testing.T) {
	tempBase(t)

	want := Config{
		ClientSecretPath: "/path/to/client_secret.json",
		AccountEmail:     "janedoe@example.com",
		TokenStore:       "keyring",
		Scopes:           []string{"openid", "email"},
	}
	if err := Save("default", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load("default")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AccountEmail != want.AccountEmail || got.TokenStore != want.TokenStore ||
		got.ClientSecretPath != want.ClientSecretPath || len(got.Scopes) != 2 {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("Save must stamp UpdatedAt")
	}

	p, err := Path("default")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("config file mode = %o, want 600", perm)
	}
}

// A half-written file must fail loudly rather than read as an empty
// profile, which would look like "never logged in".
func TestLoadRejectsAMalformedFile(t *testing.T) {
	tempBase(t)

	p, err := Path("default")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("default"); err == nil {
		t.Fatal("a malformed config must not read as an empty one")
	}
}

func TestProfileDirSeparatesNamedProfiles(t *testing.T) {
	base := tempBase(t)

	def, err := ProfileDir(DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if def != base {
		t.Errorf("default profile dir = %q, want the base %q", def, base)
	}
	named, err := ProfileDir("work")
	if err != nil {
		t.Fatal(err)
	}
	if named == base || !strings.Contains(named, "work") {
		t.Errorf("named profile dir = %q", named)
	}
	empty, err := ProfileDir("")
	if err != nil {
		t.Fatal(err)
	}
	if empty != base {
		t.Errorf("empty profile dir = %q, want the base", empty)
	}
}

func TestDerivedPaths(t *testing.T) {
	base := tempBase(t)

	for name, fn := range map[string]func(string) (string, error){
		"Path":                    Path,
		"DefaultClientSecretPath": DefaultClientSecretPath,
		"TokenFilePath":           TokenFilePath,
	} {
		got, err := fn("default")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.HasPrefix(got, base) {
			t.Errorf("%s = %q, want it under %q", name, got, base)
		}
	}
}

// With no home and no XDG override there is nowhere to put the profile.
// Every path helper has to report that rather than guess a location and
// write a token into it.
func TestPathHelpersReportAMissingConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows resolves the config dir from a different variable")
	}
	t.Setenv(EnvDir, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	if _, err := BaseDir(); err == nil {
		t.Fatal("BaseDir must fail with no home directory")
	}
	for name, fn := range map[string]func(string) (string, error){
		"ProfileDir":              ProfileDir,
		"Path":                    Path,
		"DefaultClientSecretPath": DefaultClientSecretPath,
		"TokenFilePath":           TokenFilePath,
		"Load": func(p string) (string, error) {
			_, err := Load(p)
			return "", err
		},
	} {
		if _, err := fn("default"); err == nil {
			t.Errorf("%s must fail with no home directory", name)
		}
	}
	if err := Save("default", Config{}); err == nil {
		t.Error("Save must fail with no home directory")
	}
	if err := Remove("default"); err == nil {
		t.Error("Remove must fail with no home directory")
	}
}

// A named profile keeps its own files, so two accounts never share a
// token or overwrite each other's settings.
func TestNamedProfilesAreIndependent(t *testing.T) {
	tempBase(t)

	if err := Save("work", Config{AccountEmail: "jane@example.com"}); err != nil {
		t.Fatalf("Save work: %v", err)
	}
	if err := Save("personal", Config{AccountEmail: "john@example.com"}); err != nil {
		t.Fatalf("Save personal: %v", err)
	}
	work, err := Load("work")
	if err != nil {
		t.Fatalf("Load work: %v", err)
	}
	personal, err := Load("personal")
	if err != nil {
		t.Fatalf("Load personal: %v", err)
	}
	if work.AccountEmail == personal.AccountEmail {
		t.Errorf("profiles share state: %q and %q", work.AccountEmail, personal.AccountEmail)
	}

	// Removing one leaves the other alone.
	if err := Remove("work"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := Load("work"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed profile still loads: %v", err)
	}
	if _, err := Load("personal"); err != nil {
		t.Errorf("removing one profile disturbed another: %v", err)
	}
}

// The email cache sits beside the token and the profile file, so it
// follows the same per-profile layout.
func TestDirectoryCachePath(t *testing.T) {
	base := tempBase(t)

	got, err := DirectoryCachePath(DefaultProfile)
	if err != nil {
		t.Fatalf("DirectoryCachePath: %v", err)
	}
	if want := filepath.Join(base, "directory-cache.json"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}

	got, err = DirectoryCachePath("work")
	if err != nil {
		t.Fatalf("DirectoryCachePath: %v", err)
	}
	if want := filepath.Join(base, "profiles", "work", "directory-cache.json"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}
