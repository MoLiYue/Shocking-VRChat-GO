package app

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"shockingvrc/internal/config"
	"shockingvrc/internal/handler"
	"shockingvrc/internal/logx"
)

// ---------------------------------------------------------------- curves

var defaultCurve = []handler.Point{{X: 0, Y: 0}, {X: 1, Y: 1}}

func (a *App) curvePath() string { return a.Path("curve_config.yaml") }

func toPoints(v any) []handler.Point {
	pts := []handler.Point{}
	for _, p := range config.AsList(v) {
		m := config.AsMap(p)
		if m == nil {
			continue
		}
		pts = append(pts, handler.Point{X: config.Float(m["x"], 0), Y: config.Float(m["y"], 0)})
	}
	return pts
}

func (a *App) loadCurves() {
	v, err := config.ReadYAMLFile(a.curvePath())
	if err != nil {
		return
	}
	curves := map[string][]handler.Point{}
	for k, pts := range config.AsMap(v) {
		curves[k] = toPoints(pts)
	}
	a.curveMu.Lock()
	a.curves = curves
	a.curveMu.Unlock()
}

func (a *App) saveCurvesLocked() error {
	out := map[string]any{}
	for k, pts := range a.curves {
		list := []any{}
		for _, p := range pts {
			list = append(list, map[string]any{"x": p.X, "y": p.Y})
		}
		out[k] = list
	}
	return config.WriteYAMLAtomic(a.curvePath(), out)
}

// curvePoints mirrors get_curve_points(): exact key, else channel curve for
// "A"/"B", else the identity curve.
func (a *App) curvePoints(key string) []handler.Point {
	a.curveMu.RLock()
	defer a.curveMu.RUnlock()
	if key != "" {
		if p, ok := a.curves[key]; ok {
			return p
		}
		if u := strings.ToUpper(key); u == "A" || u == "B" {
			if p := a.curves["channel_"+strings.ToLower(key)]; len(p) > 0 {
				return p
			}
			return defaultCurve
		}
	}
	return defaultCurve
}

// applyCurveSim is the module-level apply_curve used by wave_simulate.
func applyCurveSim(v float64, pts []handler.Point) float64 {
	if len(pts) == 0 || v <= 0 {
		return 0
	}
	if v >= 1 {
		return math.Min(1, pts[len(pts)-1].Y)
	}
	for i := 0; i < len(pts)-1; i++ {
		if v >= pts[i].X && v <= pts[i+1].X {
			dx := pts[i+1].X - pts[i].X
			if dx <= 0 {
				return pts[i].Y
			}
			return pts[i].Y + (v-pts[i].X)/dx*(pts[i+1].Y-pts[i].Y)
		}
	}
	return pts[len(pts)-1].Y
}

func (a *App) apiCurveList(*http.Request) (any, error) {
	a.curveMu.RLock()
	defer a.curveMu.RUnlock()
	out := map[string]any{}
	for k, v := range a.curves {
		out[k] = v
	}
	return map[string]any{"curves": out, "default": defaultCurve}, nil
}

func (a *App) apiCurveGet(r *http.Request) (any, error) {
	k := r.PathValue("key")
	return map[string]any{"key": k, "points": a.curvePoints(k)}, nil
}

func (a *App) apiCurveSet(r *http.Request) (any, error) {
	k := r.PathValue("key")
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	pts := []handler.Point{}
	for _, p := range config.AsList(data["points"]) {
		m := config.AsMap(p)
		x, ok1 := config.ToFloat(orZero(m["x"]))
		y, ok2 := config.ToFloat(orZero(m["y"]))
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("could not convert point to float: %v", p)
		}
		pts = append(pts, handler.Point{X: round4(config.Clamp(x, 0, 1)), Y: round4(config.Clamp(y, 0, 1))})
	}
	sort.SliceStable(pts, func(i, j int) bool { return pts[i].X < pts[j].X })
	a.curveMu.Lock()
	a.curves[k] = pts
	err = a.saveCurvesLocked()
	a.curveMu.Unlock()
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "key": k, "points": pts}, nil
}

func orZero(v any) any {
	if v == nil {
		return 0
	}
	return v
}

