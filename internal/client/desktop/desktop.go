// Package desktop is the Wails shell: a tray (Windows) or menu bar (macOS)
// icon that toggles a popup window. All logic lives in the agent; this
// package only wires it to the window and the frontend bindings.
package desktop

import (
	"context"
	_ "embed"
	"errors"
	"io/fs"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/KoukeNeko/ShareCodex/internal/client/agent"
	"github.com/KoukeNeko/ShareCodex/internal/client/settings"
	"github.com/KoukeNeko/ShareCodex/internal/provider/anthropic/oauthusage"
)

// StateEvent carries a fresh agent.State to the frontend.
const StateEvent = "state"

const (
	popupWidth         = 380
	defaultPopupHeight = 560
	minPopupHeight     = 360
	popupCornerRadius  = 12

	// A widget starts this tall; its page then sizes it to its card.
	defaultWidgetHeight = 420
	minWidgetHeight     = 120
	maxWidgetHeight     = 1200
	// fitSettle is how long a widget's own resizes keep reporting.
	fitSettle = 500 * time.Millisecond
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
	acceptFirstMouse()
	// However the app ends, Wails runs this before closing its windows:
	// widgets closed by quitting stay pinned for the next launch.
	app.OnShutdown(func() { svc.quitting.Store(true) })

	height := ag.PopupHeight()
	if height < minPopupHeight {
		height = defaultPopupHeight
	}
	// Windows 11 draws the popup on Acrylic, like the system's own tray
	// flyouts; the query tells the page to go translucent over it.
	svc.background, svc.backdrop = application.BackgroundTypeTransparent, ""
	if acrylicSupported() {
		svc.background, svc.backdrop = application.BackgroundTypeTranslucent, "acrylic"
	}
	popupURL := "/"
	if svc.backdrop != "" {
		popupURL += "?backdrop=" + svc.backdrop
	}
	// Only the height is adjustable; the layout is designed for one width.
	opts := svc.floatingWindow("popup", height, minPopupHeight, popupURL)
	opts.Hidden = true
	opts.HideOnEscape = true
	opts.HideOnFocusLost = true
	window := app.Window.NewWithOptions(opts)
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

	for _, w := range ag.Widgets() {
		svc.openWidget(w)
	}

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

	// background and backdrop are how this system draws the popup, which
	// widgets share.
	background application.BackgroundType
	backdrop   string

	mu sync.Mutex
	// widgets are the open widgets by account ID.
	widgets map[string]*widget
	// quitting tells a widget closed by the app ending from one the user
	// closed, which unpins its account.
	quitting atomic.Bool
}

// widget is an open widget window.
type widget struct {
	win *application.WebviewWindow
	// fitted is the height the app last gave the window to fit its card,
	// at fittedAt. A resize the app made reports late, possibly after the
	// next one, so any other height is the user's only once the app's
	// resizes have settled.
	fitted   int
	fittedAt time.Time
	// sized is set once the user chose a height, which the window then
	// keeps instead of fitting its card.
	sized bool
	// settle and resized save the position and height once a drag or a
	// resize comes to rest.
	settle, resized *time.Timer
}

// floatingWindow is the frameless, always-on-top window the popup and the
// widgets are drawn in.
func (s *Service) floatingWindow(name string, height, minHeight int, url string) application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Name:        name,
		Width:       popupWidth,
		Height:      height,
		MinWidth:    popupWidth,
		MaxWidth:    popupWidth,
		MinHeight:   minHeight,
		Frameless:   true,
		AlwaysOnTop: true,
		// DWM draws caption buttons behind a translucent page unless the
		// system menu is gone; the windows close from their own pages.
		MinimiseButtonState: application.ButtonHidden,
		MaximiseButtonState: application.ButtonHidden,
		CloseButtonState:    application.ButtonHidden,
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: true,
			BackdropType:    application.Acrylic,
		},
		// The windows draw on AppKit's Liquid Glass (NSGlassEffectView on
		// macOS 26+, a visual effect view before that); the page itself is
		// transparent on macOS so the material shows through.
		BackgroundType:   s.background,
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
	}
}

