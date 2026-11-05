package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"shockingvrc/internal/config"
	"shockingvrc/internal/handler"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/sps"
)

func (a *App) apiSettings(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	restart := map[string]bool{}
	var bad error
	toInt := func(v any) int {
		i, ok := config.ToInt(v)
		if !ok {
			bad = fmt.Errorf("invalid literal for int(): %v", v)
		}
		return i
	}
	a.Cfg.Write(func(adv, _ config.M) {
		if o := config.AsMap(data["osc"]); o != nil {
			s := config.Sub(adv, true, "osc")
			if v, ok := o["listen_port"]; ok {
				s["listen_port"] = toInt(v)
				restart["osc"] = true
			}
			if v, ok := o["listen_host"]; ok {
				s["listen_host"] = v
				restart["osc"] = true
			}
		}
		if o := config.AsMap(data["ws"]); o != nil {
			s := config.Sub(adv, true, "ws")
			if v, ok := o["listen_port"]; ok {
				s["listen_port"] = toInt(v)
				restart["ws"] = true
			}
			if v, ok := o["v4_enabled"]; ok {
				s["v4_enabled"] = config.Bool(v, true)
			}
		}
		if o := config.AsMap(data["web_server"]); o != nil {
			s := config.Sub(adv, true, "web_server")
			if v, ok := o["listen_port"]; ok {
				s["listen_port"] = toInt(v)
				restart["web_server"] = true
			}
			if v, ok := o["listen_host"]; ok {
				s["listen_host"] = v
				restart["web_server"] = true
			}
		}
		if v, ok := data["log_level"]; ok {
			adv["log_level"] = v
		}
		if v, ok := data["minimize_to_tray"]; ok {
			config.Sub(adv, true, "general")["minimize_to_tray"] = config.Bool(v, false)
		}
		if v, ok := data["github_mirror"]; ok {
			config.Sub(adv, true, "general")["github_mirror"] = strings.TrimSpace(config.Str(v, ""))
		}
	})
	if bad != nil {
		return nil, bad
	}
	if _, ok := data["log_level"]; ok {
		a.applyLogLevel()
	}
	if _, ok := data["github_mirror"]; ok {
		a.upd.clearCache()
	}
	if err := a.Cfg.Save(); err != nil {
		return nil, err
	}
	list := []string{}
	for k := range restart {
		list = append(list, k)
	}
	sort.Strings(list)
	if len(restart) > 0 {
		go func() {
			time.Sleep(500 * time.Millisecond)
			if restart["osc"] || restart["ws"] {
				if restart["ws"] {
					logx.Info("[config] Restarting engine (WS config changed)...")
					a.Engine.Restart(false)
				} else {
					logx.Info("[config] Restarting engine (preserving WS connections)...")
					a.Engine.Restart(true)
				}
			}
			if restart["web_server"] {
				logx.Warn("[config] Web server port/host changed. Full restart required.")
				a.RestartProcess()
			}
		}()
	}
	msg := "已保存。"
	if len(restart) > 0 {
		msg += "引擎正在重启..."
	}
	return map[string]any{"success": true, "restart_needed": list, "message": msg}, nil
}

func (a *App) apiConfigExport(*http.Request) (any, error) {
	adv, basic := a.Cfg.Snapshot()
	data := map[string]any{
		"version":     config.FileVersion,
		"exported_at": time.Now().Format("2006-01-02T15:04:05.000000"),
		"basic":       basic,
		"advanced":    config.StripBasic(adv),
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, err
	}
	return rawResponse{
		contentType: "application/json",
		headers:     map[string]string{"Content-Disposition": `attachment; filename="shocking-vrchat-config.json"`},
		body:        b,
	}, nil
}

func readUpload(r *http.Request) (string, []byte, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return "", nil, httpErr(422, "file: field required")
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return "", nil, httpErr(422, "file: field required")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 32<<20))
	return hdr.Filename, b, err
}

