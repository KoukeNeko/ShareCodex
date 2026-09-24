package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const shim = `"/Applications/ShareCodex.app/Contents/MacOS/ShareCodex" statusline`

func TestInstallIsIdempotentAndKeepsKeyOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{"model":"opus","statusLine":{"type":"command","command":"~/bar.sh","padding":1},"hooks":{}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := PlanInstall([]byte(original), shim)
	if err != nil || first.Empty() {
		t.Fatalf("first plan = %+v, %v; want a change", first, err)
	}
	if err := Apply(path, first); err != nil {
		t.Fatal(err)
	}

	after, _ := os.ReadFile(path)
	s := string(after)
	if !(strings.Index(s, `"model"`) < strings.Index(s, `"statusLine"`) && strings.Index(s, `"statusLine"`) < strings.Index(s, `"hooks"`)) {
		t.Errorf("key order changed:\n%s", s)
	}
	if !strings.Contains(s, `"padding": 1`) {
		t.Errorf("padding not preserved:\n%s", s)
	}

	second, err := PlanInstall(after, shim)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Empty() {
		t.Errorf("second install plan should be empty, got %+v", second)
	}

	if backup, _ := os.ReadFile(path + ".sharecodex-backup"); string(backup) != original {
		t.Errorf("backup = %q, want the pre-install file", backup)
	}
}

func TestRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	install, _ := PlanInstall([]byte(`{"model":"opus"}`), shim)
	if install.Previous != nil {
		t.Fatalf("no statusLine configured, Previous = %s", install.Previous)
	}
	if err := Apply(path, install); err != nil {
		t.Fatal(err)
	}

	installed, _ := os.ReadFile(path)
	restore, err := PlanRestore(installed, nil)
	if err != nil || restore.Empty() {
		t.Fatalf("restore plan = %+v, %v", restore, err)
	}
	if err := Apply(path, restore); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "statusLine") {
		t.Errorf("statusLine should be removed, got:\n%s", got)
	}
}

func TestRestoreLeavesUserChangesAlone(t *testing.T) {
	plan, err := PlanRestore([]byte(`{"statusLine":{"type":"command","command":"mine.sh"}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() {
		t.Error("a statusLine the user replaced after install must not be touched")
	}
}

func TestApplyRefusesStalePlan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	os.WriteFile(path, []byte(`{}`), 0o600)
	plan, _ := PlanInstall([]byte(`{}`), shim)
	os.WriteFile(path, []byte(`{"statusLine":{"type":"command","command":"new.sh"}}`), 0o600)
	if err := Apply(path, plan); err == nil {
		t.Error("Apply should refuse a plan made against an older settings.json")
	}
}

func TestIsShim(t *testing.T) {
	if !IsShim([]byte(`{"command":"\"C:\\Program Files\\ShareCodex\\ShareCodex.exe\" statusline"}`)) {
		t.Error("Windows shim path not recognised")
	}
	if IsShim([]byte(`{"command":"~/bar.sh"}`)) {
		t.Error("user command recognised as shim")
	}
}
