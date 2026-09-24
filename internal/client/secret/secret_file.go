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
	// The ID comes from the server; it must not escape the data directory.
	if deviceID == "" || strings.ContainsAny(deviceID, `/\`) || deviceID == "." || deviceID == ".." {
		return "", fmt.Errorf("invalid device ID %q", deviceID)
	}
	dir, err := settings.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "device-"+deviceID+".token"), nil
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
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func DeleteToken(deviceID string) error {
	p, err := tokenPath(deviceID)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
