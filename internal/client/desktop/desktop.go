// Package desktop is the Wails shell: a tray (Windows) or menu bar (macOS)
// icon that toggles a popup window. All logic lives in the agent; this
// package only wires it to the window and the frontend bindings.
package desktop

import (
	"context"
	_ "embed"
	"io/fs"
	"runtime"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/KoukeNeko/ShareCodex/internal/client/agent"
)

// StateEvent carries a fresh agent.State to the frontend.
const StateEvent = "state"

//go:embed tray-template.png
var trayTemplateIcon []byte

//go:embed tray.png
var trayIcon []byte

func init() {
	application.RegisterEvent[agent.State](StateEvent)
}

func Run(ag *agent.Agent, assets fs.FS, executable string) error {
	svc := &Service{agent: ag, executable: executable}
	app := application.New(application.Options{
		Name:        "ShareCodex",
		Description: "Shared Claude Code and Codex quota",
		Services:    []application.Service{application.NewService(svc)},
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac:         application.MacOptions{ActivationPolicy: application.ActivationPolicyAccessory},
	})
	svc.app = app

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "popup",
		Width:           380,
		Height:          560,
		Frameless:       true,
		AlwaysOnTop:     true,
		Hidden:          true,
		DisableResize:   true,
		HideOnEscape:    true,
		HideOnFocusLost: true,
		Windows:         application.WindowsWindow{HiddenOnTaskbar: true},
		Mac: application.MacWindow{
			Backdrop: application.MacBackdropTranslucent,
		},
		URL: "/",
	})
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})
	// Refresh whenever the popup opens, so numbers are never stale on view.
	window.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) {
		ag.Refresh()
	})

	tray := app.SystemTray.New()
	tray.SetTooltip("ShareCodex")
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(trayTemplateIcon)
	} else {
		tray.SetIcon(trayIcon)
	}
	menu := app.NewMenu()
	menu.Add("重新整理").OnClick(func(*application.Context) { ag.Refresh() })
	menu.AddSeparator()
	menu.Add("結束 ShareCodex").OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(menu)
	tray.AttachWindow(window).WindowOffset(6)

	var pending = make(chan struct{}, 1)
	ag.OnChange = func() {
		select {
		case pending <- struct{}{}:
		default:
		}
	}
	svc.pending = pending
	return app.Run()
}

// Service is bound to the frontend; its exported methods become the
// generated TypeScript bindings.
type Service struct {
	agent      *agent.Agent
	app        *application.App
	executable string
	pending    chan struct{}
	cancel     context.CancelFunc
}

func (s *Service) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	ctx, s.cancel = context.WithCancel(ctx)
	go s.agent.Run(ctx)
	go s.emitChanges(ctx)
	return nil
}

func (s *Service) ServiceShutdown() error {
	if s.cancel != nil {
		s.cancel()
	}
	return s.agent.Close()
}

// emitChanges coalesces bursts of agent changes (a scan can touch many
// files) into at most a few state pushes per second.
func (s *Service) emitChanges(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.pending:
			s.app.Event.Emit(StateEvent, s.agent.State(ctx))
			time.Sleep(300 * time.Millisecond)
		}
	}
}

func (s *Service) State(ctx context.Context) agent.State {
	return s.agent.State(ctx)
}

func (s *Service) Join(ctx context.Context, link string) error {
	return s.agent.Join(ctx, link)
}

func (s *Service) Leave() error {
	return s.agent.Leave()
}

func (s *Service) Refresh() {
	s.agent.Refresh()
}

func (s *Service) InstallStatusLine() error {
	return s.agent.InstallStatusLine(s.executable)
}

func (s *Service) RestoreStatusLine() error {
	return s.agent.RestoreStatusLine()
}

func (s *Service) SetLaunchAtLogin(enabled bool) error {
	return s.agent.SetLaunchAtLogin(s.executable, enabled)
}

// OpenReleasePage opens the release the agent reported, never an arbitrary
// URL from the frontend.
func (s *Service) OpenReleasePage(ctx context.Context) error {
	rel := s.agent.State(ctx).Update
	if rel == nil {
		return nil
	}
	return s.app.Browser.OpenURL(rel.URL)
}

func (s *Service) Quit() {
	s.app.Quit()
}
