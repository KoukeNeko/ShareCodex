package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/config"
)

func newTestAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	t.Setenv("SHARECODEX_HOME", t.TempDir())
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	a, err := New(context.Background(), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a, filepath.Join(claudeDir, "settings.json")
}

func TestRepairRepointsAStaleShim(t *testing.T) {
	a, path := newTestAgent(t)
	if err := os.WriteFile(path, []byte(`{"statusLine":{"type":"command","command":"~/bar.sh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.InstallStatusLine("/old/ShareCodex.app/Contents/MacOS/ShareCodex"); err != nil {
		t.Fatal(err)
	}

	a.RepairInstalledPaths("/Applications/ShareCodex.app/Contents/MacOS/ShareCodex")

	b, _ := os.ReadFile(path)
	sl, err := config.StatusLine(b)
	if err != nil {
		t.Fatal(err)
	}
	if !config.PointsAt(sl, "/Applications/ShareCodex.app/Contents/MacOS/ShareCodex") {
		t.Fatalf("statusLine not re-pointed: %s", sl)
	}
	if got := string(a.settings.StatusLine.Original); !strings.Contains(got, "~/bar.sh") {
		t.Fatalf("original statusLine lost: %s", got)
	}
}

func TestRepairLeavesAForeignStatusLineAlone(t *testing.T) {
	a, path := newTestAgent(t)
	const mine = `{"statusLine":{"type":"command","command":"~/bar.sh"}}`
	if err := os.WriteFile(path, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	a.RepairInstalledPaths("/Applications/ShareCodex.app/Contents/MacOS/ShareCodex")
	if b, _ := os.ReadFile(path); string(b) != mine {
		t.Fatalf("a statusLine that is not ours was changed: %s", b)
	}
}

func TestRefusesTranslocatedPaths(t *testing.T) {
	a, path := newTestAgent(t)
	moving := "/private/var/folders/x/T/AppTranslocation/ABC/d/ShareCodex.app/Contents/MacOS/ShareCodex"
	if err := a.InstallStatusLine(moving); err != errTranslocated {
		t.Fatalf("install from a translocated copy = %v, want errTranslocated", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("settings.json was written for a translocated copy")
	}
	if err := a.SetLaunchAtLogin(moving, true); err != errTranslocated {
		t.Fatalf("login item from a translocated copy = %v, want errTranslocated", err)
	}
}
