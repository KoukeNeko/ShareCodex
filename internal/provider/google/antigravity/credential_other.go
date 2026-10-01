//go:build !darwin

package antigravity

import (
	"context"
	"errors"
	"io/fs"

	"github.com/zalando/go-keyring"
)

// storedCredential reads agy's sign-in from the system credential store,
// where it keeps it as the "antigravity" account of the "gemini" service.
func storedCredential(context.Context) (string, error) {
	v, err := keyring.Get("gemini", "antigravity")
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fs.ErrNotExist
	}
	return v, err
}
