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
	// PopupHeight is the popup height the user last resized to, in points.
	PopupHeight int `json:"popup_height,omitempty"`
	// Language is the UI language ("en" or "zh-TW"); empty means English.
	Language string `json:"language,omitempty"`
	// FineChart draws the usage chart at the server's full detail instead of
	// grouping it into about 60 points.
	FineChart bool `json:"fine_chart,omitempty"`
	// AccountSort orders the popup's accounts: "" keeps the server's order
	// (the accounts this device is signed into first), "five_hour" and
	// "weekly" put the most used window first, and "custom" follows
	// AccountOrder, the account IDs in the order they were dragged into.
	AccountSort  string   `json:"account_sort,omitempty"`
	AccountOrder []string `json:"account_order,omitempty"`
	// ModelSort orders the models listed under a quota window: "" keeps the
	// server's order, the most estimated quota use first, and "tokens" puts
	// the most tokens first.
	ModelSort string `json:"model_sort,omitempty"`
	// Widgets are the accounts pinned to the screen, each in an always-on-top
	// window of its own, reopened at launch.
	Widgets []Widget `json:"widgets,omitempty"`
}

// Widget is one pinned account and where its window was last moved to.
type Widget struct {
	AccountID string `json:"account_id"`
	// Placed is false until the window is moved; X and Y are then its
	// position.
	Placed bool `json:"placed,omitempty"`
	X      int  `json:"x,omitempty"`
	Y      int  `json:"y,omitempty"`
	// Height is what the user resized the window to; 0 fits it to its
	// card.
	Height int `json:"height,omitempty"`
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
