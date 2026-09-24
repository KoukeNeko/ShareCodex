package scan

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListAndChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "s.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "ignore.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := List([]string{dir, filepath.Join(dir, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("got %d files, want 1", len(first))
	}
	if got := Changed(nil, first); len(got) != 1 {
		t.Fatalf("new file should be changed, got %v", got)
	}
	if got := Changed(first, first); len(got) != 0 {
		t.Fatalf("unchanged file reported: %v", got)
	}

	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	second, err := List([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := Changed(first, second); len(got) != 1 {
		t.Fatalf("touched file should be changed, got %v", got)
	}
}
