//go:build !darwin && !windows

package desktop

import (
	"context"
	"errors"
)

func copyImageFile(context.Context, string) error {
	return errors.New("copying images is not supported on this system")
}
