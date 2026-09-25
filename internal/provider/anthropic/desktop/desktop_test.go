package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsMapsEachSessionToItsOrganization(t *testing.T) {
	dir := t.TempDir()
	code := filepath.Join(dir, "claude-code-sessions")
	write(t, filepath.Join(code, "acct-1", "org-a", "local_1.json"), `{"sessionId":"local_1","cliSessionId":"s1"}`)
	// The same person signed into another organization later.
	write(t, filepath.Join(code, "acct-1", "org-b", "local_2.json"), `{"sessionId":"local_2","cliSessionId":"s2"}`)
	// Not session metadata, or not usable.
	write(t, filepath.Join(code, "acct-1", "org-a", "scheduled-tasks.json"), `{"scheduledTasks":[]}`)
	write(t, filepath.Join(code, "acct-1", "org-a", "local_3.json"), `{"sessionId":"local_3"}`)
	write(t, filepath.Join(code, "acct-1", "org-a", "local_4.json"), `not json`)

	cowork := filepath.Join(dir, "local-agent-mode-sessions", "acct-1", "org-c", "local_5", ".claude", "projects")
	write(t, filepath.Join(cowork, "-sessions-output", "s5.jsonl"), "{}\n")
	write(t, filepath.Join(cowork, "..", "..", "audit.jsonl"), "{}\n")

	sessions, err := Sessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"s1": "org-a", "s2": "org-b", "s5": "org-c"}
	if len(sessions) != len(want) {
		t.Fatalf("sessions = %+v, want %v", sessions, want)
	}
	for id, org := range want {
		if sessions[id] != org {
			t.Errorf("%s: org = %q, want %q", id, sessions[id], org)
		}
	}
	if roots := CoworkRoots(dir); len(roots) != 1 || roots[0] != cowork {
		t.Errorf("Cowork roots = %v, want %s", roots, cowork)
	}
}

func TestSessionsWithoutDesktop(t *testing.T) {
	sessions, err := Sessions(filepath.Join(t.TempDir(), "missing"))
	if err != nil || len(sessions) != 0 {
		t.Fatalf("sessions = %v, %v; want none", sessions, err)
	}
}

func TestEntrypoints(t *testing.T) {
	for entrypoint, want := range map[string][2]bool{
		"claude-desktop": {true, false}, "local-agent": {true, false}, "claude-desktop-3p": {false, true},
		"cli": {false, false}, "sdk-cli": {false, false}, "": {false, false},
	} {
		if got := [2]bool{FromDesktop(entrypoint), ThirdParty(entrypoint)}; got != want {
			t.Errorf("%q: FromDesktop, ThirdParty = %v, want %v", entrypoint, got, want)
		}
	}
}