func (a *App) apiCurveDelete(r *http.Request) (any, error) {
	k := r.PathValue("key")
	a.curveMu.Lock()
	defer a.curveMu.Unlock()
	if _, ok := a.curves[k]; ok {
		delete(a.curves, k)
		if err := a.saveCurvesLocked(); err != nil {
			return nil, err
		}
	}
	return map[string]any{"success": true}, nil
}

// ---------------------------------------------------------------- overlimit rules

func (a *App) overlimitPath() string { return a.Path("overlimit_rules.yaml") }

func (a *App) loadOverlimitRules() {
	v, err := config.ReadYAMLFile(a.overlimitPath())
	rules := []any{}
	if err == nil {
		if l := config.AsList(config.AsMap(v)["rules"]); l != nil {
			rules = l
		}
	}
	a.overlimitMu.Lock()
	a.overlimitRules = rules
	a.overlimitMu.Unlock()
}

func (a *App) saveOverlimitRulesLocked() error {
	return config.WriteYAMLAtomic(a.overlimitPath(), map[string]any{"rules": a.overlimitRules})
}

func evalCondition(cond config.M, state map[string]any) bool {
	cur, ok := state[config.Str(cond["param"], "")]
	if !ok || cur == nil {
		return false
	}
	c, ok1 := config.ToFloat(cur)
	th := cond["value"]
	if th == nil {
		th = 0
	}
	t, ok2 := config.ToFloat(th)
	if !ok1 || !ok2 {
		return false
	}
	switch config.Str(cond["operator"], "==") {
	case "==":
		return math.Abs(c-t) < 0.001
	case "!=":
		return math.Abs(c-t) >= 0.001
	case ">":
		return c > t
	case "<":
		return c < t
	case ">=":
		return c >= t
	case "<=":
		return c <= t
	}
	return false
}

// evaluateOverlimitLocked must be called with overlimitMu held.
func (a *App) evaluateOverlimitLocked() {
	maxL := map[string]int{"A": 0, "B": 0}
	for _, r := range a.overlimitRules {
		rule := config.AsMap(r)
		if rule == nil || !config.Bool(orTrue(rule["enabled"]), true) {
			continue
		}
		if !evalCondition(config.AsMap(rule["condition"]), a.oscParamState) {
			continue
		}
		v := config.Int(rule["limit_value"], 0)
		ch := config.Str(rule["channel"], "both")
		if (ch == "a" || ch == "both") && v > maxL["A"] {
			maxL["A"] = v
		}
		if (ch == "b" || ch == "both") && v > maxL["B"] {
			maxL["B"] = v
		}
	}
	if maxL["A"] == a.overlimitEffective["A"] && maxL["B"] == a.overlimitEffective["B"] {
		return
	}
	a.overlimitEffective["A"], a.overlimitEffective["B"] = maxL["A"], maxL["B"]
	l := a.limitsFromSettings()
	capA, capB := l.OverlimitA, l.OverlimitB
	if maxL["A"] > 0 {
		capA = maxL["A"]
	}
	if maxL["B"] > 0 {
		capB = maxL["B"]
	}
	a.Hub.SetOverlimit(maxL["A"] > 0, maxL["B"] > 0, capA, capB)
}

func orTrue(v any) any {
	if v == nil {
		return true
	}
	return v
}

func (a *App) overlimitOSCHook(addr string, args []any) {
	a.overlimitMu.Lock()
	defer a.overlimitMu.Unlock()
	if len(args) > 0 {
		a.oscParamState[addr] = args[0]
	}
	if len(a.overlimitRules) > 0 {
		a.evaluateOverlimitLocked()
	}
}

func (a *App) apiOverlimitGet(*http.Request) (any, error) {
	a.overlimitMu.Lock()
	defer a.overlimitMu.Unlock()
	return map[string]any{"rules": a.overlimitRules, "effective": a.overlimitEffective}, nil
}

func (a *App) apiOverlimitSet(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	rules := config.AsList(data["rules"])
	if rules == nil {
		rules = []any{}
	}
	a.overlimitMu.Lock()
	defer a.overlimitMu.Unlock()
	a.overlimitRules = rules
	if err := a.saveOverlimitRulesLocked(); err != nil {
		return nil, err
	}
	a.evaluateOverlimitLocked()
	logx.Info("[config] Overlimit rules saved: %d rules", len(rules))
	return map[string]any{"success": true, "rules": rules}, nil
}

