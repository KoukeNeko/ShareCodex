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
