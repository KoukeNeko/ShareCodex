//go:build !darwin

package oauthusage

import (
	"context"
	"io/fs"
)

// keychainCredential reports that this platform has no keychain to read:
// Claude Code writes its sign-in to the credential file here.
func keychainCredential(context.Context) ([]byte, error) { return nil, fs.ErrNotExist }
