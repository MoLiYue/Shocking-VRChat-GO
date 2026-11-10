// Package app wires everything together: settings, devices, OSC engine,
// HTTP API, live WebSocket push and background services.
package app

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"shockingvrc/internal/config"
	"shockingvrc/internal/device"
	"shockingvrc/internal/handler"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/netguard"
	"shockingvrc/internal/queue"
	"shockingvrc/internal/wave"
)

// Version is the application version (overridable with -ldflags -X).
var Version = "0.7.15"

// GitHubRepo is the release source for the auto-updater.
var GitHubRepo = "MoLiYue/Shocking-VRChat-GO"

// App is the whole running program.
type App struct {
	BaseDir string
	Cfg     *config.Store
	Hub     *device.Hub
	Lib     *wave.Library
	Queue   *queue.Queue
	V4      *device.V4Relay
	Live    *LiveHub
	Engine  *Engine
	// Guard rejects cross-site and DNS-rebinding requests on both servers.
	Guard *netguard.Guard

	serverIPMu sync.RWMutex
	serverIP   string

	waves    *waveHistory
	activity *oscActivity

	boostMu     sync.Mutex
	boost       map[string]int
	boostExpire map[string]time.Time

	waveTestMu sync.Mutex
	waveTest   waveTestState

	overlimitMu        sync.Mutex
	overlimitRules     []any
	overlimitState     []overlimitRuleState
	oscParamState      map[string]any
	oscParamSeen       map[string]time.Time
	overlimitEffective map[string]int

	curveMu sync.RWMutex
	curves  map[string][]handler.Point

	ogbMu   sync.Mutex
	ogbSeen map[[2]string]float64

	rec  *recorder
	play *player

	chatbox *chatboxSender
	monitor *paramMonitor
	// avatarID is the last /avatar/change id (for share cards).
	avatarID atomic.Value
	monStop  chan struct{}

	upd *updater

	// OnShutdown is called (once) when the program should exit, e.g. to
	// close the WebView window. It must cause Run() in main to return.
	OnShutdown   func()
	shutdownOnce sync.Once
	// Packaged reports whether we run as a distributed binary (enables update).
	Packaged bool
	// TrayAvailable is reported to the UI so it can offer minimize-to-tray.
	TrayAvailable bool
	// OnShowWindow brings the UI to the front (second instance / tray).
	OnShowWindow func()
	// OnTheme applies the UI theme to native chrome (title bar).
	OnTheme func(dark bool)
}

// New constructs the application rooted at baseDir.
func New(baseDir string) *App {
	a := &App{
		BaseDir:            baseDir,
		Cfg:                config.NewStore(baseDir),
		Hub:                device.NewHub(),
		Queue:              queue.New(0),
		Live:               newLiveHub(),
		waves:              newWaveHistory(),
		activity:           &oscActivity{},
		boost:              map[string]int{"A": 0, "B": 0},
		boostExpire:        map[string]time.Time{},
		oscParamState:      map[string]any{},
		oscParamSeen:       map[string]time.Time{},
		overlimitEffective: map[string]int{"A": 0, "B": 0},
		curves:             map[string][]handler.Point{},
		ogbSeen:            map[[2]string]float64{},
		rec:                &recorder{},
		play:               &player{},
		upd:                newUpdater(),
		Guard:              netguard.New("web"),
	}
	a.waveTest = waveTestState{Channel: "A", Strength: 50, WaveScale: 1.0}
	a.V4 = device.NewV4Relay(a.Hub, func() bool { return config.Bool(a.Cfg.Get("ws", "v4_enabled"), true) })
	a.Engine = &Engine{a: a}
	a.chatbox = &chatboxSender{a: a}
	a.monitor = newParamMonitor(a)
	a.monStop = make(chan struct{})
	a.Hub.WaveObserver = a.recordWave
	a.Hub.ClearObserver = a.waves.clear
	logx.SetObserver(func(e logx.Entry) { a.Live.Broadcast("log", e) })
	return a
}

// Path joins p onto the base directory.
func (a *App) Path(p ...string) string { return filepath.Join(append([]string{a.BaseDir}, p...)...) }

// Init loads configuration and auxiliary files. First run writes defaults.
func (a *App) Init() error {
	logx.Info("[init] Loading config: %s, %s", config.FileBasic, config.FileAdvanced)
	err := a.Cfg.Init()
	if err == config.ErrInited {
		logx.Success("[init] No config file found. Default config created; starting normally...")
		err = a.Cfg.Init()
	}
	if err != nil {
		return err
	}
	a.applyLogLevel()
	a.refreshAllowedHosts()
	a.refreshServerIP()
	a.Lib = wave.NewLibrary(a.Path("wave_presets"))
	a.refreshLimits()
	a.loadCurves()
	a.loadOverlimitRules()
	a.monitor.load()
	go a.monitor.saveLoop(a.monStop)
	logx.Success("[init] Config loaded. WS needs external access - allow firewall prompt if shown.")
	return nil
}

