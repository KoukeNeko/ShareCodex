package desktop

import (
	"context"
	"fmt"

	"github.com/KoukeNeko/ShareCodex/internal/provider"
)

func copyImageFile(ctx context.Context, path string) error {
	// The clipboard needs a single-threaded apartment; the path goes in as an
	// argument, never into the script text.
	const script = `param($p) Add-Type -AssemblyName System.Windows.Forms, System.Drawing; ` +
		`$img = [System.Drawing.Image]::FromFile($p); try { [System.Windows.Forms.Clipboard]::SetImage($img) } finally { $img.Dispose() }`
	out, err := provider.Command(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-STA",
		"-Command", "& {"+script+"}", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy image: %w: %s", err, out)
	}
	return nil
}
