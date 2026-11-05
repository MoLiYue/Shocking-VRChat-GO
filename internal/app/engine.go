package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"shockingvrc/internal/config"
	"shockingvrc/internal/device"
	"shockingvrc/internal/handler"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/osc"
	"shockingvrc/internal/queue"
	"shockingvrc/internal/sps"
	"shockingvrc/internal/wave"
)

// Engine runs the OSC listener, DG-LAB V3 WS server, command queue and handlers.
// The WS server lifecycle is independent so restarts keep device connections.
type Engine struct {
	a        *App
	opMu     sync.Mutex // serializes start/stop/restart
	stateMu  sync.Mutex
	running  bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	oscSrv   *osc.Server
	ws       *device.V3Server
	handlers []*handler.Shock
}

// Running reports whether the engine is started.
func (e *Engine) Running() bool {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	return e.running
}

// Handlers returns the active handlers.
func (e *Engine) Handlers() []*handler.Shock {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	return append([]*handler.Shock{}, e.handlers...)
}

// build creates the routing table. Handlers in reuse (keyed "A/shock")
// are kept, so their in-flight state survives a reconfiguration.
func (e *Engine) build(reuse map[string]*handler.Shock) (*osc.Dispatcher, []*handler.Shock) {
	a := e.a
	d := &osc.Dispatcher{Hook: a.oscGlobalHook}
	deps := &handler.Deps{
		Cfg: a.Cfg, Hub: a.Hub, Lib: a.Lib, Queue: a.Queue,
		ActivityObserver: a.recordOSCActivity,
		CurveGetter:      a.curvePoints,
	}
	var hs []*handler.Shock
	for _, ch := range []string{"A", "B"} {
		byMode := map[string]*handler.Shock{}
		active := 0
		params := config.AsList(a.Cfg.Get("dglab3", config.ChannelKey(ch), "avatar_params"))
		for _, p := range params {
			pm := config.AsMap(p)
			if pm == nil {
				continue
			}
			path := config.Str(pm["path"], "")
			if !config.Bool(pm["enabled"], true) {
				logx.Debug("[engine] Channel %s skipping disabled param: %s", ch, path)
				continue
			}
			mode := config.Str(pm["mode"], "distance")
			h := byMode[mode]
			if h == nil {
				if old := reuse[ch+"/"+mode]; old != nil {
					h = old
				} else {
					var err error
					h, err = handler.New(deps, ch, mode)
					if err != nil {
						logx.Error("[engine] Channel %s param %s: %v", ch, path, err)
						continue
					}
				}
				byMode[mode] = h
				hs = append(hs, h)
			}
			logx.Debug("[engine] Channel %s mode %s listening %s", ch, mode, path)
			d.Map(path, h.OSC)
			active++
		}
		logx.Info("[engine] Channel %s: %d active params", ch, active)
	}
	return d, hs
}

// Reconfigure applies avatar_params changes without restarting: the OSC
// socket and WS server stay up, the routing table is swapped atomically,
// unchanged handlers keep their state, only added/removed ones start/stop.
func (e *Engine) Reconfigure() {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	if !e.Running() {
		e.start()
		return
	}
	e.stateMu.Lock()
	old := map[string]*handler.Shock{}
	for _, h := range e.handlers {
		old[h.Channel+"/"+h.Mode] = h
	}
	srv := e.oscSrv
	e.stateMu.Unlock()

	d, hs := e.build(old)
	keep := map[*handler.Shock]bool{}
	for _, h := range hs {
		if old[h.Channel+"/"+h.Mode] == h {
			keep[h] = true
		} else {
			h.Start()
		}
	}
	srv.SetDispatcher(d)
	e.stateMu.Lock()
	e.handlers = hs
	e.stateMu.Unlock()
	stopped := 0
	for _, h := range old {
		if !keep[h] {
			h.Stop()
			stopped++
		}
	}
	logx.Success("[engine] Parameters applied live (%d handlers, %d new, %d removed)",
		len(hs), len(hs)-len(keep), stopped)
}

// Start starts the engine (no-op if running).
func (e *Engine) Start() {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.start()
}