func (a *App) apiOverlimitDelete(r *http.Request) (any, error) {
	idx, err := pathInt(r, "index")
	if err != nil {
		return nil, err
	}
	a.overlimitMu.Lock()
	defer a.overlimitMu.Unlock()
	if idx < 0 || idx >= len(a.overlimitRules) {
		return nil, httpErr(400, "invalid index")
	}
	removed := config.AsMap(a.overlimitRules[idx])
	a.overlimitRules = append(a.overlimitRules[:idx:idx], a.overlimitRules[idx+1:]...)
	if err := a.saveOverlimitRulesLocked(); err != nil {
		return nil, err
	}
	a.evaluateOverlimitLocked()
	name := any(idx)
	if removed != nil && removed["name"] != nil {
		name = removed["name"]
	}
	logx.Info("[config] Overlimit rule deleted: %v", name)
	return map[string]any{"success": true, "rules": a.overlimitRules}, nil
}

// ---------------------------------------------------------------- profiles

func (a *App) profilesDir() string { return a.Path("profiles") }

// safeName mirrors re.sub(r'[^\w\u4e00-\u9fff\-]', '_', name.strip())[:50].
func safeName(name string) string {
	var out []rune
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || (r >= 0x4e00 && r <= 0x9fff) {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) > 50 {
		out = out[:50]
	}
	return string(out)
}

func (a *App) apiProfilesList(*http.Request) (any, error) {
	_ = os.MkdirAll(a.profilesDir(), 0o755)
	entries, _ := os.ReadDir(a.profilesDir())
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	sort.Strings(names)
	return map[string]any{"profiles": names}, nil
}

func (a *App) apiProfileSave(r *http.Request) (any, error) {
	_ = os.MkdirAll(a.profilesDir(), 0o755)
	name := safeName(r.PathValue("name"))
	if name == "" {
		return nil, httpErr(400, "Invalid name")
	}
	adv, basic := a.Cfg.Snapshot()
	if err := config.WriteYAMLAtomic(filepath.Join(a.profilesDir(), name+".yaml"),
		map[string]any{"basic": basic, "advanced": config.StripBasic(adv)}); err != nil {
		return nil, err
	}
	logx.Info("[profile] Saved profile: %s", name)
	return map[string]any{"success": true, "name": name}, nil
}

func (a *App) apiProfileLoad(r *http.Request) (any, error) {
	name := safeName(r.PathValue("name"))
	p := filepath.Join(a.profilesDir(), name+".yaml")
	v, err := config.ReadYAMLFile(p)
	if os.IsNotExist(err) {
		return nil, httpErr(404, "Profile not found")
	}
	if err != nil {
		return nil, httpErr(500, "%v", err)
	}
	data := config.AsMap(v)
	basic := config.AsMap(config.MergeDefaults(config.DefaultBasic(), orMap(data["basic"])))
	adv := config.AsMap(config.MergeDefaults(config.DefaultAdvanced(), orMap(data["advanced"])))
	for _, ch := range config.Channels {
		ac := config.Sub(adv, true, "dglab3", ch)
		bc := config.Sub(basic, true, "dglab3", ch)
		ac["avatar_params"] = config.DeepCopy(bc["avatar_params"])
		ac["mode"] = bc["mode"]
		ac["strength_limit"] = bc["strength_limit"]
		config.NormalizeAvatarParams(ac)
	}
	config.Sub(adv, true, "ws")["master_uuid"] = a.Cfg.Get("ws", "master_uuid")
	a.applySettings(adv, basic)
	if err := a.Cfg.Save(); err != nil {
		return nil, httpErr(500, "%v", err)
	}
	logx.Success("[profile] Loaded profile: %s", name)
	return map[string]any{"success": true, "name": name}, nil
}

func orMap(v any) config.M {
	if m := config.AsMap(v); m != nil {
		return m
	}
	return config.M{}
}

func (a *App) apiProfileDelete(r *http.Request) (any, error) {
	name := safeName(r.PathValue("name"))
	p := filepath.Join(a.profilesDir(), name+".yaml")
	if err := os.Remove(p); err != nil {
		return nil, httpErr(404, "Not found")
	}
	return map[string]any{"success": true}, nil
}
