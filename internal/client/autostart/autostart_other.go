//go:build !darwin && !windows && !linux

package autostart

import "errors"

func Set(string, bool) error {
	return errors.New("launch at login is supported on macOS, Windows and Linux")
}