func (a *App) apiConfigImport(r *http.Request) (any, error) {
	name, b, err := readUpload(r)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(name, ".json") {
		return nil, httpErr(400, "Please upload a .json file")
	}
	var data config.M
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, httpErr(400, "Invalid JSON: %v", err)
	}
	newBasic, ok1 := data["basic"].(map[string]any)
	newAdv, ok2 := data["advanced"].(map[string]any)
	if !ok1 || !ok2 {
		return nil, httpErr(400, `Invalid config file: missing "basic" or "advanced" section`)
	}
	if v := config.Str(data["version"], ""); v != config.FileVersion {
		return nil, httpErr(400, "Config version mismatch: file is %s, expected %s", v, config.FileVersion)
	}
	for _, ch := range config.Channels {
		if bc := config.Sub(newBasic, false, "dglab3", ch); bc != nil {
			ac := config.Sub(newAdv, true, "dglab3", ch)
			ac["avatar_params"] = config.DeepCopy(bc["avatar_params"])
			if ac["avatar_params"] == nil {
				ac["avatar_params"] = []any{}
			}
			ac["mode"] = config.Str(bc["mode"], "distance")
			if v, ok := bc["strength_limit"]; ok {
				ac["strength_limit"] = v
			} else {
				ac["strength_limit"] = 100
			}
		}
	}
	config.Sub(newAdv, true, "ws")["master_uuid"] = a.Cfg.Get("ws", "master_uuid")
	newAdv["version"] = config.FileVersion
	newBasic["version"] = config.FileVersion
	for _, ch := range config.Channels {
		if ac := config.Sub(newAdv, false, "dglab3", ch); ac != nil {
			config.NormalizeAvatarParams(ac)
		}
	}
	a.Cfg.Replace(newAdv, newBasic)
	a.refreshServerIP()
	a.applyLogLevel()
	a.refreshLimits()
	if err := a.Cfg.Save(); err != nil {
		return nil, err
	}
	a.Engine.RestartLater(300*time.Millisecond, true)
	return map[string]any{"success": true, "message": "配置已导入，引擎正在重启..."}, nil
}

// ---------------------------------------------------------------- params

func (a *App) apiParamsGet(r *http.Request) (any, error) {
	ch, err := lowerChannel(r.PathValue("channel"))
	if err != nil {
		return nil, err
	}
	key := "channel_" + ch
	var out map[string]any
	a.Cfg.Read(func(_, basic config.M) {
		c := config.Sub(basic, false, "dglab3", key)
		params := config.DeepCopy(c["avatar_params"])
		if params == nil {
			params = []any{}
		}
		sl := c["strength_limit"]
		if sl == nil {
			sl = 100
		}
		out = map[string]any{"params": params, "default_mode": config.Str(c["mode"], "distance"), "strength_limit": sl}
	})
	return out, nil
}

// paramError is one row-level validation problem returned to the UI.
type paramError struct {
	Channel string `json:"channel"`
	Index   int    `json:"index"`
	Code    string `json:"code"` // empty_path | bad_path | bad_mode | duplicate
	Path    string `json:"path,omitempty"`
}

var validModes = func() map[string]bool {
	m := map[string]bool{}
	for _, x := range handler.Modes {
		m[x] = true
	}
	return m
}()

