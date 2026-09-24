// Package config installs and removes the ShareCodex statusLine shim in
// Claude Code's settings.json. Changes are planned first, so applying a plan
// twice is a no-op and the user's other settings keep their order.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/KoukeNeko/ShareCodex/internal/atomicfile"
)

const statusLineKey = "statusLine"

// ChangePlan replaces the statusLine setting. A nil Next removes the key.
type ChangePlan struct {
	Previous json.RawMessage
	Next     json.RawMessage
	changed  bool
}

func (p ChangePlan) Empty() bool { return !p.changed }

// SettingsPath is Claude Code's user settings file. With several
// CLAUDE_CONFIG_DIR entries the first is used.
func SettingsPath() (string, error) {
	if env := os.Getenv("CLAUDE_CONFIG_DIR"); env != "" {
		first, _, _ := strings.Cut(env, ",")
		return filepath.Join(strings.TrimSpace(first), "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// ShimCommand is the statusLine command that runs this executable's shim.
func ShimCommand(executable string) string {
	// Plain double quotes rather than Go quoting: Windows paths must keep
	// single backslashes for cmd.exe.
	return `"` + executable + `" statusline`
}

// StatusLine returns the statusLine setting, or nil when none is set.
func StatusLine(settings []byte) (json.RawMessage, error) {
	obj, err := decodeObject(settings)
	if err != nil {
		return nil, err
	}
	return obj.get(statusLineKey), nil
}

// PointsAt reports whether a statusLine value runs the shim of executable.
func PointsAt(value json.RawMessage, executable string) bool {
	var sl struct {
		Command string `json:"command"`
	}
	return json.Unmarshal(value, &sl) == nil && sl.Command == ShimCommand(executable)
}

// IsShim reports whether a statusLine value points at a ShareCodex shim,
// including one from an older install location.
func IsShim(value json.RawMessage) bool {
	var sl struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(value, &sl) != nil {
		return false
	}
	return strings.HasSuffix(sl.Command, " statusline") && strings.Contains(strings.ToLower(sl.Command), "sharecodex")
}

func PlanInstall(settings []byte, shimCommand string) (ChangePlan, error) {
	obj, err := decodeObject(settings)
	if err != nil {
		return ChangePlan{}, err
	}
	prev := obj.get(statusLineKey)

	next := map[string]any{"type": "command", "command": shimCommand}
	var old struct {
		Padding *int `json:"padding"`
	}
	if prev != nil && json.Unmarshal(prev, &old) == nil && old.Padding != nil {
		next["padding"] = *old.Padding
	}
	nextJSON, err := json.Marshal(next)
	if err != nil {
		return ChangePlan{}, err
	}
	return ChangePlan{Previous: prev, Next: nextJSON, changed: !jsonEqual(prev, nextJSON)}, nil
}

// PlanRestore puts back the statusLine saved at install time. A null
// original means there was none, so the key is removed.
func PlanRestore(settings []byte, original json.RawMessage) (ChangePlan, error) {
	obj, err := decodeObject(settings)
	if err != nil {
		return ChangePlan{}, err
	}
	prev := obj.get(statusLineKey)
	if prev == nil || !IsShim(prev) {
		// The user changed it after install; theirs wins.
		return ChangePlan{Previous: prev}, nil
	}
	var next json.RawMessage
	if len(original) > 0 && !bytes.Equal(bytes.TrimSpace(original), []byte("null")) {
		next = original
	}
	return ChangePlan{Previous: prev, Next: next, changed: true}, nil
}

// Apply writes the plan to settings.json after keeping one backup of the
// file as it was before ShareCodex first touched it.
func Apply(path string, plan ChangePlan) error {
	if plan.Empty() {
		return nil
	}
	current, err := ReadSettings(path)
	if err != nil {
		return err
	}
	obj, err := decodeObject(current)
	if err != nil {
		return err
	}
	if !jsonEqual(obj.get(statusLineKey), plan.Previous) {
		return errors.New("settings.json changed since the plan was made")
	}
	obj.set(statusLineKey, plan.Next)
	out, err := obj.encode()
	if err != nil {
		return err
	}

	backup := path + ".sharecodex-backup"
	if len(current) > 0 {
		if _, err := os.Stat(backup); errors.Is(err, fs.ErrNotExist) {
			if err := os.WriteFile(backup, current, 0o600); err != nil {
				return fmt.Errorf("back up settings.json: %w", err)
			}
		}
	}
	return atomicfile.Write(path, out, 0o600)
}

// ReadSettings returns settings.json, or nil when it does not exist yet.
func ReadSettings(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

type member struct {
	key   string
	value json.RawMessage
}

// object is a JSON object that keeps its keys in file order.
type object []member

func decodeObject(b []byte) (object, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return object{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("settings.json is not a JSON object")
	}
	var obj object
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse settings.json: %w", err)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("parse settings.json: %w", err)
		}
		obj = append(obj, member{key: tok.(string), value: v})
	}
	return obj, nil
}

func (o object) get(key string) json.RawMessage {
	for _, m := range o {
		if m.key == key {
			return m.value
		}
	}
	return nil
}

func (o *object) set(key string, v json.RawMessage) {
	for i, m := range *o {
		if m.key == key {
			if v == nil {
				*o = append((*o)[:i], (*o)[i+1:]...)
			} else {
				(*o)[i].value = v
			}
			return
		}
	}
	if v != nil {
		*o = append(*o, member{key: key, value: v})
	}
}

func (o object) encode() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("{")
	for i, m := range o {
		if i > 0 {
			buf.WriteString(",")
		}
		key, _ := json.Marshal(m.key)
		var val bytes.Buffer
		if err := json.Indent(&val, m.value, "  ", "  "); err != nil {
			return nil, err
		}
		fmt.Fprintf(&buf, "\n  %s: %s", key, val.Bytes())
	}
	buf.WriteString("\n}\n")
	return buf.Bytes(), nil
}

func jsonEqual(a, b json.RawMessage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}
