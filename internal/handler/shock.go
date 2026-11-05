// Package handler ports ShockHandler: it turns sanitized OSC values into
// DG-LAB wave output for one channel in one mode
// (distance / shock / touch / combo / boost).
package handler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"shockingvrc/internal/config"
	"shockingvrc/internal/device"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/queue"
	"shockingvrc/internal/sps"
	"shockingvrc/internal/wave"
)

// Point is a curve control point.
type Point struct {
	X float64 `json:"x" yaml:"x"`
	Y float64 `json:"y" yaml:"y"`
}

// Deps are the shared collaborators of all handlers.
type Deps struct {
	Cfg   *config.Store
	Hub   *device.Hub
	Lib   *wave.Library
	Queue *queue.Queue
	// ActivityObserver is called for every accepted OSC value.
	ActivityObserver func(addr string, value float64, channel, mode string)
	// CurveGetter returns curve points for a param path or "A"/"B".
	CurveGetter func(key string) []Point
}

// Context describes the OSC message that triggered a handler call.
type Context struct {
	Address string
	RawArgs []any
	Value   float64
	SPS     string
}

// Modes lists supported handler modes.
var Modes = []string{"distance", "shock", "touch", "combo", "boost"}

func nowSec() float64 { return float64(time.Now().UnixNano()) / 1e9 }

type tdPoint struct{ t, d float64 }

