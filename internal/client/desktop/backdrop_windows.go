package desktop

import "github.com/wailsapp/wails/v3/pkg/w32"

// acrylicSupported reports whether DWM can draw the Acrylic system backdrop,
// which needs Windows 11 22H2 (build 22621). Older builds fall back to an
// untinted blur that looks worse than an opaque popup.
func acrylicSupported() bool { return w32.SupportsBackdropTypes() }
