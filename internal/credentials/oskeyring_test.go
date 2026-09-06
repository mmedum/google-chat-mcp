package credentials

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

// The real keyring backend is exercised against go-keyring's in-memory
// mock so the wrapper methods are covered without a Secret Service.
func TestOSKeyringAgainstMock(t *testing.T) {
	keyring.MockInit()
	b := OSKeyring()
	if _, err := b.Get(ServiceName, "p"); !IsKeyringNotFound(err) {
		t.Fatalf("empty get: %v", err)
	}
	if err := b.Set(ServiceName, "p", "secret"); err != nil {
		t.Fatal(err)
	}
	if v, err := b.Get(ServiceName, "p"); err != nil || v != "secret" {
		t.Fatalf("get: %q %v", v, err)
	}
	if err := b.Delete(ServiceName, "p"); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ServiceName, "p"); !IsKeyringNotFound(err) {
		t.Fatalf("second delete: %v", err)
	}
	s := &Store{Profile: "p", Keyring: b, Env: func(string) string { return "" }}
	if _, _, err := s.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve without file path: %v", err)
	}
	if err := s.Delete(); err != nil {
		t.Fatalf("delete without file path: %v", err)
	}
}