// Shock is one channel+mode handler.
type Shock struct {
	deps    *Deps
	Channel string // "A" / "B"
	Mode    string
	handle  func(v float64, c *Context)

	mu                  sync.Mutex
	bgWindow            float64
	bgCurrentStrength   float64
	touchDist           []tdPoint
	activeDynamicPreset string
	waveHeads           map[string]float64
	activeModePresets   map[string]string
	shockStartedAt      float64
	shockLastStrength   float64
	toClearTime         float64
	isCleared           bool
	comboTouchStart     float64
	comboInTouchMode    bool
	comboShockFired     bool
	boostActive         bool
	warnMu              sync.Mutex
	warnLast            map[string]float64
	spsCalcs            map[[2]string]*sps.Calculator

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// ErrBadMode is returned for unsupported modes.
var ErrBadMode = errors.New("not supported mode")

// New creates a handler.
func New(deps *Deps, channel, mode string) (*Shock, error) {
	h := &Shock{
		deps:              deps,
		Channel:           channel,
		Mode:              mode,
		bgWindow:          0.1,
		waveHeads:         map[string]float64{"distance": 0, "touch": 0, "shock": 0},
		activeModePresets: map[string]string{},
		isCleared:         true,
		warnLast:          map[string]float64{},
		spsCalcs:          map[[2]string]*sps.Calculator{},
	}
	switch mode {
	case "distance":
		h.handle = h.handlerDistance
	case "shock":
		h.handle = h.handlerShock
	case "touch":
		h.handle = h.handlerTouch
	case "combo":
		h.handle = h.handlerCombo
	case "boost":
		h.handle = h.handlerBoost
	default:
		return nil, fmt.Errorf("%w: %s", ErrBadMode, mode)
	}
	return h, nil
}

// ---------------------------------------------------------------- config

func (h *Shock) modeConfig() config.M {
	return config.AsMap(h.deps.Cfg.Get("dglab3", config.ChannelKey(h.Channel), "mode_config"))
}

func (h *Shock) modeConf(mc config.M, mode string) config.M {
	if m := config.AsMap(mc[mode]); m != nil {
		return m
	}
	return config.M{}
}

func (h *Shock) spsEnabled() bool {
	return config.Bool(h.deps.Cfg.Get("sps", "enabled"), true)
}

func (h *Shock) warnThrottled(key, msg string) {
	now := nowSec()
	h.warnMu.Lock()
	if now-h.warnLast[key] < 5.0 {
		h.warnMu.Unlock()
		return
	}
	h.warnLast[key] = now
	h.warnMu.Unlock()
	logx.Warn("%s", msg)
}

func clampUnit(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Min(1, math.Max(0, v))
}

func (h *Shock) triggerBottom(mc config.M) float64 {
	return config.Float(config.Get(mc, "trigger_range", "bottom"), 0.0)
}

func (h *Shock) normalizeDistance(mc config.M, distance float64) float64 {
	tr := config.AsMap(mc["trigger_range"])
	bottom, top := 0.0, 1.0
	ok1, ok2 := true, true
	if v, has := tr["bottom"]; has {
		bottom, ok1 = config.ToFloat(v)
	}
	if v, has := tr["top"]; has {
		top, ok2 = config.ToFloat(v)
	}
	if !ok1 || !ok2 {
		logx.Warn("[handler] Channel %s invalid trigger_range: %v", h.Channel, tr)
		return 0
	}
	if top <= bottom {
		h.warnThrottled("trigger_range", fmt.Sprintf(
			"[handler] Channel %s invalid trigger_range: bottom=%v, top=%v; output forced to 0", h.Channel, bottom, top))
		return 0
	}
	if distance <= bottom {
		return 0
	}
	return clampUnit((distance - bottom) / (top - bottom))
}

// InterpolateCurve linearly interpolates value through points.
func InterpolateCurve(value float64, pts []Point) float64 {
	if len(pts) == 0 {
		return value
	}
	if value <= 0 {
		return 0
	}
	if value >= 1 {
		return pts[len(pts)-1].Y
	}
	for i := 0; i < len(pts)-1; i++ {
		p, n := pts[i], pts[i+1]
		if p.X <= value && value <= n.X {
			dx := n.X - p.X
			if dx <= 0 {
				return p.Y
			}
			return p.Y + (value-p.X)/dx*(n.Y-p.Y)
		}
	}
	return pts[len(pts)-1].Y
}

func (h *Shock) applyStrengthCurve(value float64, paramPath string) float64 {
	value = clampUnit(value)
	g := h.deps.CurveGetter
	if g == nil {
		return value
	}
	key := paramPath
	if key == "" {
		key = h.Channel
	}
	pts := g(key)
	if len(pts) == 0 {
		return value
	}
	return clampUnit(InterpolateCurve(value, pts))
}

func (h *Shock) normalizeBetween(v, bottom, top float64) float64 {
	if top <= bottom {
		h.warnThrottled(fmt.Sprintf("normalize_between_%v_%v", bottom, top), fmt.Sprintf(
			"[handler] Channel %s invalid normalize range: bottom=%v, top=%v; output forced to 0", h.Channel, bottom, top))
		return 0
	}
	c := math.Min(math.Max(v, bottom), top)
	return (c - bottom) / (top - bottom)
}

var defaultDerivativeParams = []config.M{
	{"bottom": 0.0, "top": 1.0},
	{"bottom": 0.0, "top": 4.0},
	{"bottom": 0.0, "top": 40.0},
	{"bottom": 0.0, "top": 400.0},
}

func (h *Shock) derivativeParams(mc config.M, n int) (bottom, top float64) {
	list := config.AsList(h.modeConf(mc, "touch")["derivative_params"])
	var entry config.M
	if len(list) == 0 {
		h.warnThrottled("derivative_params_missing", fmt.Sprintf(
			"[handler] Channel %s touch.derivative_params missing, using defaults", h.Channel))
		entry = defaultDerivativeParams[config.ClampInt(n, 0, 3)]
	} else {
		entry = config.AsMap(list[config.ClampInt(n, 0, len(list)-1)])
		if entry == nil {
			entry = defaultDerivativeParams[config.ClampInt(n, 0, 3)]
		}
	}
	return config.Float(entry["bottom"], 0.0), config.Float(entry["top"], 1.0)
}

func (h *Shock) touchNDerivative(mc config.M) int {
	n, ok := config.ToInt(config.Get(mc, "touch", "n_derivative"))
	if config.Get(mc, "touch", "n_derivative") == nil {
		n, ok = 1, true
	}
	if !ok {
		n = 1
	}
	if n < 0 || n > 3 {
		h.warnThrottled("n_derivative_invalid", fmt.Sprintf(
			"[handler] Channel %s invalid n_derivative %d, fallback to 1", h.Channel, n))
		n = 1
	}
	return n
}

func (h *Shock) freqMs(mc config.M, mode string) int {
	raw := h.modeConf(mc, mode)["freq_ms"]
	if raw == nil {
		return 10
	}
	f, ok := config.ToInt(raw)
	if !ok {
		h.warnThrottled("freq_ms_"+mode, fmt.Sprintf(
			"[handler] Channel %s invalid %s.freq_ms=%v, using 10", h.Channel, mode, raw))
		f = 10
	}
	return config.ClampInt(f, 0, 255)
}

func (h *Shock) shockDuration(mc config.M) float64 {
	return math.Max(0.1, config.Float(h.modeConf(mc, "shock")["duration"], 2))
}

func (h *Shock) windowSize(mc config.M, mode string) int {
	return config.ClampInt(config.Int(h.modeConf(mc, mode)["wave_window_ops"], 4), 1, 32)
}

func (h *Shock) sampleStep(mc config.M, mode string) float64 {
	return config.Clamp(config.Float(h.modeConf(mc, mode)["wave_sample_step"], 1.0), 0.25, 8.0)
}

func (h *Shock) windowAdvance(mc config.M, mode string) float64 {
	def := 4.0 * h.sampleStep(mc, mode)
	return config.Clamp(config.Float(h.modeConf(mc, mode)["wave_advance_samples"], def), 0.25, 32.0)
}

func (h *Shock) textureFloor(mc config.M, mode string) float64 {
	return config.Clamp(config.Float(h.modeConf(mc, mode)["texture_floor"], 0.0), 0, 1)
}

func presetOf(m config.M) string { return config.Str(m["wave_preset"], "") }

func scaleOf(m config.M) float64 { return config.Float(m["wave_scale"], 1.0) }

// buildDynamicWave must be called with h.mu held.
func (h *Shock) buildDynamicWave(mc config.M, mode string, from, to float64, preset string, scale float64) string {
	scale = config.Clamp(scale, 0, 1)
	lib := h.deps.Lib
	if preset != "" && (mode == "distance" || mode == "touch") {
		if preset != h.activeModePresets[mode] {
			h.waveHeads[mode] = 0
			h.activeModePresets[mode] = preset
		}
		start := h.waveHeads[mode]
		w := lib.BuildResampledWindow(preset, start, h.windowSize(mc, mode), scale,
			h.textureFloor(mc, mode), h.sampleStep(mc, mode))
		if w != "" {
			h.waveHeads[mode] = start + h.windowAdvance(mc, mode)
			h.activeDynamicPreset = preset
			return w
		}
	}
	if preset != "" && mode == "shock" {
		h.activeModePresets[mode] = preset
		h.activeDynamicPreset = preset
		w := lib.BuildScaledWindow(preset, int(h.waveHeads[mode]), h.windowSize(mc, mode), to*scale)
		if w != "" {
			h.waveHeads[mode]++
			return w
		}
	}
	delete(h.activeModePresets, mode)
	h.activeDynamicPreset = ""
	if mode == "shock" {
		fallback := config.Str(h.modeConf(mc, "shock")["wave"], "")
		if fallback == "" {
			fallback = wave.DefaultWave
		}
		return wave.ScaleWavestr(fallback, clampUnit(to*scale))
	}
	w, _ := wave.Generate100ms(h.freqMs(mc, mode), clampUnit(from), clampUnit(to))
	return w
}

func (h *Shock) resolveTouchProfile(mc config.M, strength float64) (string, float64) {
	tc := h.modeConf(mc, "touch")
	var sel config.M
	selTh := 0.0
	for _, b := range config.AsList(tc["preset_bands"]) {
		band := config.AsMap(b)
		if band == nil {
			continue
		}
		th, ok := config.ToFloat(band["threshold"])
		if band["threshold"] == nil {
			th, ok = 0, true
		}
		if !ok {
			logx.Warn("[handler] Channel %s invalid touch preset threshold: %v", h.Channel, band)
			continue
		}
		if strength >= th && (sel == nil || th >= selTh) {
			sel, selTh = band, th
		}
	}
	if sel == nil {
		return presetOf(tc), scaleOf(tc)
	}
	p := presetOf(sel)
	if p == "" {
		p = presetOf(tc)
	}
	s := scaleOf(tc)
	if v, ok := sel["wave_scale"]; ok && v != nil {
		s = config.Float(v, s)
	}
	return p, s
}

// ---------------------------------------------------------------- input

// Sanitize mirrors BaseHandler.param_sanitizer: first arg, bool->0/1,
// float clamped to [0,1] (non-finite -> 0), int clamped to [0,1].
func Sanitize(args []any) (float64, error) {
	if len(args) == 0 {
		return 0, errors.New("错误的输入参数类型，仅支持 1~0 之间的 float、int 或 bool。")
	}
	switch v := args[0].(type) {
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			logx.Warn("[handler] Non-finite OSC param %v, treated as 0", v)
			return 0, nil
		}
		return math.Min(math.Max(v, 0), 1), nil
	case float32:
		return Sanitize([]any{float64(v)})
	case int:
		return float64(config.ClampInt(v, 0, 1)), nil
	case int32:
		return float64(config.ClampInt(int(v), 0, 1)), nil
	case int64:
		if v > 1 {
			return 1, nil
		}
		if v < 0 {
			return 0, nil
		}
		return float64(v), nil
	}
	return 0, errors.New("错误的输入参数类型，仅支持 1~0 之间的 float、int 或 bool。")
}

