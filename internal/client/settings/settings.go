// Package settings locates the client's data directory and persists its
// non-secret settings. The device token lives in the OS keychain instead.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/KoukeNeko/ShareCodex/internal/atomicfile"
)

type Settings struct {
	ServerURL  string `json:"server_url,omitempty"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
	PersonID   string `json:"person_id,omitempty"`
	PersonName string `json:"person_name,omitempty"`
	// StatusLine is Claude Code's statusLine setting from before ShareCodex
	// installed its shim; the shim chains to it and Restore puts it back.
	StatusLine    *SavedStatusLine `json:"status_line,omitempty"`
	LaunchAtLogin bool             `json:"launch_at_login,omitempty"`
}

type SavedStatusLine struct {
	// Original is the raw JSON value, or null when none was configured.
	Original json.RawMessage `json:"original"`
}

func (s Settings) Paired() bool { return s.ServerURL != "" && s.DeviceID != "" }

// Dir is where the client keeps its database, settings and spool files.
// SHARECODEX_HOME overrides it, e.g. to run two profiles on one machine.
func Dir() (string, error) {
	if d := os.Getenv("SHARECODEX_HOME"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "ShareCodex"), nil
}

func path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

func Load() (Settings, error) {
	p, err := path()
	if err != nil {
		return Settings{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		return Settings{}, fmt.Errorf("decode %s: %w", p, err)
	}
	return s, nil
}

func Save(s Settings) error {
	p, err := path()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(p, b, 0o600)
}
