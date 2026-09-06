// Package userconfig stores non-secret, per-profile settings that survive
// between runs: where the OAuth client JSON lives, which account was
// logged in, and where the refresh token was stored. It lives at
// os.UserConfigDir()/google-chat-mcp (override with GCM_CONFIG_DIR);
// a non-default profile lives under profiles/<name>/ below that.
package userconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AppDir is the directory name under the user's config directory.
const AppDir = "google-chat-mcp"

// EnvDir overrides the base directory (tests, unusual setups).
const EnvDir = "GCM_CONFIG_DIR"

// DefaultProfile is the profile used when none is named.
const DefaultProfile = "default"

// ErrNotFound is returned when the profile has no config file yet.
var ErrNotFound = errors.New("userconfig: no config file for this profile; run `google-chat-mcp login`")

// Config is the stored, non-secret profile state.
type Config struct {
	ClientSecretPath string    `json:"client_secret_path,omitempty"`
	AccountEmail     string    `json:"account_email,omitempty"`
	TokenStore       string    `json:"token_store,omitempty"`
	Scopes           []string  `json:"scopes,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// EnvAllowOutsideHome opts an override out of the home-directory check.
const EnvAllowOutsideHome = "GCM_CONFIG_DIR_ALLOW_OUTSIDE_HOME"

// ErrOutsideHome is returned when the override points outside the home
// directory without the opt-in.
var ErrOutsideHome = errors.New("userconfig: config dir resolves outside the home directory")

// BaseDir returns the application config directory.
//
// The override is checked against the home directory because this
// package creates the directory 0700 and writes 0600 files into it. A
// mistyped value such as the SSH directory would re-permission
// something sensitive, so it is refused unless the caller says they
// meant it.
func BaseDir() (string, error) {
	v := os.Getenv(EnvDir)
	if v == "" {
		d, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("userconfig: locate user config dir: %w", err)
		}
		return filepath.Join(d, AppDir), nil
	}
	if os.Getenv(EnvAllowOutsideHome) == "1" {
		return v, nil
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		return "", fmt.Errorf("userconfig: resolve %s=%q: %w", EnvDir, v, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// With no home to compare against there is nothing to protect.
		return v, nil //nolint:nilerr // the override still stands
	}
	if !withinDir(home, abs) {
		return "", fmt.Errorf("%w: %s=%q. Set %s=1 to use it anyway",
			ErrOutsideHome, EnvDir, v, EnvAllowOutsideHome)
	}
	return v, nil
}

// withinDir reports whether path is dir or sits under it.
func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ProfileDir returns the directory holding one profile's files.
func ProfileDir(profile string) (string, error) {
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	if profile == "" || profile == DefaultProfile {
		return base, nil
	}
	return filepath.Join(base, "profiles", profile), nil
}

// Path returns the config file path for the profile.
func Path(profile string) (string, error) {
	dir, err := ProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// DefaultClientSecretPath is where `login` looks for the OAuth client JSON
// when no path is given.
func DefaultClientSecretPath(profile string) (string, error) {
	dir, err := ProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "client_secret.json"), nil
}

// TokenFilePath is the plaintext fallback location for the refresh token.
func TokenFilePath(profile string) (string, error) {
	dir, err := ProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "token.json"), nil
}

// DirectoryCachePath is where resolved email addresses are remembered
// between runs.
func DirectoryCachePath(profile string) (string, error) {
	dir, err := ProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "directory-cache.json"), nil
}

// Load reads the profile's config. ErrNotFound if absent.
func Load(profile string) (Config, error) {
	p, err := Path(profile)
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, fmt.Errorf("userconfig: read %s: %w", p, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("userconfig: parse %s: %w", p, err)
	}
	return c, nil
}

// Save writes the profile's config with owner-only permissions.
func Save(profile string, c Config) error {
	p, err := Path(profile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("userconfig: create %s: %w", filepath.Dir(p), err)
	}
	c.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("userconfig: encode: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("userconfig: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("userconfig: replace %s: %w", p, err)
	}
	return nil
}

// Remove deletes the profile's config file. Missing files are not an error.
func Remove(profile string) error {
	p, err := Path(profile)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("userconfig: remove %s: %w", p, err)
	}
	return nil
}