// OSC is the dispatcher callback.
func (h *Shock) OSC(addr string, args []any) {
	val, err := Sanitize(args)
	if err != nil {
		logx.Warn("[handler] Channel %s %s: %v (args=%v)", h.Channel, addr, err, args)
		return
	}
	c := &Context{Address: addr, RawArgs: args, Value: val}
	if h.spsEnabled() {
		if depth, sig, ok := h.aggregateSPS(addr, val); ok {
			val, c.Value, c.SPS = depth, depth, sig
		}
	}
	if obs := h.deps.ActivityObserver; obs != nil {
		obs(addr, val, h.Channel, h.Mode)
	}
	if h.deps.Queue != nil {
		ok := h.deps.Queue.Put(&queue.Command{
			Priority: queue.OSCInteraction,
			Channel:  h.Channel,
			Action:   "osc_trigger",
			Fn:       func() { h.handle(val, c) },
			SourceID: h.Channel + "_" + h.Mode + "_" + addr,
		})
		if !ok {
			logx.Debug("[handler] Queue deferred (cooldown, latest kept): %s %s %s", h.Channel, h.Mode, addr)
		}
		return
	}
	h.handle(val, c)
}

// Handle invokes the mode handler directly (bypassing the queue).
func (h *Shock) Handle(v float64, c *Context) {
	if c == nil {
		c = &Context{Value: v}
	}
	h.handle(v, c)
}

