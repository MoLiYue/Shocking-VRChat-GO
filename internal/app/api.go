package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"shockingvrc/assets"
	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/queue"
	"shockingvrc/internal/wave"
)

// HTTPError is returned by API handlers to produce FastAPI-style
// {"detail": "..."} responses.
type HTTPError struct {
	Code   int
	Detail string
}

func (e *HTTPError) Error() string { return e.Detail }

// HTTPErrorBody is an error response carrying extra JSON fields
// (e.g. per-row validation errors) next to "detail".
type HTTPErrorBody struct {
	Code   int
	Detail string
	Extra  map[string]any
}

func (e *HTTPErrorBody) Error() string { return e.Detail }

func httpErr(code int, format string, args ...any) error {
	return &HTTPError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

type apiFunc func(r *http.Request) (any, error)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func wrap(f apiFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logx.Error("[api] %s %s panic: %v", r.Method, r.URL.Path, rec)
				writeJSON(w, 500, map[string]any{"error": fmt.Sprint(rec)})
			}
		}()
		v, err := f(r)
		if err != nil {
			var hb *HTTPErrorBody
			if errors.As(err, &hb) {
				body := map[string]any{"detail": hb.Detail}
				for k, v := range hb.Extra {
					body[k] = v
				}
				writeJSON(w, hb.Code, body)
				return
			}
			var he *HTTPError
			if errors.As(err, &he) {
				writeJSON(w, he.Code, map[string]any{"detail": he.Detail})
				return
			}
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		if raw, ok := v.(rawResponse); ok {
			for k, val := range raw.headers {
				w.Header().Set(k, val)
			}
			w.Header().Set("Content-Type", raw.contentType)
			w.WriteHeader(200)
			_, _ = w.Write(raw.body)
			return
		}
		writeJSON(w, 200, v)
	}
}

type rawResponse struct {
	contentType string
	headers     map[string]string
	body        []byte
}

func readBody(r *http.Request) (config.M, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var m config.M
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %v", err)
	}
	if m == nil {
		m = config.M{}
	}
	return m, nil
}

func pathInt(r *http.Request, name string) (int, error) {
	v, err := strconv.Atoi(r.PathValue(name))
	if err != nil {
		return 0, httpErr(422, "%s: value is not a valid integer", name)
	}
	return v, nil
}

func pathFloat(r *http.Request, name string) (float64, error) {
	v, err := strconv.ParseFloat(r.PathValue(name), 64)
	if err != nil {
		return 0, httpErr(422, "%s: value is not a valid number", name)
	}
	return v, nil
}

func channelsOf(ch string) ([]string, error) {
	switch strings.ToUpper(ch) {
	case "ALL":
		return []string{"A", "B"}, nil
	case "A", "B":
		return []string{strings.ToUpper(ch)}, nil
	}
	return nil, httpErr(400, "invalid channel")
}

func lowerChannel(ch string) (string, error) {
	c := strings.ToLower(ch)
	if c != "a" && c != "b" {
		return "", httpErr(400, "invalid channel")
	}
	return c, nil
}