// validateParams normalizes one channel's list and reports problems.
// Short paths ("Shock/A") get the /avatar/parameters/ prefix.
func validateParams(ch string, raw []any, defMode string) ([]any, []paramError) {
	out := []any{}
	var errs []paramError
	seen := map[string]int{}
	for i, p := range raw {
		var path, mode string
		enabled := true
		switch t := p.(type) {
		case string:
			path, mode = t, defMode
		case map[string]any:
			path = config.Str(t["path"], "")
			mode = config.Str(t["mode"], defMode)
			if v, ok := t["enabled"]; ok {
				enabled = config.Bool(v, true)
			}
		default:
			errs = append(errs, paramError{ch, i, "bad_path", ""})
			continue
		}
		path = strings.TrimSpace(path)
		if path == "" {
			errs = append(errs, paramError{ch, i, "empty_path", ""})
			continue
		}
		if !strings.HasPrefix(path, "/") {
			path = sps.AvatarParameterPrefix + path
		}
		if strings.ContainsAny(path, " \t#,?[]{}") || strings.HasSuffix(path, "/") {
			errs = append(errs, paramError{ch, i, "bad_path", path})
			continue
		}
		if !validModes[mode] {
			errs = append(errs, paramError{ch, i, "bad_mode", path})
			continue
		}
		if _, dup := seen[path]; dup {
			errs = append(errs, paramError{ch, i, "duplicate", path})
			continue
		}
		seen[path] = i
		out = append(out, config.M{"path": path, "mode": mode, "enabled": enabled})
	}
	return out, errs
}

type channelParams struct {
	key     string
	params  []any
	defMode any
	hasMode bool
	limit   any
}

// applyParams writes validated params for the given channels, saves once
// and applies them live (no engine restart).
func (a *App) applyParams(chs []channelParams) error {
	var bad error
	a.Cfg.Write(func(adv, basic config.M) {
		for _, c := range chs {
			bc := config.Sub(basic, true, "dglab3", c.key)
			bc["avatar_params"] = c.params
			if c.hasMode {
				bc["mode"] = c.defMode
			}
			if c.limit != nil {
				i, ok := config.ToInt(c.limit)
				if !ok {
					bad = fmt.Errorf("invalid literal for int(): %v", c.limit)
					return
				}
				bc["strength_limit"] = config.ClampInt(i, 0, 200)
			}
			ac := config.Sub(adv, true, "dglab3", c.key)
			ac["avatar_params"] = config.DeepCopy(c.params)
			ac["mode"] = bc["mode"]
			ac["strength_limit"] = bc["strength_limit"]
			config.NormalizeAvatarParams(ac)
		}
	})
	if bad != nil {
		return bad
	}
	if err := a.Cfg.Save(); err != nil {
		return err
	}
	a.refreshLimits()
	a.Engine.Reconfigure()
	return nil
}

func validationError(errs []paramError) error {
	return &HTTPErrorBody{Code: 400, Detail: "invalid parameters", Extra: map[string]any{"errors": errs}}
}

func (a *App) apiParamsSet(r *http.Request) (any, error) {
	ch, err := lowerChannel(r.PathValue("channel"))
	if err != nil {
		return nil, err
	}
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	defMode := config.Str(data["default_mode"], config.Str(a.Cfg.GetBasic("dglab3", "channel_"+ch, "mode"), "distance"))
	if !validModes[defMode] {
		return nil, httpErr(400, "invalid default_mode: %s", defMode)
	}
	params, errs := validateParams(ch, config.AsList(data["params"]), defMode)
	if len(errs) > 0 {
		return nil, validationError(errs)
	}
	_, hasMode := data["default_mode"]
	if err := a.applyParams([]channelParams{{key: "channel_" + ch, params: params, defMode: defMode, hasMode: hasMode, limit: data["strength_limit"]}}); err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "params": params, "applied": true}, nil
}

