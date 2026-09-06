package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

type memKeyring struct {
	data map[string]string
	fail error
}

func (m *memKeyring) key(s, a string) string { return s + "/" + a }
func (m *memKeyring) Get(s, a string) (string, error) {
	if m.fail != nil {
		return "", m.fail
	}
	v, ok := m.data[m.key(s, a)]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}
func (m *memKeyring) Set(s, a, v string) error {
	if m.fail != nil {
		return m.fail
	}
	m.data[m.key(s, a)] = v
	return nil
}
func (m *memKeyring) Delete(s, a string) error {
	if m.fail != nil {
		return m.fail
	}
	if _, ok := m.data[m.key(s, a)]; !ok {
		return keyring.ErrNotFound
	}
	delete(m.data, m.key(s, a))
	return nil
}

func newStore(t *testing.T, kr Backend) (*Store, *[]string) {
	t.Helper()
	var warnings []string
	s := &Store{
		Profile:  "default",
		Keyring:  kr,
		FilePath: filepath.Join(t.TempDir(), "token.json"),
		Env:      func(string) string { return "" },
		Warn:     func(m string) { warnings = append(warnings, m) },
	}
	return s, &warnings
}

func TestEnvWins(t *testing.T) {
	s, _ := newStore(t, &memKeyring{data: map[string]string{"google-chat-mcp/default": "from-keyring"}})
	s.Env = func(k string) string {
		if k == EnvVar {
			return "from-env"
		}
		return ""
	}
	tok, src, err := s.Resolve()
	if err != nil || tok != "from-env" || src != SourceEnv {
		t.Fatalf("got %q %q %v", tok, src, err)
	}
}

func TestKeyringRoundTrip(t *testing.T) {
	kr := &memKeyring{data: map[string]string{}}
	s, warnings := newStore(t, kr)
	if _, _, err := s.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty store: %v", err)
	}
	src, err := s.Save("tok-1")
	if err != nil || src != SourceKeyring {
		t.Fatalf("save: %v %v", src, err)
	}
	tok, src, err := s.Resolve()
	if err != nil || tok != "tok-1" || src != SourceKeyring {
		t.Fatalf("resolve: %q %v %v", tok, src, err)
	}
	if len(*warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", *warnings)
	}
	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("double delete: %v", err)
	}
}

func TestFileFallbackWhenKeyringBroken(t *testing.T) {
	kr := &memKeyring{data: map[string]string{}, fail: errors.New("no session bus")}
	s, warnings := newStore(t, kr)
	src, err := s.Save("tok-2")
	if err != nil || src != SourceFile {
		t.Fatalf("save: %v %v", src, err)
	}
	if len(*warnings) != 1 || !strings.Contains((*warnings)[0], "plaintext") {
		t.Fatalf("expected plaintext warning, got %v", *warnings)
	}
	st, err := os.Stat(s.FilePath)
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	// Windows has no POSIX permission bits, so the 0600 the store asks
	// for cannot be asserted there; the file's ACL is what protects it.
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode: %v", st.Mode().Perm())
	}
	tok, src, err := s.Resolve()
	if err != nil || tok != "tok-2" || src != SourceFile {
		t.Fatalf("resolve: %q %v %v", tok, src, err)
	}
	if len(*warnings) != 2 {
		t.Fatalf("resolve should warn about the broken keyring: %v", *warnings)
	}
	if err := s.Delete(); err == nil {
		t.Fatal("delete with broken keyring should report the keyring error")
	}
	if _, err := os.Stat(s.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file should still be removed")
	}
}

func TestKeyringSaveRemovesStaleFile(t *testing.T) {
	kr := &memKeyring{data: map[string]string{}}
	s, _ := newStore(t, kr)
	if err := os.WriteFile(s.FilePath, []byte(`{"refresh_token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save("new"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale plaintext file should be removed after a keyring save")
	}
}

func TestBrokenKeyringNoFileReportsError(t *testing.T) {
	kr := &memKeyring{fail: errors.New("dbus down")}
	s, _ := newStore(t, kr)
	_, _, err := s.Resolve()
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "dbus down") {
		t.Fatalf("got %v", err)
	}
	s.FilePath = ""
	if _, err := s.Save("x"); err == nil || !strings.Contains(err.Error(), "no file fallback") {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Save(""); err == nil {
		t.Fatal("empty token must be refused")
	}
}

func TestCorruptFile(t *testing.T) {
	s, _ := newStore(t, nil)
	if err := os.WriteFile(s.FilePath, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve(); err == nil {
		t.Fatal("corrupt token file should error")
	}
}

func TestOSKeyringConstructor(t *testing.T) {
	if OSKeyring() == nil || !IsKeyringNotFound(keyring.ErrNotFound) || IsKeyringNotFound(errors.New("x")) {
		t.Fatal("helpers wrong")
	}
}
