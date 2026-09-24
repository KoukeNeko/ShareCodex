package scan

import (
	"io/fs"
	"os"
)

// fileInfo reads size and modification time through a handle on the file.
// A directory listing on NTFS reports the size recorded in the directory
// index, which is updated lazily while another process holds the file open
// for writing, as Codex does with its session log. Changes would then go
// unnoticed until the writer closed the file.
func fileInfo(path string, _ fs.DirEntry) (fs.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}
