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
