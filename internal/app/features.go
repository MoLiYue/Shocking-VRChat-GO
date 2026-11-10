package app

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"shockingvrc/internal/config"
	"shockingvrc/internal/device"
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

// Overlimit rules temporarily raise a channel's strength cap:
//
//   - limit_boost: how much the cap is raised (on top of strength_limit)
//   - decay_seconds: after the condition becomes true the boost falls back
//     to 0 over this time; a new trigger (condition false -> true) restores
//     the full boost. 0 = hold the boost while the condition is true.
//   - decay_curve: shape of the fall-back (see decayRemaining): a preset
//     name or a list of {x: elapsed fraction, y: remaining fraction} points.
//
// Several active rules on a channel do not add up: the largest current
// boost wins.
const defaultOverlimitDecay = 10.0

type overlimitRuleState struct {
	active    bool      // condition currently true
	triggered time.Time // last false -> true transition
}

func (a *App) loadOverlimitRules() {
	v, err := config.ReadYAMLFile(a.overlimitPath())
	rules := []any{}
	if err == nil {
		if l := config.AsList(config.AsMap(v)["rules"]); l != nil {
			rules = l
		}
	}
	migrated := a.migrateOverlimitRules(rules)
	a.overlimitMu.Lock()
	a.overlimitRules = rules
	a.overlimitState = make([]overlimitRuleState, len(rules))
	if migrated {
		if err := a.saveOverlimitRulesLocked(); err != nil {
			logx.Warn("[config] Could not save migrated overlimit rules: %v", err)
		}
	}
	a.overlimitMu.Unlock()
}

