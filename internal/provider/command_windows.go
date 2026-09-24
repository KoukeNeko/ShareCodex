package provider

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideConsole gives the child a console without a window. Its own children
// inherit that console, so they stay hidden too.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
