//go:build !darwin

package desktop

// acceptFirstMouse is needed only for macOS's WKWebView.
func acceptFirstMouse() {}
