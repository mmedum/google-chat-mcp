// Package credentials stores and resolves the OAuth refresh token.
//
// Resolution order, the same one gh uses:
//  1. GCM_REFRESH_TOKEN in the environment (CI, automation).
//  2. The OS keyring (Secret Service on Linux, Keychain on macOS,
//     Credential Manager on Windows), account = profile name.
//  3. A 0600 file under the profile directory, written only when the
//     keyring was unavailable at login time; the caller is warned.
//
// A missing keyring entry falls through; a broken keyring (no session
// bus, no secret service) also falls through to the file so a headless
// machine still works, and the underlying error is reported if nothing
// else is found.
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"
)

// ServiceName is the keyring service identifier.
const ServiceName = "google-chat-mcp"

// EnvVar is the environment override.
const EnvVar = "GCM_REFRESH_TOKEN"

// Source identifies where a token came from.
type Source string

// Source values.
const (
	SourceEnv     Source = "env"
	SourceKeyring Source = "keyring"
	SourceFile    Source = "file"
)

// ErrNotFound means no token is stored anywhere.
var ErrNotFound = errors.New("credentials: no refresh token found; run `google-chat-mcp login`")

// Backend is the keyring contract. Tests substitute an in-memory one.
type Backend interface {
	Get(service, account string) (string, error)
	Set(service, account, secret string) error
	Delete(service, account string) error
}

type osKeyring struct{}

func (osKeyring) Get(service, account string) (string, error) { return keyring.Get(service, account) }
func (osKeyring) Set(service, account, secret string) error {
	return keyring.Set(service, account, secret)
}
func (osKeyring) Delete(service, account string) error { return keyring.Delete(service, account) }

// OSKeyring returns the production keyring backend.
func OSKeyring() Backend { return osKeyring{} }

// IsKeyringNotFound reports whether err is the keyring's "no entry" error.
func IsKeyringNotFound(err error) bool { return errors.Is(err, keyring.ErrNotFound) }

// Store resolves and saves the refresh token for one profile.
type Store struct {
	Profile  string
	Keyring  Backend
	FilePath string
	Env      func(string) string
	// Warn receives human-readable warnings (plaintext fallback in use).
	Warn func(string)
}

type tokenFile struct {
	RefreshToken string    `json:"refresh_token"`
	SavedAt      time.Time `json:"saved_at"`
}

func (s *Store) warn(msg string) {
	if s.Warn != nil {
		s.Warn(msg)
	}
}

func (s *Store) env(k string) string {
	if s.Env != nil {
		return s.Env(k)
	}
	return os.Getenv(k)
}

// Resolve returns the refresh token and where it came from.
func (s *Store) Resolve() (string, Source, error) {
	if v := s.env(EnvVar); v != "" {
		return v, SourceEnv, nil
	}
	return s.ResolveStored()
}

// ResolveStored returns the token from the keyring or the file, ignoring
// the environment override: the token logout can revoke and delete.
func (s *Store) ResolveStored() (string, Source, error) {
	var keyringErr error
	if s.Keyring != nil {
		tok, err := s.Keyring.Get(ServiceName, s.Profile)
		switch {
		case err == nil && tok != "":
			return tok, SourceKeyring, nil
		case err != nil && !IsKeyringNotFound(err):
			keyringErr = err
		}
	}
	if s.FilePath != "" {
		tok, err := s.readFile()
		if err == nil && tok != "" {
			if keyringErr != nil {
				s.warn(fmt.Sprintf("keyring unavailable (%v); using plaintext token file %s", keyringErr, s.FilePath))
			}
			return tok, SourceFile, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
	}
	if keyringErr != nil {
		return "", "", fmt.Errorf("%w (keyring error: %w)", ErrNotFound, keyringErr)
	}
	return "", "", ErrNotFound
}

// Save stores the token in the keyring, or in the file when the keyring
// fails. It returns where the token ended up.
func (s *Store) Save(token string) (Source, error) {
	if token == "" {
		return "", errors.New("credentials: refusing to store an empty token")
	}
	var keyringErr error
	if s.Keyring != nil {
		if err := s.Keyring.Set(ServiceName, s.Profile, token); err == nil {
			// Remove a stale plaintext copy so there is one source of truth.
			_ = s.removeFile()
			return SourceKeyring, nil
		} else {
			keyringErr = err
		}
	}
	if s.FilePath == "" {
		if keyringErr != nil {
			return "", fmt.Errorf("credentials: keyring unavailable and no file fallback configured: %w", keyringErr)
		}
		return "", errors.New("credentials: no token store configured")
	}
	if err := s.writeFile(token); err != nil {
		return "", err
	}
	if keyringErr != nil {
		s.warn(fmt.Sprintf("keyring unavailable (%v); refresh token saved in plaintext at %s (mode 0600)", keyringErr, s.FilePath))
	}
	return SourceFile, nil
}

// Delete removes the token from every store. Missing entries are fine.
func (s *Store) Delete() error {
	var errs []error
	if s.Keyring != nil {
		if err := s.Keyring.Delete(ServiceName, s.Profile); err != nil && !IsKeyringNotFound(err) {
			errs = append(errs, fmt.Errorf("credentials: keyring delete: %w", err))
		}
	}
	if err := s.removeFile(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Store) readFile() (string, error) {
	data, err := os.ReadFile(s.FilePath)
	if err != nil {
		return "", err
	}
	var tf tokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return "", fmt.Errorf("credentials: parse %s: %w", s.FilePath, err)
	}
	return tf.RefreshToken, nil
}

func (s *Store) writeFile(token string) error {
	if err := os.MkdirAll(filepath.Dir(s.FilePath), 0o700); err != nil {
		return fmt.Errorf("credentials: create %s: %w", filepath.Dir(s.FilePath), err)
	}
	data, err := json.Marshal(tokenFile{RefreshToken: token, SavedAt: time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("credentials: encode token file: %w", err)
	}
	tmp := s.FilePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("credentials: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.FilePath); err != nil {
		return fmt.Errorf("credentials: replace %s: %w", s.FilePath, err)
	}
	return nil
}

func (s *Store) removeFile() error {
	if s.FilePath == "" {
		return nil
	}
	if err := os.Remove(s.FilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("credentials: remove %s: %w", s.FilePath, err)
	}
	return nil
}
