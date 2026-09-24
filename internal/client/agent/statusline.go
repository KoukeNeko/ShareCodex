package agent

import (
	"fmt"

	"github.com/KoukeNeko/ShareCodex/internal/client/autostart"
	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/config"
)

// InstallStatusLine points Claude Code's statusLine at this executable's
// shim, remembering the user's previous statusLine so the shim can chain to
// it and Restore can put it back.
func (a *Agent) InstallStatusLine(executable string) error {
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
