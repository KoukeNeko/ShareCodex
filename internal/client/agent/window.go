package agent

import (
	"fmt"
	"slices"

	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
)

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

// SetFineChart saves whether the usage chart shows its full detail.
func (a *Agent) SetFineChart(enabled bool) error {
	a.mu.Lock()
	a.settings.FineChart = enabled
	st := a.settings
	a.mu.Unlock()
	if err := settings.Save(st); err != nil {
		return err
	}
	a.changed()
	return nil
}

// accountSorts are the orders the popup can list accounts in; see
// settings.Settings.AccountSort.
var accountSorts = []string{"", "five_hour", "weekly", "custom"}

// SetAccountSort saves the order the popup lists accounts in.
func (a *Agent) SetAccountSort(sort string) error {
	if !slices.Contains(accountSorts, sort) {
		return fmt.Errorf("unsupported account order %q", sort)
	}
	a.mu.Lock()
	a.settings.AccountSort = sort
	st := a.settings
	a.mu.Unlock()
	if err := settings.Save(st); err != nil {
		return err
	}
	a.changed()
	return nil
}

// SetAccountOrder saves the order accounts were dragged into and lists
// them in it.
func (a *Agent) SetAccountOrder(ids []string) error {
	a.mu.Lock()
	a.settings.AccountSort = "custom"
	a.settings.AccountOrder = slices.Clone(ids)
	st := a.settings
	a.mu.Unlock()
	if err := settings.Save(st); err != nil {
		return err
	}
	a.changed()
	return nil
}

// Widgets returns the pinned accounts.
func (a *Agent) Widgets() []settings.Widget {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.settings.Widgets)
}

// PinAccount pins an account to the screen; pinning it again does nothing.
func (a *Agent) PinAccount(id string) error {
	return a.updateWidgets(func(ws []settings.Widget) []settings.Widget {
		if slices.ContainsFunc(ws, func(w settings.Widget) bool { return w.AccountID == id }) {
			return ws
		}
		return append(ws, settings.Widget{AccountID: id})
	}, true)
}

// UnpinAccount closes an account's widget for good.
func (a *Agent) UnpinAccount(id string) error {
	return a.updateWidgets(func(ws []settings.Widget) []settings.Widget {
		return slices.DeleteFunc(ws, func(w settings.Widget) bool { return w.AccountID == id })
	}, true)
}

// SetWidgetPosition remembers where a widget was moved to, so it reopens
// there.
func (a *Agent) SetWidgetPosition(id string, x, y int) error {
	return a.updateWidgets(func(ws []settings.Widget) []settings.Widget {
		if i := slices.IndexFunc(ws, func(w settings.Widget) bool { return w.AccountID == id }); i >= 0 {
			ws[i].Placed, ws[i].X, ws[i].Y = true, x, y
		}
		return ws
	}, false)
}

// updateWidgets saves a change to the pinned accounts; notify is false for
// changes the popup does not show.
func (a *Agent) updateWidgets(change func([]settings.Widget) []settings.Widget, notify bool) error {
	a.mu.Lock()
	a.settings.Widgets = change(slices.Clone(a.settings.Widgets))
	st := a.settings
	a.mu.Unlock()
	if err := settings.Save(st); err != nil {
		return err
	}
	if notify {
		a.changed()
	}
	return nil
}

// Languages the UI is translated into; the first is the default.
var languages = []string{"en", "zh-TW"}

// language returns the saved UI language, defaulting to English. The caller
// holds a.mu.
func (a *Agent) language() string {
	for _, l := range languages {
		if a.settings.Language == l {
			return l
		}
	}
	return languages[0]
}

// Language returns the UI language.
func (a *Agent) Language() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.language()
}

// SetLanguage saves the UI language.
func (a *Agent) SetLanguage(lang string) error {
	if !slices.Contains(languages, lang) {
		return fmt.Errorf("unsupported language %q", lang)
	}
	a.mu.Lock()
	a.settings.Language = lang
	st := a.settings
	a.mu.Unlock()
	if err := settings.Save(st); err != nil {
		return err
	}
	a.changed()
	return nil
}