func (a *App) refreshAllowedHosts() {
	var hosts []string
	for _, h := range config.AsList(a.Cfg.Get("web_server", "allowed_hosts")) {
		hosts = append(hosts, config.Str(h, ""))
	}
	a.Guard.SetExtraHosts(hosts)
}

func (a *App) applyLogLevel() {
	logx.SetLevel(logx.ParseLevel(config.Str(a.Cfg.Get("log_level"), "INFO")))
}

// limitsFromSettings reads strength caps from the advanced tree.
func (a *App) limitsFromSettings() device.Limits {
	var l device.Limits
	a.Cfg.Read(func(adv, _ config.M) {
		l.LimitA = config.Int(config.Get(adv, "dglab3", "channel_a", "strength_limit"), 5)
		l.LimitB = config.Int(config.Get(adv, "dglab3", "channel_b", "strength_limit"), 5)
	})
	return l
}

func (a *App) refreshLimits() { a.Hub.RefreshLimits(a.limitsFromSettings()) }

// UIThemeDark reports whether the last UI theme was dark.
func (a *App) UIThemeDark() bool {
	return config.Str(a.Cfg.Get("general", "ui_theme"), "dark") != "light"
}

// UILocale is the language last chosen in the UI ("zh" or "en"), used for
// native strings such as the tray menu.
func (a *App) UILocale() string {
	if config.Str(a.Cfg.Get("general", "ui_locale"), "zh") == "en" {
		return "en"
	}
	return "zh"
}

// CurrentIP detects the LAN IP by "connecting" a UDP socket.
func (a *App) CurrentIP() string {
	host := config.Str(a.Cfg.Get("general", "local_ip_detect", "host"), "223.5.5.5")
	port := config.Int(a.Cfg.Get("general", "local_ip_detect", "port"), 80)
	if c, err := net.Dial("udp", net.JoinHostPort(host, strconv.Itoa(port))); err == nil {
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).IP.String()
	}
	addrs, _ := net.InterfaceAddrs()
	for _, ad := range addrs {
		if ipn, ok := ad.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	return "127.0.0.1"
}

func (a *App) refreshServerIP() {
	ip := config.Str(a.Cfg.Get("SERVER_IP"), "")
	if ip == "" {
		ip = a.CurrentIP()
	}
	a.serverIPMu.Lock()
	a.serverIP = ip
	a.serverIPMu.Unlock()
}

// ServerIP returns the IP advertised in QR codes.
func (a *App) ServerIP() string {
	a.serverIPMu.RLock()
	defer a.serverIPMu.RUnlock()
	return a.serverIP
}

// QRContent is the V3 DG-LAB socket QR payload.
func (a *App) QRContent() string {
	return fmt.Sprintf("https://www.dungeon-lab.com/app-download.php#DGLAB-SOCKET#ws://%s:%d/%s",
		a.ServerIP(), config.Int(a.Cfg.Get("ws", "listen_port"), 28846), config.Str(a.Cfg.Get("ws", "master_uuid"), ""))
}

// QRContentV4 is the DG-LAB 4 APP QR payload (relay on the web port).
func (a *App) QRContentV4() string {
	ws := fmt.Sprintf("ws://%s:%d/ws/dglab-v4?tid=%s",
		a.ServerIP(), config.Int(a.Cfg.Get("web_server", "listen_port"), 8800), a.V4.ControllerID)
	return "https://dungeon-lab.cn/s/?v=1&action=socket&url=" + url.QueryEscape(ws)
}

// WebURL is the local URL of the management UI.
func (a *App) WebURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", config.Int(a.Cfg.Get("web_server", "listen_port"), 8800))
}

// Shutdown stops everything and invokes OnShutdown.
func (a *App) Shutdown() {
	a.shutdownOnce.Do(func() {
		logx.Info("[shutdown] Graceful shutdown initiated...")
		a.chatbox.stop()
		a.Engine.Stop(false)
		close(a.monStop)
		if err := a.monitor.save(); err != nil {
			logx.Warn("[monitor] Save failed: %v", err)
		}
		a.Hub.CloseAll()
		if a.OnShutdown != nil {
			a.OnShutdown()
		}
	})
}

// RestartProcess re-executes the program (used when the web port changes).
// The new process waits for this one to exit (SVRC_RESTART=<pid>) before
// binding ports and opening its window; this one shuts down normally so its
// WebView2 instance closes cleanly instead of being killed by os.Exit.
func (a *App) RestartProcess() {
	exe, err := os.Executable()
	if err != nil {
		logx.Error("[restart] %v", err)
		return
	}
	_ = os.Setenv("SVRC_RESTART", strconv.Itoa(os.Getpid()))
	if err := spawnDetached(exe, os.Args[1:]); err != nil {
		_ = os.Unsetenv("SVRC_RESTART")
		logx.Error("[restart] spawn failed: %v", err)
		return
	}
	logx.Info("[restart] New process started, shutting down this one.")
	a.Shutdown()
}
