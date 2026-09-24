package provider

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCommandHidesConsoleWindow(t *testing.T) {
	cmd := Command(context.Background(), "cmd", "/C", "echo", "hidden")
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 || !cmd.SysProcAttr.HideWindow {
		t.Fatalf("SysProcAttr = %+v, want CREATE_NO_WINDOW and HideWindow", cmd.SysProcAttr)
	}
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		t.Fatalf("hidden command must still return output: %q, %v", out, err)
	}
}
