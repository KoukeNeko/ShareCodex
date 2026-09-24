//go:build !windows

package scan

import "io/fs"

// fileInfo uses the directory entry, which is current on these systems.
func fileInfo(_ string, d fs.DirEntry) (fs.FileInfo, error) {
	return d.Info()
}