func (h *Shock) aggregateSPS(addr string, v float64) (float64, string, bool) {
	if !sps.IsOGBAddress(addr) {
		return 0, "", false
	}
	tt, zone, sig, ok := sps.ParseOGBAddress(addr)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !ok {
		h.warnThrottled("sps_bad_addr_"+addr, fmt.Sprintf(
			"[handler] Channel %s unrecognized OGB address: %s", h.Channel, addr))
		return 0, "", false
	}
	key := [2]string{tt, zone}
	calc := h.spsCalcs[key]
	if calc == nil {
		calc = sps.NewCalculator(tt)
		h.spsCalcs[key] = calc
		logx.Info("[handler] Channel %s tracking SPS zone %s/%s", h.Channel, tt, zone)
	}
	return calc.Update(sig, v), tt + "/" + zone + "/" + sig, true
}

func (h *Shock) logTrigger(c *Context, value float64, normalized *float64, extra string) {
	addr := "-"
	var raw any = []any{value}
	if c != nil {
		addr = c.Address
		raw = c.RawArgs
	}
	msg := fmt.Sprintf("[handler] [trigger] channel=%s mode=%s param=%s raw=%v value=%.3f", h.Channel, h.Mode, addr, raw, value)
	if normalized != nil {
		msg += fmt.Sprintf(" normalized=%.3f", *normalized)
	}
	if extra != "" {
		msg += " " + extra
	}
	logx.Debug("%s", msg)
}

