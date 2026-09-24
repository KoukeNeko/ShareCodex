package main

import "errors"

// There is no tray app on Linux, where the agent usually runs over SSH.
func runDesktop() error {
	return errors.New("the tray app is not available on Linux; run `sharecodex agent` or `sharecodex autostart on`\n\n" + usageText)
}