func (e *Engine) start() {
	if e.Running() {
		logx.Warn("[engine] Already running, stop first.")
		return
	}
	a := e.a
	disp, hs := e.build(nil)
	a.Queue.Clear()
	ctx, cancel := context.WithCancel(context.Background())

	host := config.Str(a.Cfg.Get("osc", "listen_host"), "127.0.0.1")
	port := config.Int(a.Cfg.Get("osc", "listen_port"), 9001)
	srv, err := osc.Listen(host, port, disp)
	if err != nil {
		cancel()
		logx.Error("[engine] OSC listen failed, possible port conflict: %v", err)
		return
	}
	logx.Success("[engine] OSC Listening: %s:%d", host, port)

	loops := []func(context.Context){
		a.commandQueueProcessor, a.strengthBoostChecker, a.waveTestLoop,
		a.statusPusher, a.ogbHeartbeat,
	}
	for _, f := range loops {
		e.wg.Add(1)
		go func(f func(context.Context)) {
			defer e.wg.Done()
			f(ctx)
		}(f)
	}
	for _, h := range hs {
		h.Start()
	}
	e.stateMu.Lock()
	e.cancel, e.oscSrv, e.handlers = cancel, srv, hs
	wsUp := e.ws != nil
	e.stateMu.Unlock()
	if !wsUp {
		e.startWS()
	}
	e.stateMu.Lock()
	e.running = true
	e.stateMu.Unlock()
	logx.Info("[engine] Started.")
}

func (e *Engine) startWS() {
	a := e.a
	host := config.Str(a.Cfg.Get("ws", "listen_host"), "0.0.0.0")
	port := config.Int(a.Cfg.Get("ws", "listen_port"), 28846)
	s := device.NewV3Server(a.Hub, func() string { return config.Str(a.Cfg.Get("ws", "master_uuid"), "") })
	// No browser guard here: this port only speaks the DG-LAB device
	// protocol (bound by master_uuid), and the APP may send an Origin
	// header that the cross-site check would reject.
	if err := s.Start(host, port); err != nil {
		logx.Error("[engine] WS server listen failed, possible port conflict: %v", err)
		return
	}
	e.stateMu.Lock()
	e.ws = s
	e.stateMu.Unlock()
	logx.Success("[engine] WS Listening: %s:%d", host, port)
}

func (e *Engine) stopWS() {
	e.stateMu.Lock()
	s := e.ws
	e.ws = nil
	e.stateMu.Unlock()
	if s != nil {
		s.Stop()
		logx.Info("[engine] WS server stopped.")
	}
}

// Stop stops the engine; keepWS preserves DG-LAB device connections.
func (e *Engine) Stop(keepWS bool) {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.stop(keepWS)
}

func (e *Engine) stop(keepWS bool) {
	if !e.Running() {
		if !keepWS {
			e.stopWS()
		}
		return
	}
	a := e.a
	a.waveTestMu.Lock()
	wasTesting := a.waveTest.Active
	a.waveTest.Active = false
	a.waveTestMu.Unlock()
	if wasTesting {
		a.setOSCQueues(true)
		a.Hub.SetSuppressClear(false)
		a.refreshLimits()
		logx.Info("[engine] Wave test stopped due to engine shutdown.")
	}
	logx.Info("[engine] Stopping...")
	e.stateMu.Lock()
	cancel, srv, hs := e.cancel, e.oscSrv, e.handlers
	e.stateMu.Unlock()
	if !keepWS {
		e.stopWS()
	}
	if srv != nil {
		srv.Close()
	}
	if cancel != nil {
		cancel()
	}
	e.wg.Wait()
	for _, h := range hs {
		h.Stop()
	}
	e.stateMu.Lock()
	e.running, e.oscSrv, e.handlers, e.cancel = false, nil, nil, nil
	e.stateMu.Unlock()
	if keepWS {
		logx.Info("[engine] Stopped. (WS preserved)")
	} else {
		logx.Info("[engine] Stopped.")
	}
}

// Restart restarts the engine.
func (e *Engine) Restart(keepWS bool) {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	if keepWS {
		logx.Info("[engine] Restarting... (preserving WS connections)")
	} else {
		logx.Info("[engine] Restarting...")
	}
	e.stop(keepWS)
	e.start()
}

// RestartWS restarts only the DG-LAB WS server.
func (e *Engine) RestartWS() {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	logx.Info("[engine] Restarting WS server (port/host changed)...")
	e.stopWS()
	e.startWS()
}

// RestartLater schedules a restart after delay (like asyncio.create_task + sleep).
func (e *Engine) RestartLater(delay time.Duration, keepWS bool) {
	go func() {
		time.Sleep(delay)
		e.Restart(keepWS)
	}()
}

// ---------------------------------------------------------------- loops

func (a *App) oscGlobalHook(addr string, args []any) {
	a.overlimitOSCHook(addr, args)
	if sps.IsOGBAddress(addr) {
		a.trackOGBZone(addr)
	}
	a.rec.record(addr, args)
}

