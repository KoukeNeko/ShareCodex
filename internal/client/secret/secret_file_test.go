//go:build linux

package secret

import (
	"errors"
	"os"
	"testing"
)

func TestFileTokenRoundTrip(t *testing.T) {
	t.Setenv("SHARECODEX_HOME", t.TempDir())
	if _, err := Token("dev1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Token before SetToken = %v, want ErrNotFound", err)
	}
	if err := SetToken("dev1", "secret"); err != nil {
		t.Fatal(err)
	}
	p, _ := tokenPath("dev1")
	if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if got, err := Token("dev1"); err != nil || got != "secret" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	if err := DeleteToken("dev1"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteToken("dev1"); err != nil {
		t.Fatalf("deleting a missing token = %v", err)
	}
	if err := SetToken("../x", "secret"); err == nil {
		t.Error("SetToken accepted a device ID with a path separator")
	}
}

func TestFileClaudeLoginIsSeparateFromDeviceToken(t *testing.T) {
	t.Setenv("SHARECODEX_HOME", t.TempDir())
	if err := SetToken("abc", "device"); err != nil {
		t.Fatal(err)
	}
	if err := SetClaudeLogin("abc", "login"); err != nil {
		t.Fatal(err)
	}
	if got, err := Token("abc"); err != nil || got != "device" {
		t.Fatalf("device token = %q, %v; a sign-in with the same ID overwrote it", got, err)
	}
	if err := DeleteClaudeLogin("abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaudeLogin("abc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ClaudeLogin after delete = %v, want ErrNotFound", err)
	}
	if got, _ := Token("abc"); got != "device" {
		t.Fatal("deleting the sign-in removed the device token")
	}
}
