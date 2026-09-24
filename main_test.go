package main

import "testing"

func TestHomebrewOptPath(t *testing.T) {
	got, ok := homebrewOptPath("/home/linuxbrew/.linuxbrew/Cellar/sharecodex-cli/0.5.0/bin/sharecodex")
	if want := "/home/linuxbrew/.linuxbrew/opt/sharecodex-cli/bin/sharecodex"; !ok || got != want {
		t.Errorf("homebrewOptPath = %q, %v; want %q", got, ok, want)
	}
	if got, ok := homebrewOptPath("/home/a/.local/bin/sharecodex"); ok {
		t.Errorf("homebrewOptPath outside the Cellar = %q", got)
	}
}