func (a *App) commandQueueProcessor(ctx context.Context) {
	for {
		cmd, err := a.Queue.Get(ctx)
		if err != nil {
			return
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					logx.Error("[queue] error: %v", r)
				}
			}()
			switch cmd.Action {
			case "osc_trigger":
				if cmd.Fn != nil {
					cmd.Fn()
				}
			case "wave":
				a.Hub.BroadcastWave(cmd.Channel, cmd.Value)
			case "clear":
				if !a.waveTestActive() {
					a.Hub.BroadcastClear(cmd.Channel)
				}
			}
		}()
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (a *App) strengthBoostChecker(ctx context.Context) {
	for sleepCtx(ctx, 100*time.Millisecond) {
		now := time.Now()
		for _, ch := range []string{"A", "B"} {
			a.boostMu.Lock()
			v := a.boost[ch]
			expired := v > 0 && !now.Before(a.boostExpire[ch])
			if expired {
				a.boost[ch] = 0
			}
			a.boostMu.Unlock()
			if expired {
				// Re-apply the configured cap: subtracting the requested
				// amount would undershoot when the boost was capped.
				a.refreshLimits()
				logx.Debug("[strength] channel=%s boost +%d expired, limits re-applied", ch, v)
			}
		}
	}
}

func (a *App) statusPusher(ctx context.Context) {
	last := -1
	for sleepCtx(ctx, 2*time.Second) {
		if !a.Live.HasSubscribers("status") {
			continue
		}
		n := a.Hub.Count()
		if n == last {
			continue
		}
		last = n
		devs := []map[string]any{}
		for _, d := range a.Hub.Devices() {
			s := d.Status()
			devs = append(devs, map[string]any{
				"strength": s.Strength, "strength_max": s.StrengthMax, "strength_limit": s.StrengthLimit,
			})
		}
		a.Live.Broadcast("status", map[string]any{"devices": devs, "engine_running": true})
	}
}

// ogbHeartbeat periodically tells the avatar an OGB-compatible tool listens.
// It never sends false on exit: other OGB tools may still be running.
func (a *App) ogbHeartbeat(ctx context.Context) {
	logx.Info("[sps] OGB_ENABLED heartbeat started")
	defer logx.Info("[sps] OGB_ENABLED heartbeat stopped")
	var client *osc.Client
	lastTarget := ""
	// Only report state changes: VRChat being closed is normal and must not
	// flood the log with a warning every interval.
	// UDP reports "connection refused" asynchronously on the *next* send, so
	// with VRChat closed sends alternate ok/fail. Recovery therefore needs a
	// streak of successes before we consider the target alive again.
	failing := false
	okStreak := 0
	fail := func(err error) {
		okStreak = 0
		if !failing {
			logx.Warn("[sps] OGB_ENABLED send failed (VRChat not running?), retrying silently: %v", err)
			failing = true
		} else {
			logx.Debug("[sps] OGB_ENABLED send failed: %v", err)
		}
	}
	defer func() {
		if client != nil {
			client.Close()
		}
	}()
	for {
		cfg := config.AsMap(a.Cfg.Get("sps"))
		interval := config.Clamp(config.Float(cfg["ogb_enabled_interval"], 5.0), 1, 60)
		if config.Bool(cfg["enabled"], true) && config.Bool(cfg["ogb_enabled_heartbeat"], true) {
			host := config.Str(cfg["ogb_target_host"], "127.0.0.1")
			port := config.Int(cfg["ogb_target_port"], 9000)
			if port >= 1 && port <= 65535 {
				target := fmt.Sprintf("%s:%d", host, port)
				if client == nil || target != lastTarget {
					if client != nil {
						client.Close()
					}
					c, err := osc.NewClient(host, port)
					if err != nil {
						fail(err)
						client = nil
					} else {
						client, lastTarget = c, target
						logx.Info("[sps] OGB_ENABLED -> %s every %.0fs", target, interval)
					}
				}
				if client != nil {
					// Keep the socket on failure: a UDP error only means nobody
					// is listening yet, recreating it would just add log noise.
					if err := client.Send("/avatar/parameters/OGB_ENABLED", true); err != nil {
						fail(err)
					} else if failing {
						if okStreak++; okStreak >= 3 {
							failing = false
							logx.Info("[sps] OGB_ENABLED sending again")
						}
					}
				}
			} else {
				logx.Warn("[sps] invalid ogb_target_port=%d, heartbeat skipped", port)
			}
		}
		if !sleepCtx(ctx, time.Duration(interval*float64(time.Second))) {
			return
		}
	}
}

// ---------------------------------------------------------------- wave test

type waveTestState struct {
	Active    bool    `json:"active"`
	Channel   string  `json:"channel"`
	Strength  int     `json:"strength"`
	WaveScale float64 `json:"wave_scale"`
	Preset    any     `json:"preset"`
}

func (a *App) waveTestActive() bool {
	a.waveTestMu.Lock()
	defer a.waveTestMu.Unlock()
	return a.waveTest.Active
}