func summarizeWave(w string) string {
	ops, err := wave.DecodeOps(w)
	if err != nil {
		if len(w) > 48 {
			w = w[:48]
		}
		return "wavestr=" + w
	}
	if len(ops) == 0 {
		return "ops=0"
	}
	return fmt.Sprintf("ops=%d first=%s last=%s", len(ops), ops[0], ops[len(ops)-1])
}

func (h *Shock) logOutput(source string, strength float64, w, extra string) {
	msg := fmt.Sprintf("[handler] [output] channel=%s mode=%s source=%s strength=%.3f %s", h.Channel, h.Mode, source, strength, summarizeWave(w))
	if extra != "" {
		msg += " " + extra
	}
	logx.Debug("%s", msg)
}

// setClearAfterLocked must be called with h.mu held.
func (h *Shock) setClearAfterLocked(sec float64) {
	h.isCleared = false
	h.toClearTime = nowSec() + sec
}

// ---------------------------------------------------------------- modes

func (h *Shock) handlerDistance(distance float64, c *Context) {
	mc := h.modeConfig()
	h.mu.Lock()
	h.setClearAfterLocked(0.5)
	n := h.normalizeDistance(mc, distance)
	n = h.applyStrengthCurve(n, c.Address)
	h.bgCurrentStrength = n
	h.mu.Unlock()
	h.logTrigger(c, distance, &n, "")
}

func (h *Shock) shockStrengthLocked(mc config.M, now float64) float64 {
	if h.shockStartedAt <= 0 {
		return 0
	}
	elapsed := now - h.shockStartedAt
	if elapsed <= 0 {
		return 1
	}
	return math.Max(0, 1-elapsed/h.shockDuration(mc))
}

func (h *Shock) handlerShock(distance float64, c *Context) {
	mc := h.modeConfig()
	if distance > h.triggerBottom(mc) {
		dur := h.shockDuration(mc)
		h.mu.Lock()
		h.setClearAfterLocked(dur)
		h.shockStartedAt = nowSec()
		h.shockLastStrength = 0
		h.mu.Unlock()
		p := presetOf(h.modeConf(mc, "shock"))
		if p == "" {
			p = "-"
		}
		h.logTrigger(c, distance, nil, fmt.Sprintf("duration=%.2fs preset=%s action=reset-to-peak", dur, p))
	}
}

func (h *Shock) touchLocked(mc config.M, distance float64, c *Context) {
	h.setClearAfterLocked(0.5)
	out := h.normalizeDistance(mc, distance)
	h.logTrigger(c, distance, &out, "")
	if out == 0 {
		return
	}
	h.touchDist = append(h.touchDist, tdPoint{nowSec(), out})
	if len(h.touchDist) > 20 {
		h.touchDist = h.touchDist[len(h.touchDist)-20:]
	}
}

func (h *Shock) handlerTouch(distance float64, c *Context) {
	mc := h.modeConfig()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.touchLocked(mc, distance, c)
}

func gradient(v, t []float64) []float64 {
	n := len(v)
	g := make([]float64, n)
	if n < 2 {
		return g
	}
	div := func(a, b float64) float64 {
		if b == 0 {
			return 0
		}
		return a / b
	}
	g[0] = div(v[1]-v[0], t[1]-t[0])
	for i := 1; i < n-1; i++ {
		g[i] = div(v[i+1]-v[i-1], t[i+1]-t[i-1])
	}
	g[n-1] = div(v[n-1]-v[n-2], t[n-1]-t[n-2])
	return g
}

