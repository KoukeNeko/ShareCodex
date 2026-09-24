package agent

import "github.com/KoukeNeko/ShareCodex/internal/client/settings"

// PopupHeight returns the saved popup height, or 0 when none was saved.
func (a *Agent) PopupHeight() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings.PopupHeight
}

// SetPopupHeight remembers the height the user resized the popup to.
func (a *Agent) SetPopupHeight(height int) {
	a.mu.Lock()
	if a.settings.PopupHeight == height {
		a.mu.Unlock()
		return
	}
	a.settings.PopupHeight = height
	st := a.settings
	a.mu.Unlock()
	if err := settings.Save(st); err != nil {
		a.log.Error("save popup height", "err", err)
	}
}