// migrateOverlimitRules converts the old absolute limit_value ("raise the
// cap to N") into limit_boost ("raise the cap by N") using the current
// strength limits, so existing rules keep their peak.
func (a *App) migrateOverlimitRules(rules []any) bool {
	l := a.limitsFromSettings()
	changed := false
	for _, r := range rules {
		rule := config.AsMap(r)
		if rule == nil {
			continue
		}
		if _, ok := rule["decay_seconds"]; !ok {
			rule["decay_seconds"] = defaultOverlimitDecay
			changed = true
		}
		if _, ok := rule["decay_curve"]; !ok {
			rule["decay_curve"] = "linear"
			changed = true
		}
		old, hasOld := rule["limit_value"]
		if !hasOld {
			continue
		}
		delete(rule, "limit_value")
		changed = true
		if _, ok := rule["limit_boost"]; ok {
			continue
		}
		base := map[string]int{"a": l.LimitA, "b": l.LimitB, "both": max(l.LimitA, l.LimitB)}[config.Str(rule["channel"], "both")]
		boost := max(0, config.Int(old, 0)-base)
		rule["limit_boost"] = boost
		logx.Info("[config] Overlimit rule %v: limit_value %v -> limit_boost +%d", rule["name"], old, boost)
	}
	return changed
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

// Change operators fire once per qualifying change of the parameter
// (e.g. 10 -> 11) instead of while a comparison holds. The condition value
// is the minimum change (0 = any change).
var changeOperators = map[string]bool{"changed": true, "increased": true, "decreased": true}

// paramChange describes the OSC update being processed (nil when rules
// are re-evaluated without a new value, e.g. after saving them).
type paramChange struct {
	addr      string
	prev, cur any
}

func changeFires(cond config.M, ch *paramChange) bool {
	if ch == nil || ch.addr != config.Str(cond["param"], "") || ch.prev == nil {
		return false
	}
	p, ok1 := config.ToFloat(ch.prev)
	c, ok2 := config.ToFloat(ch.cur)
	if !ok1 || !ok2 {
		return false
	}
	minDelta := math.Max(0.001, math.Abs(config.Float(cond["value"], 0)))
	d := c - p
	switch config.Str(cond["operator"], "") {
	case "changed":
		return math.Abs(d) >= minDelta-1e-9
	case "increased":
		return d >= minDelta-1e-9
	case "decreased":
		return -d >= minDelta-1e-9
	}
	return false
}

// updateOverlimitTriggersLocked re-evaluates rule conditions, recording
// false -> true transitions (and qualifying changes for change
// operators). Must be called with overlimitMu held.
func (a *App) updateOverlimitTriggersLocked(now time.Time, ch *paramChange) {
	if len(a.overlimitState) != len(a.overlimitRules) {
		a.overlimitState = make([]overlimitRuleState, len(a.overlimitRules))
	}
	for i, r := range a.overlimitRules {
		rule := config.AsMap(r)
		st := &a.overlimitState[i]
		enabled := rule != nil && config.Bool(orTrue(rule["enabled"]), true)
		cond := config.AsMap(rule["condition"])
		if changeOperators[config.Str(cond["operator"], "")] {
			// Momentary: each change re-arms the full boost, then it decays.
			if enabled && changeFires(cond, ch) {
				st.triggered = now
			}
			st.active = false
			continue
		}
		on := enabled && evalCondition(cond, a.oscParamState)
		if on && !st.active {
			st.triggered = now
		}
		st.active = on
	}
}

// fullBoost is a rule's boost at full strength for one channel:
// boost_unit "value" (default) = limit_boost points, "percent" =
// limit_boost % of that channel's base strength limit.
func fullBoost(rule config.M, base int) int {
	if config.Str(rule["boost_unit"], "value") == "percent" {
		pct := config.Clamp(config.Float(rule["limit_boost"], 0), 0, 1000)
		return config.ClampInt(int(math.Round(float64(base)*pct/100)), 0, 200)
	}
	return config.ClampInt(config.Int(rule["limit_boost"], 0), 0, 200)
}

// overlimitBoostsLocked returns the current (decayed) boost per channel
// for the given base limits. Must be called with overlimitMu held.
func (a *App) overlimitBoostsLocked(now time.Time, l device.Limits) map[string]int {
	out := map[string]int{"A": 0, "B": 0}
	base := map[string]int{"A": l.LimitA, "B": l.LimitB}
	for i, r := range a.overlimitRules {
		rule := config.AsMap(r)
		if rule == nil || i >= len(a.overlimitState) || !config.Bool(orTrue(rule["enabled"]), true) {
			continue
		}
		st := a.overlimitState[i]
		if st.triggered.IsZero() {
			continue
		}
		decay := config.Float(rule["decay_seconds"], defaultOverlimitDecay)
		if decay <= 0 && changeOperators[config.Str(config.Get(rule, "condition", "operator"), "")] {
			decay = defaultOverlimitDecay // a change has no duration to hold for
		}
		left := 0.0
		if decay <= 0 {
			if st.active {
				left = 1
			}
		} else {
			left = decayRemaining(rule["decay_curve"], now.Sub(st.triggered).Seconds()/decay)
		}
		ch := config.Str(rule["channel"], "both")
		for _, c := range []string{"A", "B"} {
			if ch != "both" && ch != strings.ToLower(c) {
				continue
			}
			if v := int(math.Ceil(float64(fullBoost(rule, base[c]))*left - 1e-9)); v > out[c] {
				out[c] = v
			}
		}
	}
	return out
}

// DecayCurves are the preset fall-back shapes: remaining boost fraction
// for elapsed fraction t in [0,1].
var DecayCurves = map[string]func(t float64) float64{
	"linear":     func(t float64) float64 { return 1 - t },
	"ease_out":   func(t float64) float64 { return (1 - t) * (1 - t) }, // drops fast, then slowly
	"ease_in":    func(t float64) float64 { return 1 - t*t },           // stays high, then drops
	"smoothstep": func(t float64) float64 { return 1 - t*t*(3-2*t) },   // gentle start and end
	"hold":       func(t float64) float64 { return 1 },                 // full until the end
	"exp":        func(t float64) float64 { return (math.Exp(-4*t) - math.Exp(-4)) / (1 - math.Exp(-4)) },
}

// decayRemaining returns the remaining boost fraction (0..1) after elapsed
// fraction t of the decay time. curve is a preset name or a list of
// {x, y} points (x = elapsed fraction, y = remaining fraction, linearly
// interpolated). Whatever the curve, the boost is 1 at t<=0 and 0 at t>=1
// so it always returns to the normal limit.
func decayRemaining(curve any, t float64) float64 {
	if t <= 0 {
		return 1
	}
	if t >= 1 || math.IsNaN(t) {
		return 0
	}
	var v float64
	if pts := decayPoints(curve); len(pts) > 0 {
		v = interpolatePoints(pts, t)
	} else if f := DecayCurves[config.Str(curve, "linear")]; f != nil {
		v = f(t)
	} else {
		v = 1 - t
	}
	return config.Clamp(v, 0, 1)
}

type curvePoint struct{ x, y float64 }

// decayPoints parses custom points, sorted by x with endpoints (0,1) and
// (1,0) added when missing. nil if curve is not a point list.
func decayPoints(curve any) []curvePoint {
	list := config.AsList(curve)
	if len(list) == 0 {
		return nil
	}
	pts := make([]curvePoint, 0, len(list)+2)
	for _, p := range list {
		m := config.AsMap(p)
		x, ok1 := config.ToFloat(m["x"])
		y, ok2 := config.ToFloat(m["y"])
		if m == nil || !ok1 || !ok2 {
			continue
		}
		pts = append(pts, curvePoint{config.Clamp(x, 0, 1), config.Clamp(y, 0, 1)})
	}
	if len(pts) == 0 {
		return nil
	}
	sort.SliceStable(pts, func(i, j int) bool { return pts[i].x < pts[j].x })
	if pts[0].x > 0 {
		pts = append([]curvePoint{{0, 1}}, pts...)
	}
	if pts[len(pts)-1].x < 1 {
		pts = append(pts, curvePoint{1, 0})
	}
	return pts
}

func interpolatePoints(pts []curvePoint, t float64) float64 {
	for i := 0; i+1 < len(pts); i++ {
		a, b := pts[i], pts[i+1]
		if t >= a.x && t <= b.x {
			if b.x-a.x <= 0 {
				return b.y
			}
			return a.y + (t-a.x)/(b.x-a.x)*(b.y-a.y)
		}
	}
	return pts[len(pts)-1].y
}

// overlimitLoop applies the decaying boosts (every 100 ms; caps are only
// pushed to devices when the integer value changes).
func (a *App) overlimitLoop(ctx context.Context) {
	for sleepCtx(ctx, 100*time.Millisecond) {
		a.overlimitTick(time.Now())
	}
}

func (a *App) overlimitTick(now time.Time) {
	l := a.limitsFromSettings() // outside overlimitMu: reads the config store
	a.overlimitMu.Lock()
	b := a.overlimitBoostsLocked(now, l)
	changed := b["A"] != a.overlimitEffective["A"] || b["B"] != a.overlimitEffective["B"]
	if changed {
		logx.Debug("[overlimit] boost A +%d B +%d", b["A"], b["B"])
		a.overlimitEffective["A"], a.overlimitEffective["B"] = b["A"], b["B"]
	}
	a.overlimitMu.Unlock()
	if changed {
		a.Hub.SetLimitBoost(b["A"], b["B"])
	}
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
	var ch *paramChange
	if len(args) > 0 {
		ch = &paramChange{addr: addr, prev: a.oscParamState[addr], cur: args[0]}
		a.oscParamState[addr] = args[0]
		a.oscParamSeen[addr] = time.Now()
	}
	if len(a.overlimitRules) > 0 {
		a.updateOverlimitTriggersLocked(time.Now(), ch)
	}
}

func (a *App) apiOverlimitGet(*http.Request) (any, error) {
	a.overlimitMu.Lock()
	defer a.overlimitMu.Unlock()
	// Copies: the response is encoded after the lock is released, while
	// overlimitTick keeps writing overlimitEffective (a concurrent map
	// read/write is a fatal, unrecoverable runtime error).
	return map[string]any{"rules": config.DeepCopy(a.overlimitRules),
		"effective": map[string]int{"A": a.overlimitEffective["A"], "B": a.overlimitEffective["B"]}}, nil
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
	a.migrateOverlimitRules(rules)
	a.overlimitRules = rules
	if err := a.saveOverlimitRulesLocked(); err != nil {
		return nil, err
	}
	// Edited rules start fresh: a condition that is already true counts
	// as a new trigger.
	a.overlimitState = nil
	a.updateOverlimitTriggersLocked(time.Now(), nil)
	logx.Info("[config] Overlimit rules saved: %d rules", len(rules))
	return map[string]any{"success": true, "rules": config.DeepCopy(rules)}, nil
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
	if idx < len(a.overlimitState) {
		a.overlimitState = append(a.overlimitState[:idx:idx], a.overlimitState[idx+1:]...)
	}
	if err := a.saveOverlimitRulesLocked(); err != nil {
		return nil, err
	}
	name := any(idx)
	if removed != nil && removed["name"] != nil {
		name = removed["name"]
	}
	logx.Info("[config] Overlimit rule deleted: %v", name)
	return map[string]any{"success": true, "rules": config.DeepCopy(a.overlimitRules)}, nil
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

// ---------------------------------------------------------------- used params

// apiParamsUsed lists every OSC parameter the configuration listens to
// (parameter mapping per channel and overlimit rule conditions), with its
// last received value and when it arrived.
func (a *App) apiParamsUsed(*http.Request) (any, error) {
	type source = map[string]any
	byPath := map[string][]source{}
	order := []string{}
	add := func(path string, s source) {
		if path == "" {
			return
		}
		if _, ok := byPath[path]; !ok {
			order = append(order, path)
		}
		byPath[path] = append(byPath[path], s)
	}
	for _, ch := range config.Channels {
		for _, p := range config.AsList(a.Cfg.Get("dglab3", ch, "avatar_params")) {
			m := config.AsMap(p)
			add(config.Str(m["path"], ""), source{"kind": "param", "channel": strings.ToUpper(strings.TrimPrefix(ch, "channel_")),
				"mode": config.Str(m["mode"], "distance"), "enabled": config.Bool(m["enabled"], true)})
		}
	}
	a.overlimitMu.Lock()
	defer a.overlimitMu.Unlock()
	for _, r := range a.overlimitRules {
		rule := config.AsMap(r)
		cond := config.AsMap(rule["condition"])
		add(config.Str(cond["param"], ""), source{"kind": "overlimit", "rule": config.Str(rule["name"], ""),
			"channel": strings.ToUpper(config.Str(rule["channel"], "both")), "operator": config.Str(cond["operator"], "=="),
			"value": cond["value"], "enabled": config.Bool(orTrue(rule["enabled"]), true)})
	}
	out := make([]any, 0, len(order))
	now := time.Now()
	for _, path := range order {
		e := map[string]any{"path": path, "sources": byPath[path], "wildcard": strings.Contains(path, "*")}
		if v, ok := a.oscParamState[path]; ok {
			e["last_value"] = v
			e["last_seen_ago"] = now.Sub(a.oscParamSeen[path]).Seconds()
		}
		out = append(out, e)
	}
	return map[string]any{"params": out}, nil
}
