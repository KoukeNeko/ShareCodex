// Package desktop finds which account Claude Desktop ran each Claude Code
// session under. Desktop signs in separately from the claude CLI, so its
// sessions cannot be attributed through `claude auth status`. It keeps each
// session's metadata in a folder named after the account and organization;
// the organization UUID is the value the CLI reports as orgId, so both map to
// the same pooled account. Only this session metadata is read, never
// Desktop's settings or credentials.
//
// Desktop also lists sessions started from the terminal, filed under
// whichever organization it was using then, so a session's folder is only
// meaningful for usage whose transcript entrypoint is Desktop's.
package desktop

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Dir is Claude Desktop's data folder.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Claude"), nil
}

// Entrypoints are the transcript entrypoints of Claude Code run by Claude
// Desktop signed into a Claude account: its Code tab and Cowork.
var Entrypoints = []string{"claude-desktop", "local-agent"}

// FromDesktop reports whether a transcript entrypoint is Claude Desktop
// signed into a Claude account.
func FromDesktop(entrypoint string) bool {
	return slices.Contains(Entrypoints, entrypoint)
}

// ThirdParty reports whether a transcript entrypoint is Claude Desktop set up
// for third-party inference, whose usage never draws on a Claude
// subscription.
func ThirdParty(entrypoint string) bool {
	return entrypoint == "claude-desktop-3p"
}

type metadata struct {
	CLISessionID string `json:"cliSessionId"`
}

// Sessions maps Claude Code session IDs to the organization Desktop filed
// them under: Code sessions from
// claude-code-sessions/<account>/<org>/local_*.json, and Cowork sessions from
// the transcripts Cowork keeps under local-agent-mode-sessions/<account>/<org>.
// A missing folder means Desktop is not installed and yields no sessions.
func Sessions(dir string) (map[string]string, error) {
	out := map[string]string{}

	code, err := filepath.Glob(filepath.Join(dir, "claude-code-sessions", "*", "*", "local_*.json"))
	if err != nil {
		return nil, err
	}
	for _, p := range code {
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var m metadata
		// Desktop's files are not a public format; skip what does not parse
		// rather than stop attributing every other session.
		if json.Unmarshal(b, &m) != nil || m.CLISessionID == "" {
			continue
		}
		out[m.CLISessionID] = filepath.Base(filepath.Dir(p))
	}

	for _, root := range CoworkRoots(dir) {
		org := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(root))))
		transcripts, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
		if err != nil {
			return nil, err
		}
		for _, p := range transcripts {
			out[strings.TrimSuffix(filepath.Base(p), ".jsonl")] = org
		}
	}
	return out, nil
}

// CoworkRoots returns the Claude Code projects folders of Cowork sessions,
// local-agent-mode-sessions/<account>/<org>/<session>/.claude/projects.
func CoworkRoots(dir string) []string {
	roots, _ := filepath.Glob(filepath.Join(dir, "local-agent-mode-sessions", "*", "*", "local_*", ".claude", "projects"))
	return roots
}