// computeDerivativeLocked returns distance, velocity, acceleration, jerk.
func (h *Shock) computeDerivativeLocked() [4]float64 {
	data := h.touchDist
	if len(data) < 4 {
		return [4]float64{}
	}
	const w = 3
	dist := make([]float64, 0, len(data)-w+1)
	for i := 0; i+w <= len(data); i++ {
		dist = append(dist, (data[i].d+data[i+1].d+data[i+2].d)/w)
	}
	ts := make([]float64, len(dist))
	for i := range ts {
		ts[i] = data[i].t
	}
	vel := gradient(dist, ts)
	acc := gradient(vel, ts)
	jerk := gradient(acc, ts)
	last := len(dist) - 1
	return [4]float64{dist[last], vel[last], acc[last], jerk[last]}
}

// ComputeDerivative is exported for tests.
func (h *Shock) ComputeDerivative() [4]float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.computeDerivativeLocked()
}

func (h *Shock) touchStrengthLocked(mc config.M) float64 {
	n := h.touchNDerivative(mc)
	vals := h.computeDerivativeLocked()
	b, t := h.derivativeParams(mc, n)
	return h.normalizeBetween(math.Abs(vals[n]), b, t)
}

func (h *Shock) handlerCombo(distance float64, c *Context) {
	mc := h.modeConfig()
	cc := h.modeConf(mc, "combo")
	threshold := 0.3
	if v, ok := cc["switch_duration"]; ok {
		f, ok2 := config.ToFloat(v)
		if !ok2 {
			h.warnThrottled("combo_switch_duration", fmt.Sprintf(
				"[handler] Channel %s invalid combo.switch_duration=%v, using 0.3", h.Channel, v))
		} else {
			threshold = f
		}
	}
	threshold = config.Clamp(threshold, 0, 10)
	bottom := h.triggerBottom(mc)
	now := nowSec()
	h.mu.Lock()
	if distance > bottom {
		if h.comboTouchStart == 0 {
			h.comboTouchStart = now
			h.comboInTouchMode = false
			h.comboShockFired = false
		}
		dur := now - h.comboTouchStart
		if dur >= threshold && !h.comboInTouchMode {
			h.comboInTouchMode = true
			h.touchDist = nil
			logx.Debug("[handler] [combo] channel=%s switched to touch after %.2fs", h.Channel, dur)
		}
		if h.comboInTouchMode {
			h.touchLocked(mc, distance, c)
		} else {
			h.setClearAfterLocked(math.Max(threshold+0.2, h.shockDuration(mc)))
		}
		h.mu.Unlock()
		return
	}
	fire := false
	var dur float64
	if h.comboTouchStart > 0 {
		dur = now - h.comboTouchStart
		if dur < threshold && !h.comboShockFired {
			h.comboShockFired = true
			fire = true
		}
		h.comboTouchStart = 0
		h.comboInTouchMode = false
	}
	h.mu.Unlock()
	if fire {
		h.handlerShock(bottom+0.01, c)
		logx.Debug("[handler] [combo] channel=%s fired shock (duration=%.2fs)", h.Channel, dur)
	}
}

func (h *Shock) boostValue(mc config.M) int {
	raw := h.modeConf(mc, "boost")["value"]
	if raw == nil {
		return 30
	}
	v, ok := config.ToInt(raw)
	if !ok {
		h.warnThrottled("boost_value", fmt.Sprintf(
			"[handler] Channel %s invalid boost.value=%v, using 30", h.Channel, raw))
		v = 30
	}
	return config.ClampInt(v, 0, 200)
}