// openWidget shows a pinned account in a window of its own, where it was
// last moved to and as tall as it was last made, or brings the open one
// forward.
func (s *Service) openWidget(w settings.Widget) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if open, ok := s.widgets[w.AccountID]; ok {
		open.win.Focus()
		return
	}
	q := url.Values{"widget": {w.AccountID}}
	if s.backdrop != "" {
		q.Set("backdrop", s.backdrop)
	}
	height := defaultWidgetHeight
	if w.Height > 0 {
		height = w.Height
	}
	opts := s.floatingWindow("widget:"+w.AccountID, height, minWidgetHeight, "/?"+q.Encode())
	if w.Placed {
		opts.InitialPosition, opts.X, opts.Y = application.WindowXY, w.X, w.Y
	}
	// On macOS a widget is a floating panel that does not activate the
	// app: the first click on it takes effect, and the app in front stays
	// in front.
	opts.Mac.WindowClass = application.MacWindowClassPanel
	opts.Mac.PanelPreferences = application.MacPanelPreferences{FloatingPanel: true, NonActivating: true}
	win := s.app.Window.NewWithOptions(opts)
	wg := &widget{win: win, fitted: height, fittedAt: time.Now(), sized: w.Height > 0}
	id := w.AccountID
	// A drag moves the window many times a second; its position is saved
	// once it comes to rest.
	win.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if wg.settle != nil {
			wg.settle.Stop()
		}
		wg.settle = time.AfterFunc(500*time.Millisecond, func() {
			x, y := win.Position()
			if err := s.agent.SetWidgetPosition(id, x, y); err != nil {
				s.app.Logger.Error("save widget position", "err", err)
			}
		})
	})
	win.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) {
		_, h := win.Size()
		s.mu.Lock()
		defer s.mu.Unlock()
		if !wg.sized && (h == wg.fitted || time.Since(wg.fittedAt) < fitSettle) {
			return
		}
		wg.sized = true
		if wg.resized != nil {
			wg.resized.Stop()
		}
		wg.resized = time.AfterFunc(500*time.Millisecond, func() {
			_, h := win.Size()
			if err := s.agent.SetWidgetHeight(id, h); err != nil {
				s.app.Logger.Error("save widget height", "err", err)
			}
		})
	})
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		s.mu.Lock()
		delete(s.widgets, id)
		s.mu.Unlock()
		if s.quitting.Load() {
			return
		}
		if err := s.agent.UnpinAccount(id); err != nil {
			s.app.Logger.Error("unpin account", "err", err)
		}
	})
	if s.widgets == nil {
		s.widgets = map[string]*widget{}
	}
	s.widgets[id] = wg
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

func (s *Service) LeaveAccount(ctx context.Context, accountID string) error {
	return s.agent.LeaveAccount(ctx, accountID)
}

func (s *Service) Refresh() {
	s.agent.Refresh()
}

// Resync re-reads this device's logs and uploads them again, replacing what
// was uploaded under older attribution rules.
func (s *Service) Resync(ctx context.Context) error {
	_, err := s.agent.Resync(ctx)
	return err
}

func (s *Service) InstallStatusLine() error {
	return s.agent.InstallStatusLine(s.executable)
}

func (s *Service) RestoreStatusLine() error {
	return s.agent.RestoreStatusLine()
}

// SignInClaude signs a Claude account in to ShareCodex in the browser, so
// its quota is read even where only Claude Desktop uses it. It returns once
// the sign-in finishes or is abandoned.
func (s *Service) SignInClaude(ctx context.Context) error {
	login, err := oauthusage.StartLogin()
	if err != nil {
		return err
	}
	if err := s.app.Browser.OpenURL(login.URL); err != nil {
		login.Close()
		return err
	}
	tokens, profile, err := login.Wait(ctx)
	if err != nil {
		return err
	}
	_, _, err = s.agent.LinkClaude(ctx, tokens, profile)
	return err
}

// SignOutClaude forgets a sign-in, named as State lists it.
func (s *Service) SignOutClaude(ctx context.Context, hint string) error {
	return s.agent.UnlinkClaude(ctx, hint)
}

func (s *Service) SetLaunchAtLogin(enabled bool) error {
	return s.agent.SetLaunchAtLogin(s.executable, enabled)
}

// OpenDashboard opens the server's public dashboard in the browser, once
// the admin has published it; never an arbitrary URL from the frontend.
func (s *Service) OpenDashboard(ctx context.Context) error {
	st := s.agent.State(ctx)
	if st.Overview == nil || !st.Overview.DashboardPublished {
		return errors.New("the dashboard is not published")
	}
	return s.app.Browser.OpenURL(strings.TrimRight(st.ServerURL, "/") + "/dashboard")
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

func (s *Service) SetFineChart(enabled bool) error {
	return s.agent.SetFineChart(enabled)
}

func (s *Service) SetAccountSort(sort string) error {
	return s.agent.SetAccountSort(sort)
}

func (s *Service) SetAccountOrder(ids []string) error {
	return s.agent.SetAccountOrder(ids)
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

// PinAccount pins an account to the screen in a widget of its own.
func (s *Service) PinAccount(accountID string) error {
	if err := s.agent.PinAccount(accountID); err != nil {
		return err
	}
	s.openWidget(settings.Widget{AccountID: accountID})
	return nil
}

// UnpinAccount closes an account's widget, which unpins it.
func (s *Service) UnpinAccount(accountID string) error {
	s.mu.Lock()
	wg := s.widgets[accountID]
	s.mu.Unlock()
	if wg == nil {
		return s.agent.UnpinAccount(accountID)
	}
	wg.win.Close()
	return nil
}

// SetWidgetHeight fits a widget's window to its card, but no taller than
// the screen it is on leaves room for; the widget scrolls past that. A
// widget the user resized keeps their height.
func (s *Service) SetWidgetHeight(accountID string, height int) {
	s.mu.Lock()
	wg := s.widgets[accountID]
	s.mu.Unlock()
	if wg == nil {
		return
	}
	limit := maxWidgetHeight
	if screen, err := wg.win.GetScreen(); err == nil && screen != nil && screen.WorkArea.Height > 0 {
		limit = min(limit, screen.WorkArea.Height)
	}
	height = max(minWidgetHeight, min(limit, height))
	s.mu.Lock()
	if wg.sized {
		s.mu.Unlock()
		return
	}
	wg.fitted, wg.fittedAt = height, time.Now()
	s.mu.Unlock()
	wg.win.SetSize(popupWidth, height)
}

func (s *Service) Quit() {
	s.app.Quit()
}