// apiParamsSetAll saves both channels in one request:
// {"a": {"params": [...], "default_mode": "distance"}, "b": {...}}
func (a *App) apiParamsSetAll(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	var chs []channelParams
	var errs []paramError
	out := map[string]any{"success": true, "applied": true}
	for _, ch := range []string{"a", "b"} {
		cd := config.AsMap(data[ch])
		if cd == nil {
			continue
		}
		defMode := config.Str(cd["default_mode"], config.Str(a.Cfg.GetBasic("dglab3", "channel_"+ch, "mode"), "distance"))
		if !validModes[defMode] {
			return nil, httpErr(400, "invalid default_mode for channel %s: %s", ch, defMode)
		}
		params, e := validateParams(ch, config.AsList(cd["params"]), defMode)
		errs = append(errs, e...)
		_, hasMode := cd["default_mode"]
		chs = append(chs, channelParams{key: "channel_" + ch, params: params, defMode: defMode, hasMode: hasMode})
		out[ch] = params
	}
	if len(errs) > 0 {
		return nil, validationError(errs)
	}
	if len(chs) == 0 {
		return nil, httpErr(400, "nothing to save")
	}
	if err := a.applyParams(chs); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------- mode config

var modeKeys = map[string][]string{
	"shock":    {"duration", "wave_preset", "wave_scale", "wave", "follow_value", "min_strength"},
	"distance": {"freq_ms", "wave_preset", "wave_scale", "texture_floor", "wave_window_ops", "wave_sample_step", "wave_advance_samples", "wave_envelope_curve"},
	"touch": {"freq_ms", "wave_preset", "wave_scale", "n_derivative", "derivative_params", "texture_floor",
		"wave_window_ops", "wave_sample_step", "wave_advance_samples", "wave_envelope_curve", "preset_bands"},
}

func checkMode(mode string) error {
	if _, ok := modeKeys[mode]; !ok {
		return httpErr(400, "invalid mode, use: shock, distance, touch")
	}
	return nil
}

func (a *App) apiModeConfigGet(r *http.Request) (any, error) {
	ch, err := lowerChannel(r.PathValue("channel"))
	if err != nil {
		return nil, err
	}
	mode := r.PathValue("mode")
	if err := checkMode(mode); err != nil {
		return nil, err
	}
	mc := config.AsMap(a.Cfg.Get("dglab3", "channel_"+ch, "mode_config"))
	cfg, tr := config.AsMap(mc[mode]), config.AsMap(mc["trigger_range"])
	if cfg == nil {
		cfg = config.M{}
	}
	if tr == nil {
		tr = config.M{}
	}
	return map[string]any{"config": cfg, "trigger_range": tr}, nil
}

func mergeInto(dst config.M, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}

func (a *App) apiModeConfigSet(r *http.Request) (any, error) {
	ch, err := lowerChannel(r.PathValue("channel"))
	if err != nil {
		return nil, err
	}
	mode := r.PathValue("mode")
	if err := checkMode(mode); err != nil {
		return nil, err
	}
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	a.Cfg.Write(func(adv, _ config.M) {
		mc := config.Sub(adv, true, "dglab3", "channel_"+ch, "mode_config")
		m := config.Sub(mc, true, mode)
		for _, k := range modeKeys[mode] {
			if v, ok := data[k]; ok {
				m[k] = v
			}
		}
		if tr := config.AsMap(data["trigger_range"]); tr != nil {
			mergeInto(config.Sub(mc, true, "trigger_range"), tr)
		}
	})
	if err := a.Cfg.Save(); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

func (a *App) apiComboGet(r *http.Request) (any, error) {
	ch, err := lowerChannel(r.PathValue("channel"))
	if err != nil {
		return nil, err
	}
	mc := config.AsMap(a.Cfg.Get("dglab3", "channel_"+ch, "mode_config"))
	get := func(k string) any {
		if m := config.AsMap(mc[k]); m != nil {
			return m
		}
		return config.M{}
	}
	return map[string]any{"combo": get("combo"), "shock": get("shock"), "touch": get("touch"), "trigger_range": get("trigger_range")}, nil
}

func (a *App) apiComboSet(r *http.Request) (any, error) {
	ch, err := lowerChannel(r.PathValue("channel"))
	if err != nil {
		return nil, err
	}
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	a.Cfg.Write(func(adv, _ config.M) {
		mc := config.Sub(adv, true, "dglab3", "channel_"+ch, "mode_config")
		if c := config.AsMap(data["combo"]); c != nil {
			mergeInto(config.Sub(mc, true, "combo"), c)
		}
		if s := config.AsMap(data["shock"]); s != nil {
			for _, k := range []string{"duration", "wave_preset", "wave_scale"} {
				if v, ok := s[k]; ok {
					config.Sub(mc, true, "shock")[k] = v
				}
			}
		}
		if t := config.AsMap(data["touch"]); t != nil {
			for _, k := range []string{"wave_preset", "wave_scale", "n_derivative"} {
				if v, ok := t[k]; ok {
					config.Sub(mc, true, "touch")[k] = v
				}
			}
		}
		if tr := config.AsMap(data["trigger_range"]); tr != nil {
			mergeInto(config.Sub(mc, true, "trigger_range"), tr)
		}
	})
	if err := a.Cfg.Save(); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

// ---------------------------------------------------------------- SPS

func (a *App) apiSPSGet(*http.Request) (any, error) {
	cfg := config.AsMap(config.DefaultAdvanced()["sps"])
	mergeInto(cfg, config.AsMap(a.Cfg.Get("sps")))
	ogb := map[string]any{}
	for _, ch := range []string{"a", "b"} {
		paths := []any{}
		for _, e := range config.AsList(a.Cfg.Get("dglab3", "channel_"+ch, "avatar_params")) {
			m := config.AsMap(e)
			p := config.Str(m["path"], "")
			if strings.HasPrefix(p, sps.OGBParameterPrefix) {
				paths = append(paths, map[string]any{"path": p, "mode": m["mode"], "enabled": config.Bool(m["enabled"], true)})
			}
		}
		ogb[ch] = paths
	}
	return map[string]any{"sps": cfg, "ogb_params": ogb}, nil
}

func (a *App) apiSPSSet(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	var out config.M
	var bad error
	a.Cfg.Write(func(adv, _ config.M) {
		s := config.Sub(adv, true, "sps")
		if v, ok := data["enabled"]; ok {
			s["enabled"] = config.Bool(v, true)
		}
		if v, ok := data["ogb_enabled_heartbeat"]; ok {
			s["ogb_enabled_heartbeat"] = config.Bool(v, true)
		}
		if v, ok := data["ogb_enabled_interval"]; ok {
			f, ok := config.ToFloat(v)
			if !ok {
				bad = httpErr(400, "ogb_enabled_interval must be a number")
				return
			}
			s["ogb_enabled_interval"] = config.Clamp(f, 1, 60)
		}
		if v, ok := data["ogb_target_host"]; ok {
			h := strings.TrimSpace(config.Str(v, ""))
			if h == "" {
				h = "127.0.0.1"
			}
			s["ogb_target_host"] = h
		}
		if v, ok := data["ogb_target_port"]; ok {
			p, ok := config.ToInt(v)
			if !ok {
				bad = httpErr(400, "ogb_target_port must be an integer")
				return
			}
			if p < 1 || p > 65535 {
				bad = httpErr(400, "ogb_target_port out of range")
				return
			}
			s["ogb_target_port"] = p
		}
		out = config.CopyMap(s)
	})
	if bad != nil {
		return nil, bad
	}
	if err := a.Cfg.Save(); err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "sps": out}, nil
}

func (a *App) apiSPSZones(*http.Request) (any, error) {
	type z struct {
		key  [2]string
		seen float64
	}
	a.ogbMu.Lock()
	list := make([]z, 0, len(a.ogbSeen))
	for k, v := range a.ogbSeen {
		list = append(list, z{k, v})
	}
	a.ogbMu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].seen > list[j].seen })
	zones := []any{}
	for _, e := range list {
		kind := sps.Kind(e.key[0])
		zones = append(zones, map[string]any{
			"trigger_type":   e.key[0],
			"zone_id":        e.key[1],
			"kind":           kind,
			"suggested_path": sps.OGBParameterPrefix + kind + "/" + e.key[1] + "/*",
			"last_seen":      e.seen,
		})
	}
	return map[string]any{"zones": zones}, nil
}