func (h *Shock) handlerBoost(distance float64, c *Context) {
	mc := h.modeConfig()
	bottom := h.triggerBottom(mc)
	bv := h.boostValue(mc)
	h.mu.Lock()
	var mode string
	if distance > bottom && !h.boostActive {
		h.boostActive, mode = true, "1"
	} else if distance <= bottom && h.boostActive {
		h.boostActive, mode = false, "0"
	}
	h.mu.Unlock()
	switch mode {
	case "1":
		h.deps.Hub.BroadcastBoost(h.Channel, true, bv)
		h.logTrigger(c, distance, nil, fmt.Sprintf("boost +%d", bv))
	case "0":
		h.deps.Hub.BroadcastBoost(h.Channel, false, bv)
		h.logTrigger(c, distance, nil, "boost reverted")
	}
}

// ---------------------------------------------------------------- feeders

func (h *Shock) distanceTick(last *float64) {
	mc := h.modeConfig()
	h.mu.Lock()
	cur := h.bgCurrentStrength
	if cur == 0 && *last == 0 {
		h.mu.Unlock()
		return
	}
	dc := h.modeConf(mc, "distance")
	w := h.buildDynamicWave(mc, "distance", *last, cur, presetOf(dc), scaleOf(dc))
	preset := h.activeDynamicPreset
	h.mu.Unlock()
	if preset == "" {
		preset = "-"
	}
	h.logOutput("distance", cur, w, fmt.Sprintf("from=%.3f to=%.3f preset=%s texture_floor=%.2f",
		*last, cur, preset, h.textureFloor(mc, "distance")))
	*last = cur
	h.deps.Hub.BroadcastWave(h.Channel, w)
}

// shockOutputLocked computes the next shock wave; "" when idle.
func (h *Shock) shockOutputLocked(mc config.M, now float64) (string, float64) {
	cur := h.shockStrengthLocked(mc, now)
	if cur <= 0 {
		h.shockLastStrength = 0
		return "", 0
	}
	sc := h.modeConf(mc, "shock")
	w := h.buildDynamicWave(mc, "shock", h.shockLastStrength, cur, presetOf(sc), scaleOf(sc))
	h.shockLastStrength = cur
	return w, cur
}

func (h *Shock) shockTick() {
	mc := h.modeConfig()
	h.mu.Lock()
	if h.isCleared {
		h.shockLastStrength = 0
		h.mu.Unlock()
		return
	}
	from := h.shockLastStrength
	w, cur := h.shockOutputLocked(mc, nowSec())
	h.mu.Unlock()
	if w == "" {
		return
	}
	p := presetOf(h.modeConf(mc, "shock"))
	if p == "" {
		p = "-"
	}
	h.logOutput("shock", cur, w, fmt.Sprintf("from=%.3f to=%.3f duration=%.2fs preset=%s", from, cur, h.shockDuration(mc), p))
	h.deps.Hub.BroadcastWave(h.Channel, w)
}

// touchOutputLocked computes the next touch wave; "" when idle.
func (h *Shock) touchOutputLocked(mc config.M, last *float64) (string, float64, string, float64) {
	cur := h.touchStrengthLocked(mc)
	h.bgCurrentStrength = cur
	if cur == 0 && *last == 0 {
		return "", 0, "", 0
	}
	p, s := h.resolveTouchProfile(mc, cur)
	w := h.buildDynamicWave(mc, "touch", *last, cur, p, s)
	return w, cur, p, s
}

func (h *Shock) touchTick(last *float64) {
	mc := h.modeConfig()
	h.mu.Lock()
	from := *last
	w, cur, p, s := h.touchOutputLocked(mc, last)
	h.mu.Unlock()
	if w == "" {
		return
	}
	if p == "" {
		p = "-"
	}
	names := []string{"distance", "velocity", "acceleration", "jerk"}
	h.logOutput("touch", cur, w, fmt.Sprintf("from=%.3f to=%.3f derivative=%s preset=%s scale=%.2f texture_floor=%.2f",
		from, cur, names[h.touchNDerivative(mc)], p, s, h.textureFloor(mc, "touch")))
	*last = cur
	h.deps.Hub.BroadcastWave(h.Channel, w)
}

