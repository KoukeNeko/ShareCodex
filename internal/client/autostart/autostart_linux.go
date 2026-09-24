package autostart

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/KoukeNeko/ShareCodex/internal/atomicfile"
)

const unitName = "sharecodex.service"

// environment is copied into the unit because systemd starts user services
// with a minimal PATH, which usually misses npm- or nvm-installed CLIs.
var environment = []string{"PATH", "SHARECODEX_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME"}

func unitPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "systemd", "user", unitName), nil
}

// Set installs or removes a systemd user service that runs the background
// agent. Enabling also restarts it, so it picks up a new join or binary.
func Set(executable string, enabled bool) error {
	p, err := unitPath()
	if err != nil {
		return err
	}
	if !enabled {
		if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err := systemctl("disable", "--now", unitName); err != nil {
			return err
		}
		if err := os.Remove(p); err != nil {
			return err
		}
		return systemctl("daemon-reload")
	}

	env := map[string]string{}
	for _, name := range environment {
		if v := os.Getenv(name); v != "" {
			env[name] = v
		}
	}
	if err := atomicfile.Write(p, []byte(unit(executable, env)), 0o644); err != nil {
		return err
	}
	if err := systemctl("daemon-reload"); err != nil {
		return err
	}
	if err := systemctl("enable", unitName); err != nil {
		return err
	}
	return systemctl("restart", unitName)
}

func unit(executable string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=ShareCodex agent\nAfter=network-online.target\n\n[Service]\n")
	fmt.Fprintf(&b, "ExecStart=%s agent\n", quote(executable))
	for _, name := range environment {
		if v, ok := env[name]; ok {
			fmt.Fprintf(&b, "Environment=%s\n", quote(name+"="+v))
		}
	}
	b.WriteString("Restart=on-failure\nRestartSec=30\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

// quote escapes a value for a systemd unit, where % starts a specifier.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(s) + `"`
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
