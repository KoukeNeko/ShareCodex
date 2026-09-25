package desktop

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
	write(t, filepath.Join(code, "acct-1", "org-a", "local_1.json"),
		`{"sessionId":"local_1","cliSessionId":"s1","createdAt":1000,"lastActivityAt":5000}`)
	// The same person signed into another organization later.
	write(t, filepath.Join(code, "acct-1", "org-b", "local_2.json"),
		`{"sessionId":"local_2","cliSessionId":"s2","createdAt":9000}`)
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
		if sessions[id].OrgID != org {
			t.Errorf("%s: org = %q, want %q", id, sessions[id].OrgID, org)
		}
	}
	if !sessions["s1"].LastActivity.Equal(time.UnixMilli(5000)) || !sessions["s2"].LastActivity.Equal(time.UnixMilli(9000)) {
		t.Errorf("last activity = %v, %v; want the later of lastActivityAt and createdAt",
			sessions["s1"].LastActivity, sessions["s2"].LastActivity)
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
	if _, ok := Latest(sessions); ok {
		t.Error("no sessions must have no latest")
	}
}

func TestLatest(t *testing.T) {
	sessions := map[string]Session{
		"old": {OrgID: "org-a", LastActivity: time.UnixMilli(1000)},
		"new": {OrgID: "org-b", LastActivity: time.UnixMilli(2000)},
	}
	if s, ok := Latest(sessions); !ok || s.OrgID != "org-b" {
		t.Errorf("latest = %+v, %v; want org-b", s, ok)
	}
}

func TestFromDesktop(t *testing.T) {
	for entrypoint, want := range map[string]bool{
		"claude-desktop": true, "claude-desktop-3p": true, "local-agent": true,
		"cli": false, "sdk-cli": false, "": false,
	} {
		if got := FromDesktop(entrypoint); got != want {
			t.Errorf("FromDesktop(%q) = %v, want %v", entrypoint, got, want)
		}
	}
}
