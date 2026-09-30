package desktop

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// maxImageBytes bounds the image the popup hands over: a full-length popup at
// twice its size is a few megabytes.
const maxImageBytes = 32 << 20

// CopyImage puts a PNG, base64-encoded, on the clipboard. Wails can only copy
// text, so the image goes through a temporary file the system copies from.
func (s *Service) CopyImage(ctx context.Context, pngBase64 string) error {
	png, err := base64.StdEncoding.DecodeString(pngBase64)
	if err != nil {
		return fmt.Errorf("decode image: %w", err)
	}
	if len(png) == 0 || len(png) > maxImageBytes {
		return errors.New("image is empty or too large")
	}
	dir, err := os.MkdirTemp("", "sharecodex-share-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "ShareCodex.png")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		return err
	}
	return copyImageFile(ctx, path)
}
