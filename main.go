// Command sharecodex is the desktop client. Without arguments it runs the
// tray app; subcommands serve tooling that must not start the GUI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/KoukeNeko/ShareCodex/internal/client/agent"
	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/statusline"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usageText = `usage:
  sharecodex                 run the tray app
  sharecodex join LINK       join a server with an admin's join link
  sharecodex agent           run the background agent without a window
  sharecodex scan [--json]   print daily token totals from local logs
  sharecodex statusline      Claude Code statusLine shim (installed by the app)`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		if err := runDesktop(); err != nil {
			fmt.Fprintln(os.Stderr, "sharecodex:", err)
			os.Exit(1)
		}
		return
	}

	var err error
	switch args[0] {
	case "statusline":
		runStatusLine()
	case "scan":
		err = runScan(args[1:])
	case "agent":
		err = runAgent()
	case "join":
		if len(args) != 2 {
			err = errors.New(usageText)
			break
		}
		err = runJoin(args[1])
	default:
		err = errors.New(usageText)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sharecodex:", err)
		os.Exit(1)
	}
}

// runStatusLine must stay fast and never fail loudly: Claude Code runs it on
// every status refresh.
func runStatusLine() {
	dir, err := settings.Dir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sharecodex statusline:", err)
		return
	}
	st, err := settings.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sharecodex statusline:", err)
	}
	statusline.Run(os.Stdin, os.Stdout, os.Stderr, filepath.Join(dir, "claude-statusline"), chainCommand(st))
}

// chainCommand is the user's own statusLine command saved at install time.
func chainCommand(st settings.Settings) string {
	if st.StatusLine == nil {
		return ""
	}
	var original struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(st.StatusLine.Original, &original) != nil {
		return ""
	}
	return original.Command
}

func newAgent(ctx context.Context) (*agent.Agent, error) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return agent.New(ctx, version, log)
}

func runAgent() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := newAgent(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	a.Run(ctx)
	return nil
}

func runJoin(link string) error {
	ctx := context.Background()
	a, err := newAgent(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.Join(ctx, link); err != nil {
		return err
	}
	st := a.State(ctx)
	fmt.Printf("Joined %s as %s (%s)\n", st.ServerURL, st.PersonName, st.DeviceName)
	return nil
}
