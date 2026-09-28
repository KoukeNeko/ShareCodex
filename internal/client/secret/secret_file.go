//go:build linux

package secret

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/KoukeNeko/ShareCodex/internal/atomicfile"
	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
)

func tokenPath(deviceID string) (string, error) {
	return secretPath("device-", deviceID, ".token")
}

func claudeLoginPath(refHash string) (string, error) {
	return secretPath("claude-login-", refHash, ".json")
}

func secretPath(prefix, id, suffix string) (string, error) {
	// IDs come from the server or a hash; they must not escape the data
	// directory.
	if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return "", fmt.Errorf("invalid secret ID %q", id)
	}
	dir, err := settings.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, prefix+id+suffix), nil
}

func SetToken(deviceID, token string) error {
	p, err := tokenPath(deviceID)
	if err != nil {
		return err
	}
	return atomicfile.Write(p, []byte(token), 0o600)
}

func Token(deviceID string) (string, error) {
	p, err := tokenPath(deviceID)
	if err != nil {
		return "", err
	}
	return readSecret(p)
}

func DeleteToken(deviceID string) error {
	p, err := tokenPath(deviceID)
	if err != nil {
		return err
	}
	return removeSecret(p)
}

func SetClaudeLogin(refHash, value string) error {
	p, err := claudeLoginPath(refHash)
	if err != nil {
		return err
	}
	return atomicfile.Write(p, []byte(value), 0o600)
}

func ClaudeLogin(refHash string) (string, error) {
	p, err := claudeLoginPath(refHash)
	if err != nil {
		return "", err
	}
	return readSecret(p)
}

func DeleteClaudeLogin(refHash string) error {
	p, err := claudeLoginPath(refHash)
	if err != nil {
		return err
	}
	return removeSecret(p)
}

func readSecret(p string) (string, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func removeSecret(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
