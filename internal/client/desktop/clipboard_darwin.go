package desktop

import (
	"context"
	"fmt"

	"github.com/KoukeNeko/ShareCodex/internal/provider"
)

func copyImageFile(ctx context.Context, path string) error {
	// The path goes in as an argument, never into the script text.
	out, err := provider.Command(ctx, "/usr/bin/osascript",
		"-e", "on run argv",
		"-e", "set the clipboard to (read (POSIX file (item 1 of argv)) as «class PNGf»)",
		"-e", "end run",
		path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy image: %w: %s", err, out)
	}
	return nil
}
