//go:build !linux

package secret

import (
	"errors"

	"github.com/zalando/go-keyring"
)

const service = "ShareCodex"

func SetToken(deviceID, token string) error {
	return keyring.Set(service, deviceID, token)
}

func Token(deviceID string) (string, error) {
	t, err := keyring.Get(service, deviceID)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return t, err
}

func DeleteToken(deviceID string) error {
	err := keyring.Delete(service, deviceID)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// claudeLoginKey keeps Claude sign-ins apart from device tokens, which are
// keyed by device ID in the same service.
func claudeLoginKey(refHash string) string { return "claude-login:" + refHash }

func SetClaudeLogin(refHash, value string) error {
	return keyring.Set(service, claudeLoginKey(refHash), value)
}

func ClaudeLogin(refHash string) (string, error) {
	v, err := keyring.Get(service, claudeLoginKey(refHash))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func DeleteClaudeLogin(refHash string) error {
	err := keyring.Delete(service, claudeLoginKey(refHash))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
