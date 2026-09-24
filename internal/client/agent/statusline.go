package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/KoukeNeko/ShareCodex/internal/client/autostart"
	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/config"
)

// InstallStatusLine points Claude Code's statusLine at this executable's
// shim, remembering the user's previous statusLine so the shim can chain to
// it and Restore can put it back.
func (a *Agent) InstallStatusLine(executable string) error {
	if translocated(executable) {
		return errTranslocated
	}
	path, err := config.SettingsPath()
	if err != nil {
		return err
	}
	current, err := config.ReadSettings(path)
	if err != nil {
		return err
	}
	plan, err := config.PlanInstall(current, config.ShimCommand(executable))
	if err != nil {
		return err
	}
	if plan.Empty() {
		return nil
	}

	a.mu.Lock()
	// When re-pointing an older shim, keep the original saved at first install.
	if a.settings.StatusLine == nil && !config.IsShim(plan.Previous) {
		original := plan.Previous
		if original == nil {
			original = []byte("null")
		}
		a.settings.StatusLine = &settings.SavedStatusLine{Original: original}
	}
	st := a.settings
	a.mu.Unlock()

	if err := settings.Save(st); err != nil {
		return err
	}
	if err := config.Apply(path, plan); err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	a.changed()
	return nil
}

func (a *Agent) RestoreStatusLine() error {
	path, err := config.SettingsPath()
	if err != nil {
		return err
	}
	current, err := config.ReadSettings(path)
	if err != nil {
		return err
	}
	a.mu.Lock()
	var original []byte
	if a.settings.StatusLine != nil {
		original = a.settings.StatusLine.Original
	}
	a.mu.Unlock()

	plan, err := config.PlanRestore(current, original)
	if err != nil {
		return err
	}
	if err := config.Apply(path, plan); err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}

	a.mu.Lock()
	a.settings.StatusLine = nil
	st := a.settings
	a.mu.Unlock()
	if err := settings.Save(st); err != nil {
		return err
	}
	a.changed()
	return nil
}

func (a *Agent) SetLaunchAtLogin(executable string, enabled bool) error {
	if enabled && translocated(executable) {
		return errTranslocated
	}
	if err := autostart.Set(executable, enabled); err != nil {
		return err
	}
	a.mu.Lock()
	a.settings.LaunchAtLogin = enabled
	st := a.settings
	a.mu.Unlock()
	if err := settings.Save(st); err != nil {
		return err
	}
	a.changed()
	return nil
}

var errTranslocated = errors.New("move ShareCodex to the Applications folder and open it from there first")

// translocated reports whether macOS is running the app from a temporary
// App Translocation copy (a quarantined app opened where it was downloaded).
// That path disappears when the app quits, so nothing may be pointed at it.
func translocated(executable string) bool {
	return strings.Contains(executable, "/AppTranslocation/")
}

// RepairInstalledPaths re-points the statusLine hook and the login item at
// this executable when they refer to another copy of the app, for example
// after it was moved, reinstalled or had run from a translocated path.
func (a *Agent) RepairInstalledPaths(executable string) {
	if translocated(executable) {
		return
	}
	path, err := config.SettingsPath()
	if err != nil {
		a.log.Error("repair statusLine", "err", err)
		return
	}
	current, err := config.ReadSettings(path)
	if err != nil {
		a.log.Error("repair statusLine", "err", err)
		return
	}
	if statusLine, err := config.StatusLine(current); err != nil {
		a.log.Error("repair statusLine", "err", err)
	} else if config.IsShim(statusLine) && !config.PointsAt(statusLine, executable) {
		if err := a.InstallStatusLine(executable); err != nil {
			a.log.Error("repair statusLine", "err", err)
		} else {
			a.log.Info("re-pointed Claude Code statusLine", "executable", executable)
		}
	}

	a.mu.Lock()
	launchAtLogin := a.settings.LaunchAtLogin
	a.mu.Unlock()
	if launchAtLogin {
		if err := autostart.Set(executable, true); err != nil {
			a.log.Error("repair login item", "err", err)
		}
	}
}
