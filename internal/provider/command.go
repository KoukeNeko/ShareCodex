package provider

import (
	"context"
	"os/exec"
)

// Command builds a command for the provider CLIs and the statusLine chain.
// On Windows the desktop app is a GUI program, so every console program it
// starts would otherwise open its own terminal window.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hideConsole(cmd)
	return cmd
}