// Handler builds the HTTP router.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, f apiFunc) { mux.HandleFunc(pattern, wrap(f)) }

	// pages
	mux.HandleFunc("GET /{$}", a.serveSPA)
	h("GET /get_ip", func(*http.Request) (any, error) { return a.CurrentIP(), nil })
	mux.HandleFunc("GET /dashboard-legacy", func(w http.ResponseWriter, r *http.Request) { a.serveTemplate(w, "dashboard.html", nil) })
	mux.HandleFunc("GET /qr", func(w http.ResponseWriter, r *http.Request) {
		a.serveTemplate(w, "tiny-qr.html", map[string]string{"content": a.QRContent()})
	})

	// websockets
	mux.Handle("GET /ws/live", a.Live)
	mux.Handle("GET /ws/dglab-v4", a.V4)

	// status & device control
	h("GET /api/v1/status", a.apiStatus)
	h("GET /api/v1/wave_history", func(*http.Request) (any, error) {
		return map[string]any{"A": a.waves.snapshot("A"), "B": a.waves.snapshot("B")}, nil
	})
	h("GET /api/v1/wave_history/stream", a.apiWaveHistoryStream)
	h("GET /api/v1/osc_activity", func(*http.Request) (any, error) {
		return map[string]any{"events": a.activity.list()}, nil
	})
	h("GET /api/v1/qr_payload", func(*http.Request) (any, error) { return map[string]any{"content": a.QRContent()}, nil })
	h("GET /api/v1/qr_payload_v4", func(*http.Request) (any, error) {
		return map[string]any{
			"content":       a.QRContentV4(),
			"enabled":       config.Bool(a.Cfg.Get("ws", "v4_enabled"), true),
			"controller_id": a.V4.ControllerID,
		}, nil
	})
	h("GET /api/v1/strength/{channel}/{action}/{value}", a.apiStrength)
	h("GET /api/v1/strength_boost/{channel}/{value}/{duration}", a.apiStrengthBoost)
	h("GET /api/v1/strength_limit", func(*http.Request) (any, error) { return a.strengthLimitView(), nil })
	h("POST /api/v1/strength_limit", a.apiStrengthLimitSet)
	h("GET /api/v1/overlimit_rules", a.apiOverlimitGet)
	h("GET /api/v1/params/used", a.apiParamsUsed)
	h("POST /api/v1/overlimit_rules", a.apiOverlimitSet)
	h("DELETE /api/v1/overlimit_rules/{index}", a.apiOverlimitDelete)
	h("GET /api/v1/shock/{channel}/{second}", a.apiShock)
	h("GET /api/v1/sendwave/{channel}/{repeat}/{wavedata}", a.apiSendwave)
	h("POST /api/v1/wave_test/start", a.apiWaveTestStart)
	h("POST /api/v1/wave_test/stop", a.apiWaveTestStop)
	h("POST /api/v1/wave_test/update", a.apiWaveTestUpdate)
	h("GET /api/v1/wave_test/status", func(*http.Request) (any, error) {
		a.waveTestMu.Lock()
		defer a.waveTestMu.Unlock()
		return a.waveTest, nil
	})

	// settings
	h("GET /api/v1/config", func(*http.Request) (any, error) {
		adv, basic := a.Cfg.Snapshot()
		return map[string]any{"basic": basic, "advanced": config.StripBasic(adv)}, nil
	})
	h("POST /api/v1/settings", a.apiSettings)
	h("GET /api/v1/config/export", a.apiConfigExport)
	h("POST /api/v1/config/import", a.apiConfigImport)
	h("POST /api/v1/engine/restart", func(*http.Request) (any, error) {
		a.Engine.RestartLater(300*time.Millisecond, true)
		return map[string]any{"success": true, "message": "引擎正在重启（保持设备连接）..."}, nil
	})
	h("GET /api/v1/engine/status", func(*http.Request) (any, error) {
		return map[string]any{"running": a.Engine.Running()}, nil
	})
	h("POST /api/v1/shutdown", func(*http.Request) (any, error) {
		logx.Info("[shutdown] Shutdown requested via API.")
		go func() { time.Sleep(500 * time.Millisecond); a.Shutdown() }()
		return map[string]any{"success": true, "message": "Shutting down..."}, nil
	})
	h("POST /api/v1/ui/show", func(*http.Request) (any, error) {
		if a.OnShowWindow != nil {
			go a.OnShowWindow()
		}
		return map[string]any{"success": true}, nil
	})
	h("POST /api/v1/ui/theme", func(r *http.Request) (any, error) {
		data, err := readBody(r)
		if err != nil {
			return nil, err
		}
		theme := "dark"
		if config.Str(data["theme"], "") == "light" {
			theme = "light"
		}
		if (theme == "dark") != a.UIThemeDark() {
			a.Cfg.Write(func(adv, _ config.M) { config.Sub(adv, true, "general")["ui_theme"] = theme })
			if err := a.Cfg.Save(); err != nil {
				return nil, err
			}
		}
		if a.OnTheme != nil {
			a.OnTheme(theme == "dark")
		}
		return map[string]any{"success": true, "theme": theme}, nil
	})
	h("POST /api/v1/ui/locale", func(r *http.Request) (any, error) {
		data, err := readBody(r)
		if err != nil {
			return nil, err
		}
		loc := "zh"
		if config.Str(data["locale"], "") == "en" {
			loc = "en"
		}
		if loc != a.UILocale() {
			a.Cfg.Write(func(adv, _ config.M) { config.Sub(adv, true, "general")["ui_locale"] = loc })
			if err := a.Cfg.Save(); err != nil {
				return nil, err
			}
		}
		return map[string]any{"success": true, "locale": loc}, nil
	})
	h("GET /api/v1/logs", func(*http.Request) (any, error) {
		return map[string]any{"logs": logx.Buffer(200)}, nil
	})
	h("GET /api/v1/sps", a.apiSPSGet)
	h("POST /api/v1/sps", a.apiSPSSet)
	h("GET /api/v1/sps/zones", a.apiSPSZones)
	h("GET /api/v1/chatbox", a.apiChatboxGet)
	h("POST /api/v1/chatbox", a.apiChatboxSet)
	h("POST /api/v1/chatbox/test", a.apiChatboxTest)
	h("GET /api/v1/update/check", a.apiUpdateCheck)
	h("GET /api/v1/update/progress", func(*http.Request) (any, error) { return a.upd.progressSnapshot(), nil })
	h("POST /api/v1/update/apply", a.apiUpdateApply)
	h("GET /api/v1/params/{channel}", a.apiParamsGet)
	h("POST /api/v1/params/{channel}", a.apiParamsSet)
	h("POST /api/v1/params", a.apiParamsSetAll)
	h("GET /api/v1/mode_config/{channel}/{mode}", a.apiModeConfigGet)
	h("POST /api/v1/mode_config/{channel}/{mode}", a.apiModeConfigSet)
	h("GET /api/v1/combo/{channel}", a.apiComboGet)
	h("POST /api/v1/combo/{channel}", a.apiComboSet)
	h("GET /api/v1/curve", a.apiCurveList)
	h("GET /api/v1/curve/{key...}", a.apiCurveGet)
	h("POST /api/v1/curve/{key...}", a.apiCurveSet)
	h("DELETE /api/v1/curve/{key...}", a.apiCurveDelete)
	h("GET /api/v1/profiles", a.apiProfilesList)
	h("PUT /api/v1/profiles/{name}", a.apiProfileSave)
	h("POST /api/v1/profiles/{name}", a.apiProfileLoad)
	h("DELETE /api/v1/profiles/{name}", a.apiProfileDelete)
	h("POST /api/v1/recorder/start", a.apiRecorderStart)
	h("POST /api/v1/recorder/stop", a.apiRecorderStop)
	h("GET /api/v1/recorder/status", a.apiRecorderStatus)
	h("GET /api/v1/recordings", a.apiRecordingsList)
	h("DELETE /api/v1/recordings/{filename}", a.apiRecordingDelete)
	h("POST /api/v1/playback/start", a.apiPlaybackStart)
	h("POST /api/v1/playback/stop", func(*http.Request) (any, error) {
		a.play.requestStop()
		return map[string]any{"success": true}, nil
	})
	h("GET /api/v1/playback/status", func(*http.Request) (any, error) { return a.play.status(), nil })
	h("POST /api/v1/wave_simulate", a.apiWaveSimulate)
	h("GET /api/v1/wave_presets", func(*http.Request) (any, error) {
		return map[string]any{"presets": a.Lib.Names()}, nil
	})
	h("GET /api/v1/wave_presets/{name}/samples", a.apiPresetSamples)
	h("GET /api/v1/wave_presets/{name}/preview", a.apiPresetPreview)
	h("POST /api/v1/wave_presets/import", a.apiPresetImport)
	h("GET /api/v1/wave_preset/{channel}/{name}/{duration}", a.apiWavePresetSend)

	// SPA assets + fallback
	static := assets.Static()
	assetSrv := http.FileServer(http.FS(static))
	mux.Handle("GET /assets/", assetSrv)
	mux.HandleFunc("GET /{path...}", func(w http.ResponseWriter, r *http.Request) {
		p := r.PathValue("path")
		for _, pre := range []string{"api/", "ws/", "assets/", "static/"} {
			if strings.HasPrefix(p, pre) {
				writeJSON(w, 404, map[string]any{"detail": "Not Found"})
				return
			}
		}
		if strings.Contains(path.Base(p), ".") {
			if f, err := fs.Stat(static, p); err == nil && !f.IsDir() {
				assetSrv.ServeHTTP(w, r)
				return
			}
			writeJSON(w, 404, map[string]any{"detail": "Not Found"})
			return
		}
		a.serveSPA(w, r)
	})
	guarded := a.Guard.Wrap(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The DG-LAB APP endpoint is authenticated by the random tid and
		// must not go through the browser guard: the APP may send an Origin
		// header or connect through a hostname (SERVER_IP), which the guard
		// would reject with 403.
		if r.URL.Path == "/ws/dglab-v4" {
			mux.ServeHTTP(w, r)
			return
		}
		guarded.ServeHTTP(w, r)
	})
}

