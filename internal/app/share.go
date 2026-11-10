package app

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"shockingvrc/internal/config"
	"shockingvrc/internal/handler"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/sps"
	"shockingvrc/internal/wave"
)

// Parameter-setup sharing. A share is a versioned JSON document with the
// parameter mapping, the mode settings they use, their curves, overlimit
// rules and the wave presets referenced. It travels as
//   - a PNG share card with the data in a tEXt chunk ("svrc-share"),
//   - a .svrc file (zip: share.json + preview.png),
//   - a text share code "SVRC1:<base64url(zlib(json))>".
// Strength limits and anything network / personal are never included
// (the limits only as a read-only reference) and never imported.

const (
	shareFormat     = "svrc-share"
	shareVersion    = 1
	shareCodePrefix = "SVRC1:"
	shareChunkKey   = "svrc-share"
	shareMaxInput   = 8 << 20 // uploaded file / code
	shareMaxJSON    = 5 << 20 // decompressed share document
	// Imported overlimit rules above this are flagged in the preview.
	shareHighBoostValue   = 30
	shareHighBoostPercent = 50
)

var errNoShareData = errors.New("no share data found")

// ---------------------------------------------------------------- build

type shareOptions struct {
	Title, Note   string
	IncludeAvatar bool
}

func shareOptsFrom(r *http.Request) shareOptions {
	q := r.URL.Query()
	o := shareOptions{Title: q.Get("title"), Note: q.Get("note"), IncludeAvatar: q.Get("avatar") == "1"}
	if r.MultipartForm != nil {
		if v := r.FormValue("title"); v != "" {
			o.Title = v
		}
		if v := r.FormValue("note"); v != "" {
			o.Note = v
		}
		if r.FormValue("avatar") == "1" {
			o.IncludeAvatar = true
		}
	}
	o.Title = truncRunes(strings.TrimSpace(o.Title), 60)
	o.Note = truncRunes(strings.TrimSpace(o.Note), 300)
	return o
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func shortParamPath(p string) string { return strings.TrimPrefix(p, sps.AvatarParameterPrefix) }

// currentAvatarName resolves the last /avatar/change id to its name via
// VRChat's OSC config ("" if unknown).
func (a *App) currentAvatarName() string {
	id, _ := a.avatarID.Load().(string)
	if id == "" {
		return ""
	}
	path := findAvatarConfig(id)
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var c avatarOSCConfig
	if json.Unmarshal([]byte(strings.TrimPrefix(string(b), "\ufeff")), &c) != nil {
		return ""
	}
	return c.Name
}

// usedModes returns the modes a channel's enabled params use (ranges
// expanded, combo needing shock + touch).
func usedModes(params []any) map[string]bool {
	out := map[string]bool{}
	add := func(m string) {
		out[m] = true
		if m == "combo" {
			out["shock"], out["touch"] = true, true
		}
	}
	for _, p := range params {
		pm := config.AsMap(p)
		mode := config.Str(pm["mode"], "distance")
		if mode == handler.RangesMode {
			for _, r := range config.AsList(pm["ranges"]) {
				add(config.Str(config.AsMap(r)["mode"], ""))
			}
			continue
		}
		add(mode)
	}
	return out
}

// presetsOf lists wave preset names referenced by a mode config.
func presetsOf(mc config.M) []string {
	set := map[string]bool{}
	for _, mode := range []string{"shock", "distance", "touch"} {
		m := config.AsMap(mc[mode])
		if n := config.Str(m["wave_preset"], ""); n != "" {
			set[n] = true
		}
		for _, b := range config.AsList(m["preset_bands"]) {
			if n := config.Str(config.AsMap(b)["wave_preset"], ""); n != "" {
				set[n] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// presetRaw finds the JSON document of a loaded preset in the presets dir.
func (a *App) presetRaw(name string) map[string]any {
	files, _ := filepath.Glob(filepath.Join(a.Lib.Dir(), "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || len(b) > 2<<20 {
			continue
		}
		var raw map[string]any
		if json.Unmarshal(b, &raw) != nil {
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		if config.Str(raw["name"], stem) == name {
			return raw
		}
	}
	return nil
}

func (a *App) buildShare(o shareOptions) map[string]any {
	adv, basic := a.Cfg.Snapshot()
	channels := map[string]any{}
	modeCfg := map[string]any{}
	presets := map[string]any{}
	curves := map[string]any{}
	limits := map[string]any{}
	paths := map[string]bool{}
	for _, ch := range []string{"a", "b"} {
		key := "channel_" + ch
		var params []any
		for _, p := range config.AsList(config.Get(basic, "dglab3", key, "avatar_params")) {
			pm := config.AsMap(config.DeepCopy(p))
			if pm == nil || !config.Bool(pm["enabled"], true) {
				continue // only what is in use
			}
			path := config.Str(pm["path"], "")
			paths[path] = true
			pm["path"] = shortParamPath(path)
			delete(pm, "enabled")
			params = append(params, pm)
		}
		if params == nil {
			params = []any{}
		}
		channels[ch] = map[string]any{"params": params}
		mc := config.AsMap(config.Get(adv, "dglab3", key, "mode_config"))
		sel := map[string]any{}
		for m := range usedModes(params) {
			if v, ok := mc[m]; ok {
				sel[m] = config.DeepCopy(v)
			}
		}
		if len(params) > 0 {
			sel["trigger_range"] = config.DeepCopy(mc["trigger_range"])
		}
		modeCfg[ch] = sel
		for _, n := range presetsOf(sel) {
			if raw := a.presetRaw(n); raw != nil {
				presets[n] = raw
			}
		}
		limits[ch] = config.Get(basic, "dglab3", key, "strength_limit")
	}
	a.curveMu.RLock()
	for k, pts := range a.curves {
		full := k
		if !strings.HasPrefix(full, "/") && !strings.HasPrefix(full, "channel_") {
			full = "/" + full
		}
		if paths[full] || k == "channel_a" || k == "channel_b" {
			curves[shortParamPath(full)] = pts
		}
	}
	a.curveMu.RUnlock()
	a.overlimitMu.Lock()
	var rules []any
	for _, r := range a.overlimitRules {
		rm := config.AsMap(config.DeepCopy(r))
		if rm == nil || !config.Bool(orTrue(rm["enabled"]), true) {
			continue
		}
		if c := config.AsMap(rm["condition"]); c != nil {
			c["param"] = shortParamPath(config.Str(c["param"], ""))
		}
		delete(rm, "enabled")
		rules = append(rules, rm)
	}
	a.overlimitMu.Unlock()
	if rules == nil {
		rules = []any{}
	}
	s := map[string]any{
		"format": shareFormat, "version": shareVersion, "app": Version,
		"created": time.Now().Format(time.RFC3339),
		"title":   o.Title, "note": o.Note,
		"channels": channels, "mode_config": modeCfg, "curves": curves,
		"overlimit_rules": rules, "presets": presets,
		"reference": map[string]any{"strength_limit": limits},
	}
	if o.IncludeAvatar {
		if n := a.currentAvatarName(); n != "" {
			s["avatar"] = n
		}
	}
	return s
}

// ---------------------------------------------------------------- encode / decode

func shareJSON(s map[string]any) ([]byte, error) { return json.Marshal(s) }

func encodeShareCode(doc []byte) string {
	var buf bytes.Buffer
	w, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	_, _ = w.Write(doc)
	_ = w.Close()
	return shareCodePrefix + base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

func decodeShareCode(code string) ([]byte, error) {
	code = strings.TrimSpace(code)
	code = strings.Join(strings.Fields(code), "") // pasted with line breaks
	if !strings.HasPrefix(code, shareCodePrefix) {
		return nil, errNoShareData
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(code[len(shareCodePrefix):], "="))
	if err != nil {
		return nil, fmt.Errorf("invalid share code: %w", err)
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid share code: %w", err)
	}
	defer zr.Close()
	b, err := io.ReadAll(io.LimitReader(zr, shareMaxJSON+1))
	if err != nil {
		return nil, fmt.Errorf("invalid share code: %w", err)
	}
	if len(b) > shareMaxJSON {
		return nil, errors.New("share data too large")
	}
	return b, nil
}

var pngSig = []byte("\x89PNG\r\n\x1a\n")

// injectPNGText inserts a tEXt chunk right before IEND.
func injectPNGText(png []byte, key, text string) ([]byte, error) {
	if !bytes.HasPrefix(png, pngSig) {
		return nil, errors.New("not a PNG image")
	}
	off := len(pngSig)
	for off+12 <= len(png) {
		n := int(binary.BigEndian.Uint32(png[off:]))
		typ := string(png[off+4 : off+8])
		if n < 0 || off+12+n > len(png) {
			return nil, errors.New("corrupt PNG")
		}
		if typ == "IEND" {
			data := append([]byte(key), 0)
			data = append(data, text...)
			chunk := make([]byte, 0, len(data)+12)
			chunk = binary.BigEndian.AppendUint32(chunk, uint32(len(data)))
			chunk = append(chunk, "tEXt"...)
			chunk = append(chunk, data...)
			chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
			out := append([]byte{}, png[:off]...)
			out = append(out, chunk...)
			return append(out, png[off:]...), nil
		}
		off += 12 + n
	}
	return nil, errors.New("corrupt PNG (no IEND)")
}

// pngText returns the text of the tEXt chunk with key.
func pngText(png []byte, key string) (string, bool) {
	off := len(pngSig)
	for off+12 <= len(png) {
		n := int(binary.BigEndian.Uint32(png[off:]))
		if n < 0 || off+12+n > len(png) {
			return "", false
		}
		if string(png[off+4:off+8]) == "tEXt" {
			data := png[off+8 : off+8+n]
			if i := bytes.IndexByte(data, 0); i >= 0 && string(data[:i]) == key {
				return string(data[i+1:]), true
			}
		}
		off += 12 + n
	}
	return "", false
}

// decodeShareInput accepts a PNG share card, a .svrc zip, a share code or a
// raw share JSON and returns the share document.
func decodeShareInput(b []byte) (map[string]any, string, error) {
	var doc []byte
	var kind string
	switch {
	case bytes.HasPrefix(b, pngSig):
		kind = "png"
		code, ok := pngText(b, shareChunkKey)
		if !ok {
			return nil, kind, errNoShareData
		}
		var err error
		if doc, err = decodeShareCode(code); err != nil {
			return nil, kind, err
		}
	case bytes.HasPrefix(b, []byte("PK\x03\x04")):
		kind = "svrc"
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return nil, kind, fmt.Errorf("invalid .svrc file: %w", err)
		}
		for _, f := range zr.File {
			if f.Name != "share.json" { // whitelist: nothing else is read
				continue
			}
			if f.UncompressedSize64 > shareMaxJSON {
				return nil, kind, errors.New("share data too large")
			}
			rc, err := f.Open()
			if err != nil {
				return nil, kind, err
			}
			doc, err = io.ReadAll(io.LimitReader(rc, shareMaxJSON+1))
			rc.Close()
			if err != nil {
				return nil, kind, err
			}
			if len(doc) > shareMaxJSON {
				return nil, kind, errors.New("share data too large")
			}
		}
		if doc == nil {
			return nil, kind, errNoShareData
		}
	default:
		t := strings.TrimSpace(string(b))
		if strings.HasPrefix(t, shareCodePrefix) {
			kind = "code"
			var err error
			if doc, err = decodeShareCode(t); err != nil {
				return nil, kind, err
			}
		} else if strings.HasPrefix(t, "{") {
			kind, doc = "json", []byte(t)
		} else {
			return nil, "", errNoShareData
		}
	}
	var s map[string]any
	if err := json.Unmarshal(doc, &s); err != nil {
		return nil, kind, fmt.Errorf("invalid share data: %w", err)
	}
	if config.Str(s["format"], "") != shareFormat {
		return nil, kind, errNoShareData
	}
	return s, kind, nil
}

// ---------------------------------------------------------------- preview

func fullParamPath(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return sps.AvatarParameterPrefix + p
}

func ruleIsHigh(r config.M) bool {
	v := config.Float(r["limit_boost"], 0)
	if config.Str(r["boost_unit"], "value") == "percent" {
		return v > shareHighBoostPercent
	}
	return v > shareHighBoostValue
}

func (a *App) sharePreview(s map[string]any) map[string]any {
	adv, basic := a.Cfg.Snapshot()
	chs := map[string]any{}
	for _, ch := range []string{"a", "b"} {
		key := "channel_" + ch
		cur := map[string]config.M{}
		for _, p := range config.AsList(config.Get(basic, "dglab3", key, "avatar_params")) {
			pm := config.AsMap(p)
			cur[config.Str(pm["path"], "")] = pm
		}
		var items []any
		seen := map[string]bool{}
		for _, p := range config.AsList(config.Get(s, "channels", ch, "params")) {
			pm := config.AsMap(p)
			path := fullParamPath(config.Str(pm["path"], ""))
			seen[path] = true
			status := "new"
			if old, ok := cur[path]; ok {
				status = "same"
				o, n := config.DeepCopy(old).(map[string]any), config.DeepCopy(pm).(map[string]any)
				delete(o, "enabled")
				o["path"], n["path"] = path, path
				if !config.Equal(o, n) {
					status = "changed"
				}
			}
			items = append(items, map[string]any{"path": path, "mode": pm["mode"], "status": status, "ranges": pm["ranges"]})
		}
		var removed []any
		for path := range cur {
			if !seen[path] {
				removed = append(removed, path)
			}
		}
		sort.Slice(removed, func(i, j int) bool { return removed[i].(string) < removed[j].(string) })
		var modes []any
		curMC := config.AsMap(config.Get(adv, "dglab3", key, "mode_config"))
		for m, v := range config.AsMap(config.Get(s, "mode_config", ch)) {
			cv := config.AsMap(curMC[m])
			changed := false
			for k, nv := range config.AsMap(v) {
				if !config.Equal(cv[k], nv) {
					changed = true
					break
				}
			}
			modes = append(modes, map[string]any{"mode": m, "changed": changed})
		}
		sort.Slice(modes, func(i, j int) bool {
			return config.Str(config.AsMap(modes[i])["mode"], "") < config.Str(config.AsMap(modes[j])["mode"], "")
		})
		chs[ch] = map[string]any{"params": orEmpty(items), "removed_on_replace": orEmpty(removed), "modes": orEmpty(modes)}
	}
	var rules []any
	for _, r := range config.AsList(s["overlimit_rules"]) {
		rm := config.AsMap(r)
		rules = append(rules, map[string]any{"name": rm["name"], "channel": rm["channel"], "limit_boost": rm["limit_boost"],
			"boost_unit": config.Str(rm["boost_unit"], "value"), "condition": rm["condition"], "high": ruleIsHigh(rm)})
	}
	var presets []any
	for n := range config.AsMap(s["presets"]) {
		state := "new"
		if a.Lib.Get(n) != nil {
			state = "exists" // kept: local presets are never overwritten
		}
		presets = append(presets, map[string]any{"name": n, "state": state})
	}
	sort.Slice(presets, func(i, j int) bool {
		return config.Str(config.AsMap(presets[i])["name"], "") < config.Str(config.AsMap(presets[j])["name"], "")
	})
	curves := []any{}
	for k := range config.AsMap(s["curves"]) {
		curves = append(curves, k)
	}
	sort.Slice(curves, func(i, j int) bool { return curves[i].(string) < curves[j].(string) })
	warnings := []any{}
	if VersionNewer(config.Str(s["app"], ""), Version) {
		warnings = append(warnings, "newer_version")
	}
	if config.Int(s["version"], 0) > shareVersion {
		warnings = append(warnings, "newer_format")
	}
	return map[string]any{
		"meta":     map[string]any{"title": s["title"], "note": s["note"], "avatar": s["avatar"], "app": s["app"], "created": s["created"]},
		"channels": chs, "overlimit_rules": orEmpty(rules), "presets": orEmpty(presets), "curves": curves,
		"reference": s["reference"],
		"current_limits": map[string]any{"a": config.Get(basic, "dglab3", "channel_a", "strength_limit"),
			"b": config.Get(basic, "dglab3", "channel_b", "strength_limit")},
		"warnings": warnings,
	}
}

func orEmpty(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

// ---------------------------------------------------------------- apply

type shareApplyOpts struct {
	Params, ModeConfig, Curves, Overlimit, Presets bool
	Replace                                        bool // replace channel params instead of merging
	EnableRules                                    bool // keep imported rules enabled (default: disabled)
	Channels                                       map[string]bool
}

func (a *App) applyShare(s map[string]any, o shareApplyOpts) (map[string]any, error) {
	// 1. validate everything first: nothing changes if any part is invalid
	newParams := map[string][]any{}
	if o.Params {
		for _, ch := range []string{"a", "b"} {
			if !o.Channels[ch] {
				continue
			}
			key := "channel_" + ch
			var incoming []any
			for _, p := range config.AsList(config.Get(s, "channels", ch, "params")) {
				pm := config.AsMap(config.DeepCopy(p))
				if pm == nil {
					continue
				}
				pm["path"] = fullParamPath(config.Str(pm["path"], ""))
				pm["enabled"] = true
				incoming = append(incoming, pm)
			}
			merged := incoming
			if !o.Replace {
				byPath := map[string]int{}
				merged = nil
				for _, p := range config.AsList(a.Cfg.GetBasic("dglab3", key, "avatar_params")) {
					pm := config.AsMap(p)
					byPath[config.Str(pm["path"], "")] = len(merged)
					merged = append(merged, pm)
				}
				for _, p := range incoming {
					if i, ok := byPath[config.Str(config.AsMap(p)["path"], "")]; ok {
						merged[i] = p
					} else {
						merged = append(merged, p)
					}
				}
			}
			defMode := config.Str(a.Cfg.GetBasic("dglab3", key, "mode"), "distance")
			out, errs := validateParams(ch, merged, defMode)
			if len(errs) > 0 {
				return nil, validationError(errs)
			}
			newParams[key] = out
		}
	}

	// 2. backup
	backup := "导入前备份_" + time.Now().Format("20060102_150405")
	_ = os.MkdirAll(a.profilesDir(), 0o755)
	adv0, basic0 := a.Cfg.Snapshot()
	if err := config.WriteYAMLAtomic(filepath.Join(a.profilesDir(), backup+".yaml"),
		map[string]any{"basic": basic0, "advanced": config.StripBasic(adv0)}); err != nil {
		return nil, fmt.Errorf("backup failed: %w", err)
	}
	logx.Info("[share] Backup saved as profile %s", backup)

	res := map[string]any{"success": true, "backup": backup}
	// 3. presets (before params so they resolve on reconfigure)
	if o.Presets {
		written := 0
		for n, raw := range config.AsMap(s["presets"]) {
			rm := config.AsMap(raw)
			if a.Lib.Get(n) != nil || rm == nil {
				continue
			}
			if _, err := wave.BuildPreset(rm, n); err != nil {
				continue
			}
			rm["name"] = n
			b, _ := json.MarshalIndent(rm, "", "  ")
			if err := os.WriteFile(filepath.Join(a.Lib.Dir(), safeName(n)+".json"), b, 0o644); err == nil {
				written++
			}
		}
		if written > 0 {
			a.Lib.Reload()
		}
		res["presets_added"] = written
	}
	// 4. mode config (only known keys)
	if o.ModeConfig {
		a.Cfg.Write(func(adv, _ config.M) {
			for _, ch := range []string{"a", "b"} {
				if !o.Channels[ch] {
					continue
				}
				mc := config.Sub(adv, true, "dglab3", "channel_"+ch, "mode_config")
				for m, v := range config.AsMap(config.Get(s, "mode_config", ch)) {
					vm := config.AsMap(v)
					var keys []string
					switch m {
					case "trigger_range":
						keys = []string{"bottom", "top"}
					case "combo":
						keys = []string{"enabled", "switch_duration"}
					default:
						keys = modeKeys[m]
					}
					for _, k := range keys {
						if nv, ok := vm[k]; ok {
							config.Sub(mc, true, m)[k] = config.DeepCopy(nv)
						}
					}
				}
			}
		})
	}
	// 5. params (saves + applies live) / plain save
	if o.Params && len(newParams) > 0 {
		var chs []channelParams
		for key, p := range newParams {
			chs = append(chs, channelParams{key: key, params: p})
		}
		if err := a.applyParams(chs); err != nil {
			return nil, err
		}
	} else if err := a.Cfg.Save(); err != nil {
		return nil, err
	}
	// 6. curves
	if o.Curves {
		n := 0
		a.curveMu.Lock()
		for k, v := range config.AsMap(s["curves"]) {
			key := k
			if !strings.HasPrefix(k, "channel_") {
				key = fullParamPath(k)
			} else if (k == "channel_a" && !o.Channels["a"]) || (k == "channel_b" && !o.Channels["b"]) {
				continue
			}
			pts := []handler.Point{}
			for _, p := range config.AsList(v) {
				pm := config.AsMap(p)
				x, ok1 := config.ToFloat(pm["x"])
				y, ok2 := config.ToFloat(pm["y"])
				if ok1 && ok2 {
					pts = append(pts, handler.Point{X: round4(config.Clamp(x, 0, 1)), Y: round4(config.Clamp(y, 0, 1))})
				}
			}
			if len(pts) == 0 {
				continue
			}
			sort.SliceStable(pts, func(i, j int) bool { return pts[i].X < pts[j].X })
			a.curves[key] = pts
			n++
		}
		err := a.saveCurvesLocked()
		a.curveMu.Unlock()
		if err != nil {
			return nil, err
		}
		res["curves"] = n
	}
	// 7. overlimit rules: appended, disabled unless asked otherwise
	if o.Overlimit {
		var add []any
		for _, r := range config.AsList(s["overlimit_rules"]) {
			rm := config.AsMap(config.DeepCopy(r))
			if rm == nil {
				continue
			}
			if c := config.AsMap(rm["condition"]); c != nil {
				c["param"] = fullParamPath(config.Str(c["param"], ""))
			}
			rm["enabled"] = o.EnableRules
			rm["name"] = truncRunes(config.Str(rm["name"], "rule"), 40) + "（导入）"
			rm["limit_boost"] = config.Clamp(math.Abs(config.Float(rm["limit_boost"], 0)), 0, 1000)
			add = append(add, rm)
		}
		a.migrateOverlimitRules(add)
		a.overlimitMu.Lock()
		a.overlimitRules = append(a.overlimitRules, add...)
		a.overlimitState = nil
		err := a.saveOverlimitRulesLocked()
		a.updateOverlimitTriggersLocked(time.Now(), nil)
		a.overlimitMu.Unlock()
		if err != nil {
			return nil, err
		}
		res["rules_added"] = len(add)
	}
	logx.Success("[share] Imported %q (%v)", config.Str(s["title"], ""), res)
	return res, nil
}

// ---------------------------------------------------------------- API

func (a *App) apiShareData(r *http.Request) (any, error) {
	return a.buildShare(shareOptsFrom(r)), nil
}

func (a *App) apiShareCode(r *http.Request) (any, error) {
	doc, err := shareJSON(a.buildShare(shareOptsFrom(r)))
	if err != nil {
		return nil, err
	}
	return map[string]any{"code": encodeShareCode(doc), "bytes": len(doc)}, nil
}

// readShareImage reads the rendered card uploaded by the UI (optional).
func readShareImage(r *http.Request) ([]byte, error) {
	if err := r.ParseMultipartForm(shareMaxInput); err != nil {
		return nil, httpErr(400, "invalid upload: %v", err)
	}
	f, _, err := r.FormFile("image")
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, shareMaxInput+1))
	if err != nil || len(b) > shareMaxInput {
		return nil, httpErr(400, "image too large")
	}
	if !bytes.HasPrefix(b, pngSig) {
		return nil, httpErr(400, "image must be a PNG")
	}
	return b, nil
}

func shareFileName(title, ext string) string {
	n := safeName(title)
	if strings.Trim(n, "_") == "" {
		n = "shocking_vrchat_share"
	}
	return n + "_" + time.Now().Format("20060102") + ext
}

func attachment(name, ctype string, body []byte) rawResponse {
	return rawResponse{contentType: ctype, body: body, headers: map[string]string{
		"Content-Disposition": `attachment; filename="share` + filepath.Ext(name) + `"; filename*=UTF-8''` + urlPathEscape(name)}}
}

func urlPathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// apiSharePNG embeds the share data into the uploaded card image.
func (a *App) apiSharePNG(r *http.Request) (any, error) {
	img, err := readShareImage(r)
	if err != nil {
		return nil, err
	}
	if img == nil {
		return nil, httpErr(400, "missing image")
	}
	o := shareOptsFrom(r)
	doc, err := shareJSON(a.buildShare(o))
	if err != nil {
		return nil, err
	}
	out, err := injectPNGText(img, shareChunkKey, encodeShareCode(doc))
	if err != nil {
		return nil, httpErr(400, "%v", err)
	}
	return attachment(shareFileName(o.Title, ".png"), "image/png", out), nil
}

// apiShareSvrc packs share.json (+ the card as preview.png) into a .svrc.
func (a *App) apiShareSvrc(r *http.Request) (any, error) {
	img, err := readShareImage(r)
	if err != nil {
		return nil, err
	}
	o := shareOptsFrom(r)
	doc, err := json.MarshalIndent(a.buildShare(o), "", "  ")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("share.json")
	_, _ = w.Write(doc)
	if img != nil {
		w, _ = zw.Create("preview.png")
		_, _ = w.Write(img)
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return attachment(shareFileName(o.Title, ".svrc"), "application/zip", buf.Bytes()), nil
}

// readShareUpload reads a dropped file or a pasted code.
func readShareUpload(r *http.Request) ([]byte, error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(shareMaxInput); err != nil {
			return nil, httpErr(400, "invalid upload: %v", err)
		}
		if f, _, err := r.FormFile("file"); err == nil {
			defer f.Close()
			b, err := io.ReadAll(io.LimitReader(f, shareMaxInput+1))
			if err != nil || len(b) > shareMaxInput {
				return nil, httpErr(400, "file too large")
			}
			return b, nil
		}
		if c := r.FormValue("code"); c != "" {
			return []byte(c), nil
		}
		return nil, httpErr(400, "missing file or code")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, shareMaxInput+1))
	if err != nil || len(b) > shareMaxInput {
		return nil, httpErr(400, "input too large")
	}
	var body struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(b, &body) == nil && body.Code != "" {
		return []byte(body.Code), nil
	}
	return b, nil
}

func shareDecodeError(kind string, err error) error {
	if errors.Is(err, errNoShareData) {
		code := "no_share_data"
		if kind == "png" {
			code = "png_stripped" // e.g. recompressed by a chat app
		}
		return &HTTPErrorBody{Code: 400, Detail: code, Extra: map[string]any{"code": code}}
	}
	return httpErr(400, "%v", err)
}

func (a *App) apiSharePreview(r *http.Request) (any, error) {
	b, err := readShareUpload(r)
	if err != nil {
		return nil, err
	}
	s, kind, err := decodeShareInput(b)
	if err != nil {
		return nil, shareDecodeError(kind, err)
	}
	p := a.sharePreview(s)
	p["kind"] = kind
	p["share"] = s // sent back on apply
	return p, nil
}

func (a *App) apiShareApply(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	doc, _ := json.Marshal(data["share"])
	s, _, err := decodeShareInput(doc)
	if err != nil {
		return nil, shareDecodeError("json", err)
	}
	sec := config.AsMap(data["sections"])
	on := func(k string) bool { return config.Bool(sec[k], true) }
	chs := map[string]bool{"a": true, "b": true}
	if c := config.AsMap(data["channels"]); c != nil {
		chs = map[string]bool{"a": config.Bool(c["a"], true), "b": config.Bool(c["b"], true)}
	}
	return a.applyShare(s, shareApplyOpts{
		Params: on("params"), ModeConfig: on("mode_config"), Curves: on("curves"),
		Overlimit: on("overlimit"), Presets: on("presets"),
		Replace:     config.Str(data["mode"], "merge") == "replace",
		EnableRules: config.Bool(data["enable_rules"], false),
		Channels:    chs,
	})
}
