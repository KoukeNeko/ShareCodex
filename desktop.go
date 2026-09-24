package main

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/KoukeNeko/ShareCodex/internal/client/agent"
	"github.com/KoukeNeko/ShareCodex/internal/client/desktop"
	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
)

//go:embed all:frontend/dist
var frontend embed.FS

func runDesktop() error {
	dir, err := settings.Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "sharecodex.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(logFile, nil))

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Scoop runs the app through its `current` junction; resolving it would
	// pin the statusLine hook and login item to a versioned folder that
	// `scoop cleanup` deletes after an update.
	if runtime.GOOS != "windows" {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
	}

	ag, err := agent.New(context.Background(), version, log)
	if err != nil {
		return err
	}
	ag.RepairInstalledPaths(exe)
	assets, err := fs.Sub(frontend, "frontend/dist")
	if err != nil {
		return err
	}
	return desktop.Run(ag, assets, exe)
}