func (a *App) serveSPA(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(assets.FS, "static/index.html")
	if err != nil {
		http.Redirect(w, r, "/dashboard-legacy", http.StatusTemporaryRedirect)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	_, _ = w.Write(b)
}

func (a *App) serveTemplate(w http.ResponseWriter, name string, vars map[string]string) {
	b, err := assets.Template(name)
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(404)
		_, _ = io.WriteString(w, "<h1>Template not found</h1>")
		return
	}
	s := string(b)
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{{ "+k+" }}", v)
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, s)
}

// ---------------------------------------------------------------- status

func (a *App) apiStatus(*http.Request) (any, error) {
	devs := []any{}
	for _, d := range a.Hub.Devices() {
		devs = append(devs, map[string]any{"type": "shock", "device": "coyotev3", "attr": d.Status()})
	}
	return map[string]any{
		"healthy":        "ok",
		"engine_running": a.Engine.Running(),
		"osc_listening": fmt.Sprintf("%s:%d", config.Str(a.Cfg.Get("osc", "listen_host"), ""),
			config.Int(a.Cfg.Get("osc", "listen_port"), 0)),
		"last_osc_time":  a.activity.lastTime(),
		"devices":        devs,
		"version":        Version,
		"tray_available": a.TrayAvailable,
	}, nil
}

