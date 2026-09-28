//go:build darwin

package oauthusage

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

// keychainService is the macOS Keychain item Claude Code keeps its current
// sign-in in.
const keychainService = "Claude Code-credentials"

// keychainTimeout bounds the read: the first one may wait for the user to
// allow access to the item.
const keychainTimeout = 20 * time.Second

// keychainCredential reads Claude Code's sign-in from the macOS Keychain. A
// missing item is reported as fs.ErrNotExist, so the caller falls back to the
// credential file.
func keychainCredential(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, keychainTimeout)
	defer cancel()
	out, err := provider.Command(ctx, "/usr/bin/security",
		"find-generic-password", "-s", keychainService, "-w").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && (exit.ExitCode() == 44 || strings.Contains(string(exit.Stderr), "could not be found")) {
			return nil, fs.ErrNotExist
		}
		return nil, fmt.Errorf("read Claude Code keychain item: %w", err)
	}
	return out, nil
}
