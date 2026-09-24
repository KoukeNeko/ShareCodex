// Package provider holds what the Claude Code and Codex adapters share.
package provider

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// LookPath finds a provider CLI. Apps started from the macOS Dock or the
// Windows Start menu get a minimal PATH, so the usual install locations are
// tried after PATH.
func LookPath(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", name),
		filepath.Join(home, ".claude", "local", name),
		filepath.Join(home, ".bun", "bin", name),
		filepath.Join(home, ".npm-global", "bin", name),
		"/opt/homebrew/bin/" + name,
		"/usr/local/bin/" + name,
	}
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		candidates = []string{
			filepath.Join(home, ".local", "bin", name+".exe"),
			filepath.Join(appData, "npm", name+".cmd"),
			filepath.Join(home, ".bun", "bin", name+".exe"),
		}
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}
	return "", exec.ErrNotFound
}