func (a *App) setOSCQueues(on bool) {
	a.Queue.SetEnabled(queue.OSCInteraction, on)
	a.Queue.SetEnabled(queue.OSCPanel, on)
	a.Queue.SetEnabled(queue.Game, on)
}

func testChannels(ch string) []string {
	if ch == "AB" {
		return []string{"A", "B"}
	}
	return []string{ch}
}

// waveTestLoop streams the test wave while active. The test strength is
// pushed to devices as a temporary limit and never written to settings.
func (a *App) waveTestLoop(ctx context.Context) {
	pos := 0.0
	overriding := false
	for {
		a.waveTestMu.Lock()
		st := a.waveTest
		a.waveTestMu.Unlock()
		if st.Active {
			if !overriding {
				overriding = true
				a.Hub.SetSuppressClear(true)
				a.setOSCQueues(false)
			}
			chs := testChannels(st.Channel)
			l := a.limitsFromSettings()
			for _, ch := range chs {
				if ch == "A" {
					l.LimitA = st.Strength
				} else {
					l.LimitB = st.Strength
				}
			}
			a.Hub.RefreshLimits(l)
			for _, ch := range chs {
				a.Hub.BroadcastSetStrength(ch, "2", st.Strength, true)
			}
			preset := config.Str(st.Preset, "")
			w := ""
			if preset != "" && a.Lib.Get(preset) != nil {
				w = a.Lib.BuildResampledWindow(preset, pos, 10, st.WaveScale, 0, 1)
				pos += 40
			}
			if w == "" {
				w = wave.ScaleWavestr(config.DefaultShockWave, st.WaveScale)
			}
			for _, ch := range chs {
				a.Queue.PutWave(queue.API, ch, w, "wave_test_loop")
			}
			if !sleepCtx(ctx, 900*time.Millisecond) {
				return
			}
			continue
		}
		if overriding {
			overriding = false
			a.refreshLimits()
			a.Hub.SetSuppressClear(false)
			a.setOSCQueues(true)
		}
		pos = 0
		if !sleepCtx(ctx, 100*time.Millisecond) {
			return
		}
	}
}

// ---------------------------------------------------------------- hot reload

// HotReloadLoop polls config files every second.
func (a *App) HotReloadLoop(ctx context.Context) {
	for sleepCtx(ctx, time.Second) {
		if a.Cfg.Changed() {
			a.hotReload()
		}
	}
}

func (a *App) hotReload() {
	adv, basic, err := a.Cfg.LoadFiles()
	if err != nil {
		logx.Error("[hot-reload] Failed to reload configuration: %v", err)
		a.Cfg.UpdateMtimes()
		return
	}
	a.applySettings(adv, basic)
	a.Cfg.UpdateMtimes()
}

// applySettings swaps in new trees and restarts only what is needed.
func (a *App) applySettings(adv, basic config.M) {
	oldAdv, oldBasic := a.Cfg.Snapshot()
	engineRestart, wsRestart, paramsChanged := false, false, false
	if !config.Equal(adv["osc"], oldAdv["osc"]) {
		engineRestart = true
	}
	if !config.Equal(adv["ws"], oldAdv["ws"]) {
		wsRestart = true
	}
	for _, ch := range config.Channels {
		if !config.Equal(config.Get(basic, "dglab3", ch, "avatar_params"), config.Get(oldBasic, "dglab3", ch, "avatar_params")) ||
			!config.Equal(config.Get(basic, "dglab3", ch, "mode"), config.Get(oldBasic, "dglab3", ch, "mode")) {
			paramsChanged = true
		}
	}
	if !config.Equal(adv["web_server"], oldAdv["web_server"]) {
		logx.Warn("[hot-reload] web_server config changed. Full program restart required to apply.")
		adv["web_server"] = oldAdv["web_server"]
	}
	a.Cfg.Replace(adv, basic)
	a.refreshServerIP()
	a.applyLogLevel()
	a.refreshAllowedHosts()
	a.refreshLimits()
	switch {
	case wsRestart && !engineRestart:
		logx.Info("[hot-reload] WS config changed, restarting WS server only...")
		go func() {
			a.Engine.RestartWS()
			if paramsChanged {
				a.Engine.Reconfigure()
			}
		}()
	case wsRestart && engineRestart:
		logx.Info("[hot-reload] OSC + WS config changed, full engine restart...")
		go a.Engine.Restart(false)
	case engineRestart:
		logx.Info("[hot-reload] OSC config changed, restarting engine (preserving WS connections)...")
		go a.Engine.Restart(true)
	case paramsChanged:
		go a.Engine.Reconfigure()
	}
	logx.Success("[hot-reload] Configuration reloaded.")
}
