// Package scan finds session log files that changed since the last pass.
// Changed files are re-parsed whole; idempotent inserts keyed by each
// event's DedupeKey make re-reading safe.
package scan

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

type FileState struct {
	Size    int64
	ModTime time.Time
}

// List returns every .jsonl file under the given roots. Roots that do not
// exist are skipped: a provider that is not installed is not an error.
func List(roots []string) (map[string]FileState, error) {
	files := make(map[string]FileState)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			files[path] = FileState{Size: info.Size(), ModTime: info.ModTime()}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// Changed returns the paths in current that are new or differ from known.
func Changed(known, current map[string]FileState) []string {
	var changed []string
	for path, st := range current {
		if prev, ok := known[path]; !ok || prev.Size != st.Size || !prev.ModTime.Equal(st.ModTime) {
			changed = append(changed, path)
		}
	}
	return changed
}
