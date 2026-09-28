// Command sharecodex is the desktop client. Without arguments it runs the
// tray app; subcommands serve tooling that must not start the GUI.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/KoukeNeko/ShareCodex/internal/client/agent"
	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/oauthusage"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/statusline"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usageText = `usage:
  sharecodex                           run the tray app (macOS and Windows)
  sharecodex join LINK                 join a server with a join link
  sharecodex invite                    create a join link for another of your devices
  sharecodex status                    show this device's join and capture settings
  sharecodex agent                     run the background agent without a window
  sharecodex autostart on|off          run the agent at login (a systemd user service on Linux)
  sharecodex statusline-capture on|off capture Claude's quota through Claude Code's statusLine
  sharecodex scan [--json]             print daily token totals from local logs
  sharecodex resync                    re-read local logs and upload the result again
  sharecodex claude-login              sign a Claude account in to ShareCodex to read its quota
  sharecodex claude-logout ACCOUNT     forget a Claude sign-in, as listed by status
  sharecodex statusline                Claude Code statusLine shim (installed by the above)`

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
	case "status":
		err = runStatus()
	case "resync":
		err = runResync()
	case "claude-login":
		err = runClaudeLogin()
	case "claude-logout":
		if len(args) != 2 {
			err = errors.New(usageText)
			break
		}
		err = runClaudeLogout(args[1])
	case "invite":
		err = runInvite()
	case "autostart", "statusline-capture":
		if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
			err = errors.New(usageText)
			break
		}
		err = runToggle(args[0], args[1] == "on")
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
	// A running Linux service read the old join at startup; restart it.
	if runtime.GOOS == "linux" && st.LaunchAtLogin {
		exe, err := executable()
		if err != nil {
			return err
		}
		return a.SetLaunchAtLogin(exe, true)
	}
	return nil
}

func runInvite() error {
	ctx := context.Background()
	a, err := newAgent(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	inv, err := a.CreateInvite(ctx)
	if err != nil {
		return err
	}
	fmt.Println(inv.Link)
	fmt.Printf("Single use, valid until %s. Run `sharecodex join` with it on the other device.\n",
		inv.ExpiresAt.Local().Format("2006-01-02 15:04"))
	return nil
}

func runStatus() error {
	ctx := context.Background()
	a, err := newAgent(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	st := a.State(ctx)
	if !st.Paired {
		fmt.Println("Not joined. Run `sharecodex join LINK` with a join link from the server's admin or another of your devices.")
	} else {
		fmt.Printf("Server:  %s\nMember:  %s\nDevice:  %s\n", st.ServerURL, st.PersonName, st.DeviceName)
	}
	fmt.Printf("Pending uploads:    %d\n", st.PendingUploads)
	fmt.Printf("statusLine capture: %s\n", onOff(st.StatusLineInstalled))
	fmt.Printf("Autostart:          %s\n", onOff(st.LaunchAtLogin))
	logins, err := a.ClaudeLogins(ctx)
	if err != nil {
		return err
	}
	for i, l := range logins {
		label := "Claude sign-ins:   "
		if i > 0 {
			label = "                   "
		}
		fmt.Printf("%s %s (%s)\n", label, l.Hint, l.PlanType)
	}
	return nil
}

// runClaudeLogin signs a Claude account in to ShareCodex, the way Claude
// Code signs in, so its quota is read even where only Claude Desktop uses it.
func runClaudeLogin() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := newAgent(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	login, err := oauthusage.StartLogin()
	if err != nil {
		return err
	}
	fmt.Printf("Open this page and approve the sign-in:\n\n  %s\n\n", login.URL)
	fmt.Println("If the browser is on another machine, paste the code the page shows here and press Enter.")
	openBrowser(login.URL)
	go func() {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err == nil || line != "" {
			login.Submit(line)
		}
	}()
	tokens, profile, err := login.Wait(ctx)
	if err != nil {
		return err
	}
	l, buckets, err := a.LinkClaude(ctx, tokens, profile)
	if err != nil {
		return err
	}
	fmt.Printf("Signed in: %s (%s)\n", l.Hint, l.PlanType)
	for _, b := range buckets {
		if b.UsedPercent != nil {
			fmt.Printf("  %-9s %.0f%%\n", b.Key, *b.UsedPercent)
		}
	}
	return nil
}

func runClaudeLogout(hint string) error {
	ctx := context.Background()
	a, err := newAgent(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.UnlinkClaude(ctx, hint); err != nil {
		return err
	}
	fmt.Printf("Signed out: %s\n", hint)
	return nil
}

// openBrowser shows the sign-in page where there is a desktop to show it on;
// over SSH the printed link is used instead.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "sharecodex: open browser:", err)
	}
}

// runResync throws away this device's ledger and reads its logs again, which
// is how events uploaded under older attribution rules are replaced. The app
// must not be running: both processes would open the same database.
func runResync() error {
	ctx := context.Background()
	a, err := newAgent(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	if st := a.State(ctx); st.Paired {
		fmt.Printf("Re-reading logs for %s (%s). Quit ShareCodex first; two processes must not share its database.\n",
			st.PersonName, st.DeviceName)
	}
	stats, err := a.Resync(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Events re-read: %d\nUploaded:       %d pending\n", stats.Events, stats.Pending)
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func runToggle(setting string, enabled bool) error {
	ctx := context.Background()
	a, err := newAgent(ctx)
	if err != nil {
		return err
	}
	defer a.Close()
	exe, err := executable()
	if err != nil {
		return err
	}
	switch {
	case setting == "autostart":
		err = a.SetLaunchAtLogin(exe, enabled)
	case enabled:
		err = a.InstallStatusLine(exe)
	default:
		err = a.RestoreStatusLine()
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", setting, onOff(enabled))
	if setting == "autostart" && enabled && runtime.GOOS == "linux" && !lingering() {
		fmt.Println("The agent stops when you log out. To keep it running, run `loginctl enable-linger`.")
	}
	return nil
}

// lingering reports whether systemd keeps this user's services running
// after their last session ends.
func lingering() bool {
	u, err := user.Current()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join("/var/lib/systemd/linger", u.Username))
	return err == nil
}

// executable is the path the statusLine hook and login item point at.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Scoop runs the app through its `current` junction; resolving it would
	// pin the statusLine hook and login item to a versioned folder that
	// `scoop cleanup` deletes after an update.
	if runtime.GOOS != "windows" {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
	}
	// Likewise, `brew upgrade` removes the versioned Cellar folder, while
	// Homebrew's opt link always points at the installed version.
	if opt, ok := homebrewOptPath(exe); ok {
		if _, err := os.Stat(opt); err == nil {
			exe = opt
		}
	}
	return exe, nil
}

// homebrewFormula is the Linux formula in KoukeNeko/homebrew-tap.
const homebrewFormula = "sharecodex-cli"

// homebrewOptPath maps <prefix>/Cellar/sharecodex-cli/<version>/bin/sharecodex
// to <prefix>/opt/sharecodex-cli/bin/sharecodex.
func homebrewOptPath(exe string) (string, bool) {
	sep := string(filepath.Separator)
	prefix, rest, ok := strings.Cut(exe, sep+"Cellar"+sep+homebrewFormula+sep)
	if !ok {
		return "", false
	}
	_, inVersion, ok := strings.Cut(rest, sep)
	if !ok {
		return "", false
	}
	return filepath.Join(prefix, "opt", homebrewFormula, inVersion), true
}