func (a *App) apiWaveHistoryStream(r *http.Request) (any, error) {
	ch := strings.ToUpper(r.URL.Query().Get("channel"))
	if ch == "" {
		ch = "A"
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	samples, seq := a.waves.since(ch, since)
	return map[string]any{"samples": samples, "seq": seq, "sample_ms": waveHistorySampleMs}, nil
}

func (a *App) apiStrength(r *http.Request) (any, error) {
	chs, err := channelsOf(r.PathValue("channel"))
	if err != nil {
		return nil, err
	}
	value, err := pathInt(r, "value")
	if err != nil {
		return nil, err
	}
	action := r.PathValue("action")
	mode, ok := map[string]string{"set": "2", "add": "1", "sub": "0"}[action]
	if !ok {
		return nil, httpErr(400, "action must be set/add/sub")
	}
	value = config.ClampInt(value, 0, 200)
	// Never force: "set" is clamped to the effective cap and "add" is
	// limited to the remaining headroom, exactly like OSC-driven output.
	for _, ch := range chs {
		if mode == "2" {
			a.Hub.BroadcastSetStrength(ch, "2", value, false)
		} else {
			a.Hub.BroadcastStrengthAdjust(ch, mode, value)
		}
	}
	return map[string]any{"result": "OK", "action": action, "value": value, "channels": chs}, nil
}

func (a *App) apiStrengthBoost(r *http.Request) (any, error) {
	chs, err := channelsOf(r.PathValue("channel"))
	if err != nil {
		return nil, err
	}
	value, err := pathInt(r, "value")
	if err != nil {
		return nil, err
	}
	dur, err := pathFloat(r, "duration")
	if err != nil {
		return nil, err
	}
	value = config.ClampInt(value, 1, 100)
	dur = config.Clamp(dur, 0.5, 30)
	for _, ch := range chs {
		a.boostMu.Lock()
		already := a.boost[ch]
		a.boost[ch] = value
		a.boostExpire[ch] = time.Now().Add(time.Duration(dur * float64(time.Second)))
		a.boostMu.Unlock()
		if already != value {
			if already != 0 {
				a.refreshLimits()
			}
			// Capped at the effective limit (incl. overlimit rules).
			a.Hub.BroadcastStrengthAdjust(ch, "1", value)
		}
	}
	return map[string]any{"result": "OK", "boost": value, "duration": dur, "channels": chs}, nil
}

func (a *App) strengthLimitView() map[string]any {
	out := map[string]any{}
	a.Cfg.Read(func(_, basic config.M) {
		out["channel_a"] = config.Get(basic, "dglab3", "channel_a", "strength_limit")
		out["channel_b"] = config.Get(basic, "dglab3", "channel_b", "strength_limit")
	})
	return out
}

func (a *App) apiStrengthLimitSet(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	changed := false
	var bad error
	a.Cfg.Write(func(adv, basic config.M) {
		for _, ch := range []string{"a", "b"} {
			key := "channel_" + ch
			set := func(field string, raw any) {
				v, ok := config.ToInt(raw)
				if !ok {
					bad = fmt.Errorf("invalid literal for int(): %v", raw)
					return
				}
				v = config.ClampInt(v, 0, 200)
				config.Sub(basic, true, "dglab3", key)[field] = v
				config.Sub(adv, true, "dglab3", key)[field] = v
				changed = true
			}
			if raw, ok := data[key]; ok {
				set("strength_limit", raw)
			}
		}
	})
	if bad != nil {
		return nil, bad
	}
	if changed {
		if err := a.Cfg.Save(); err != nil {
			return nil, err
		}
		a.refreshLimits()
		v := a.strengthLimitView()
		logx.Info("[strength] Updated: A=%v, B=%v", v["channel_a"], v["channel_b"])
	}
	v := a.strengthLimitView()
	v["success"] = true
	return v, nil
}

func (a *App) apiShock(r *http.Request) (any, error) {
	chs := []string{strings.ToUpper(r.PathValue("channel"))}
	if r.PathValue("channel") == "all" {
		chs = []string{"A", "B"}
	}
	sec, err := strconv.ParseFloat(r.PathValue("second"), 64)
	if err != nil {
		sec = 1.0
	}
	sec = minF(sec, 10.0)
	repeat := int(10 * sec)
	for i := 0; i < repeat/10; i++ {
		for _, ch := range chs {
			a.Queue.PutWave(queue.API, ch, wave.RepeatOp("0A0A0A0A64646464", 10), "api_shock")
		}
	}
	if rem := repeat % 10; rem > 0 {
		for _, ch := range chs {
			a.Queue.PutWave(queue.API, ch, wave.RepeatOp("0A0A0A0A64646464", rem), "api_shock")
		}
	}
	return map[string]any{"result": "OK"}, nil
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

var hex16 = regexp.MustCompile(`^[0-9A-F]{16}$`)

func (a *App) apiSendwave(r *http.Request) (any, error) {
	ch := strings.ToUpper(r.PathValue("channel"))
	if ch != "A" && ch != "B" {
		ch = "A"
	}
	rep, err := strconv.Atoi(r.PathValue("repeat"))
	if err != nil || rep < 1 || rep > 100 {
		rep = 10
	}
	wd := r.PathValue("wavedata")
	if !hex16.MatchString(wd) {
		wd = "0A0A0A0A64646464"
	}
	logx.Debug("[API][sendwave] C:%s R:%d W:%s", ch, rep, wd)
	a.Queue.PutWave(queue.API, ch, wave.RepeatOp(wd, rep), "api_sendwave")
	return map[string]any{"result": "OK"}, nil
}

// ---------------------------------------------------------------- wave test

func (a *App) apiWaveTestStart(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	ch := strings.ToUpper(config.Str(data["channel"], "A"))
	if ch != "A" && ch != "B" && ch != "AB" {
		return nil, httpErr(400, "channel must be A, B, or AB")
	}
	strength := config.ClampInt(config.Int(data["strength"], 50), 0, 200)
	scale := config.Clamp(config.Float(data["wave_scale"], 1.0), 0, 1)
	var preset any
	if p := config.Str(data["preset"], ""); p != "" {
		preset = p
	}
	a.waveTestMu.Lock()
	a.waveTest = waveTestState{Active: true, Channel: ch, Strength: strength, WaveScale: scale, Preset: preset}
	a.waveTestMu.Unlock()
	pn := config.Str(preset, "default")
	logx.Info("[wave-test] Started: ch=%s str=%d scale=%v preset=%s", ch, strength, scale, pn)
	return map[string]any{"result": "OK", "active": true}, nil
}

func (a *App) apiWaveTestStop(*http.Request) (any, error) {
	a.waveTestMu.Lock()
	a.waveTest.Active = false
	ch := a.waveTest.Channel
	a.waveTestMu.Unlock()
	for _, c := range testChannels(ch) {
		for _, d := range a.Hub.Devices() {
			_ = d.ClearWave(c)
		}
	}
	logx.Info("[wave-test] Stopped")
	return map[string]any{"result": "OK", "active": false}, nil
}

func (a *App) apiWaveTestUpdate(r *http.Request) (any, error) {
	a.waveTestMu.Lock()
	active := a.waveTest.Active
	a.waveTestMu.Unlock()
	if !active {
		return nil, httpErr(400, "test not running")
	}
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	a.waveTestMu.Lock()
	defer a.waveTestMu.Unlock()
	if v, ok := data["strength"]; ok {
		a.waveTest.Strength = config.ClampInt(config.Int(v, a.waveTest.Strength), 0, 200)
	}
	if v, ok := data["wave_scale"]; ok {
		a.waveTest.WaveScale = config.Clamp(config.Float(v, a.waveTest.WaveScale), 0, 1)
	}
	if v, ok := data["preset"]; ok {
		if p := config.Str(v, ""); p != "" {
			a.waveTest.Preset = p
		} else {
			a.waveTest.Preset = nil
		}
	}
	return map[string]any{"result": "OK", "state": map[string]any{
		"active": true, "strength": a.waveTest.Strength, "wave_scale": a.waveTest.WaveScale, "preset": a.waveTest.Preset,
	}}, nil
}
