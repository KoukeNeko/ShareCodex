//go:build !darwin && !windows

package autostart

import "errors"

func Set(string, bool) error { return errors.New("launch at login is supported on macOS and Windows") }
