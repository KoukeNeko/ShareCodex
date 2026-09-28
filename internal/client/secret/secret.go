// Package secret keeps the device token and ShareCodex's own Claude sign-ins
// in the OS credential store (macOS Keychain, Windows Credential Manager). On
// Linux, where the agent usually runs over SSH without a Secret Service, each
// is a file readable only by the user.
package secret

import "errors"

var ErrNotFound = errors.New("device token not found")