func (h *Shock) comboTick(lastTouch *float64) {
	mc := h.modeConfig()
	h.mu.Lock()
	if h.isCleared {
		h.shockLastStrength = 0
		*lastTouch = 0
		h.mu.Unlock()
		return
	}
	var w, src string
	var cur float64
	if h.comboInTouchMode {
		w, cur, _, _ = h.touchOutputLocked(mc, lastTouch)
		if w != "" {
			*lastTouch = cur
		}
		src = "combo/touch"
	} else {
		*lastTouch = 0
		w, cur = h.shockOutputLocked(mc, nowSec())
		src = "combo/shock"
	}
	h.mu.Unlock()
	if w == "" {
		return
	}
	h.logOutput(src, cur, w, "")
	h.deps.Hub.BroadcastWave(h.Channel, w)
}

func (h *Shock) clearTick() {
	h.mu.Lock()
	if h.isCleared || nowSec() <= h.toClearTime {
		h.mu.Unlock()
		return
	}
	h.isCleared = true
	h.bgCurrentStrength = 0
	h.touchDist = nil
	h.activeDynamicPreset = ""
	for k := range h.waveHeads {
		h.waveHeads[k] = 0
	}
	h.activeModePresets = map[string]string{}
	h.shockStartedAt = 0
	h.shockLastStrength = 0
	h.comboTouchStart = 0
	h.comboInTouchMode = false
	h.comboShockFired = false
	for _, c := range h.spsCalcs {
		c.Reset()
	}
	h.mu.Unlock()
	h.deps.Hub.BroadcastClear(h.Channel)
	logx.Debug("[handler] Channel %s cleared after timeout", h.Channel)
}

// supervise runs tick every period until ctx is done, surviving panics.
func (h *Shock) supervise(ctx context.Context, name string, period time.Duration, tick func()) {
	defer h.wg.Done()
	backoff := 500 * time.Millisecond
	for {
		crashed := func() (crashed bool) {
			defer func() {
				if r := recover(); r != nil {
					logx.Error("[handler] Channel %s %s crashed, restarting in %.1fs: %v", h.Channel, name, backoff.Seconds(), r)
					crashed = true
				}
			}()
			t := time.NewTicker(period)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return false
				case <-t.C:
					tick()
				}
			}
		}()
		if !crashed {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 5*time.Second {
			backoff = 5 * time.Second
		}
	}
}

// Start launches the clear checker and the mode's feeder.
func (h *Shock) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	logx.Info("[handler] Channel %s background job started", h.Channel)
	h.wg.Add(1)
	go h.supervise(ctx, "clear_check", 50*time.Millisecond, h.clearTick)
	period := time.Duration(h.bgWindow * float64(time.Second))
	switch h.Mode {
	case "distance":
		var last float64
		h.wg.Add(1)
		go h.supervise(ctx, "distance_feeder", period, func() { h.distanceTick(&last) })
	case "shock":
		h.wg.Add(1)
		go h.supervise(ctx, "shock_feeder", period, h.shockTick)
	case "touch":
		var last float64
		h.wg.Add(1)
		go h.supervise(ctx, "touch_feeder", period, func() { h.touchTick(&last) })
	case "combo":
		var last float64
		h.wg.Add(1)
		go h.supervise(ctx, "combo_feeder", period, func() { h.comboTick(&last) })
	}
}

// Stop cancels background loops and reverts any active boost.
func (h *Shock) Stop() {
	if h.cancel != nil {
		h.cancel()
	}
	h.wg.Wait()
	h.mu.Lock()
	boosted := h.boostActive
	h.boostActive = false
	wasOutputting := !h.isCleared
	h.isCleared = true
	h.mu.Unlock()
	if wasOutputting {
		// The feeder is gone; stop whatever the APP still has buffered.
		h.deps.Hub.BroadcastClear(h.Channel)
		logx.Debug("[handler] Channel %s cleared on stop", h.Channel)
	}
	if boosted {
		h.deps.Hub.BroadcastBoost(h.Channel, false, 0)
		logx.Info("[handler] Channel %s boost reverted on shutdown", h.Channel)
	}
}
