//go:build darwin

package antigravity

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"time"

	"github.com/KoukeNeko/ShareCodex/internal/provider"
)

// keychainTimeout bounds the read: the first one may wait for the user to
// allow access to the item.
const keychainTimeout = 20 * time.Second

// storedCredential reads agy's sign-in from the macOS Keychain, where it
// keeps it as the "antigravity" account of the "gemini" service.
func storedCredential(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, keychainTimeout)
	defer cancel()
	out, err := provider.Command(ctx, "/usr/bin/security",
		"find-generic-password", "-s", "gemini", "-a", "antigravity", "-w").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && (exit.ExitCode() == 44 || strings.Contains(string(exit.Stderr), "could not be found")) {
			return "", fs.ErrNotExist
		}
		return "", fmt.Errorf("read Antigravity's keychain item: %w", err)
	}
	return string(out), nil
}
