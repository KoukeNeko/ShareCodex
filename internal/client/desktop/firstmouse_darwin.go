package desktop

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <WebKit/WebKit.h>
#import <objc/runtime.h>

static BOOL acceptsFirstMouse(id self, SEL _cmd, NSEvent *event) {
	return YES;
}

// Overrides the method on WKWebView alone; NSView's own, which every other
// view inherits, is left as it is.
static void acceptFirstMouseInWebViews(void) {
	SEL sel = @selector(acceptsFirstMouse:);
	Method m = class_getInstanceMethod([WKWebView class], sel);
	class_replaceMethod([WKWebView class], sel, (IMP)acceptsFirstMouse, method_getTypeEncoding(m));
}
*/
import "C"

// acceptFirstMouse makes a click on a window that is not the key window act
// at once. WKWebView otherwise spends that click making its window key, so
// the first click on a widget, such as on its close button, would do
// nothing.
func acceptFirstMouse() {
	C.acceptFirstMouseInWebViews()
}
