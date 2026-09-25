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

const (
	popupWidth         = 380
	defaultPopupHeight = 560
	minPopupHeight     = 360
	popupCornerRadius  = 12
)

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

	height := ag.PopupHeight()
	if height < minPopupHeight {
		height = defaultPopupHeight
	}
	// Windows 11 draws the popup on Acrylic, like the system's own tray
	// flyouts; the query tells the page to go translucent over it.
	background, url := application.BackgroundTypeTransparent, "/"
	if acrylicSupported() {
		background, url = application.BackgroundTypeTranslucent, "/?backdrop=acrylic"
	}
	// Only the height is adjustable; the layout is designed for one width.
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            "popup",
		Width:           popupWidth,
		Height:          height,
		MinWidth:        popupWidth,
		MaxWidth:        popupWidth,
		MinHeight:       minPopupHeight,
		Frameless:       true,
		AlwaysOnTop:     true,
		Hidden:          true,
		HideOnEscape:    true,
		HideOnFocusLost: true,
		// DWM draws caption buttons behind a translucent page unless the
		// system menu is gone; the popup closes via Escape or focus loss.
		MinimiseButtonState: application.ButtonHidden,
		MaximiseButtonState: application.ButtonHidden,
		CloseButtonState:    application.ButtonHidden,
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: true,
			BackdropType:    application.Acrylic,
		},
		// The popup draws on AppKit's Liquid Glass (NSGlassEffectView on
		// macOS 26+, a visual effect view before that); the page itself is
		// transparent on macOS so the material shows through.
		BackgroundType:   background,
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		Mac: application.MacWindow{
			Backdrop:     application.MacBackdropLiquidGlass,
			CornerRadius: popupCornerRadius,
			LiquidGlass: application.MacLiquidGlass{
				Style:        application.LiquidGlassStyleAutomatic,
				Material:     application.NSVisualEffectMaterialAuto,
				CornerRadius: popupCornerRadius,
			},
		},
		URL: url,
	})
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})
	// Refresh whenever the popup opens, so numbers are never stale on view.
	window.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) {
		ag.Refresh()
	})
	window.OnWindowEvent(events.Common.WindowHide, func(*application.WindowEvent) {
		_, h := window.Size()
		ag.SetPopupHeight(h)
	})

	tray := app.SystemTray.New()
	tray.SetTooltip("ShareCodex")
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(trayTemplateIcon)
	} else {
		tray.SetIcon(trayIcon)
	}
	menu := app.NewMenu()
	svc.menu = menu
	svc.refreshItem = menu.Add("").OnClick(func(*application.Context) { ag.Refresh() })
	menu.AddSeparator()
	svc.quitItem = menu.Add("").OnClick(func(*application.Context) { app.Quit() })
	svc.labelMenu(ag.Language())
	tray.SetMenu(menu)
	// Wails multiplies the offset by the display scale on macOS, whose window
	// coordinates are already in points, so a Retina screen doubles it. Keep
	// the popup directly under the menu bar there, like a native menu.
	offset := 6
	if runtime.GOOS == "darwin" {
		offset = 0
	}
	tray.AttachWindow(window).WindowOffset(offset)

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
// trayLabels are the tray menu's strings; the popup's live in the frontend.
var trayLabels = map[string]struct{ refresh, quit string }{
	"en":    {"Refresh", "Quit ShareCodex"},
	"zh-TW": {"重新整理", "結束 ShareCodex"},
}

type Service struct {
	agent      *agent.Agent
	app        *application.App
	executable string
	pending    chan struct{}

	menu        *application.Menu
	refreshItem *application.MenuItem
	quitItem    *application.MenuItem
	cancel      context.CancelFunc
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

// CreateInvite makes a join link for another device and copies it, since
// the popup has no Edit menu for Cmd+C.
func (s *Service) CreateInvite(ctx context.Context) (agent.Invite, error) {
	inv, err := s.agent.CreateInvite(ctx)
	if err != nil {
		return agent.Invite{}, err
	}
	s.app.Clipboard.SetText(inv.Link)
	return inv, nil
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

// SetLanguage switches the UI language, including the tray menu.
func (s *Service) SetLanguage(lang string) error {
	if err := s.agent.SetLanguage(lang); err != nil {
		return err
	}
	s.labelMenu(lang)
	return nil
}

func (s *Service) labelMenu(lang string) {
	labels, ok := trayLabels[lang]
	if !ok {
		labels = trayLabels["en"]
	}
	s.refreshItem.SetLabel(labels.refresh)
	s.quitItem.SetLabel(labels.quit)
	s.menu.Update()
}

func (s *Service) Quit() {
	s.app.Quit()
}
