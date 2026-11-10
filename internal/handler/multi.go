package handler

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/queue"
	"shockingvrc/internal/wave"
)

// RangesMode is the avatar_params mode of a parameter whose value ranges
// drive different modes (see Multi).
const RangesMode = "ranges"

// Range is one value range of a "ranges" parameter.
type Range struct {
	From, To float64
	Mode     string
}

// MultiConfig is the per-parameter configuration of a ranges parameter.
type MultiConfig struct {
	Ranges []Range
	// Crossfade is how long (s) the fader takes to move fully from one
	// range's mode to the next; 0 = instant.
	Crossfade float64
	// Blend is the width (in value units) of the zone around each range
	// edge where both neighbours play, so slow movements blend smoothly.
	Blend float64
	// Curve: "equal_power" (constant loudness, like a DJ crossfader) or
	// "linear".
	Curve string
	// Remap: each mode sees the value rescaled to 0..1 within its range.
	Remap bool
}

// MixModes are the modes a range may use.
var MixModes = []string{"distance", "shock", "touch", "combo", "boost"}

// ParseMultiConfig reads the ranges settings of an avatar_params entry.
func ParseMultiConfig(pm config.M) (MultiConfig, error) {
	c := MultiConfig{
		Crossfade: config.Clamp(config.Float(pm["crossfade"], 0.5), 0, 10),
		Blend:     config.Clamp(config.Float(pm["blend"], 0.05), 0, 0.5),
		Curve:     config.Str(pm["crossfade_curve"], "equal_power"),
		Remap:     config.Bool(orTrueAny(pm["remap"]), true),
	}
	if c.Curve != "linear" {
		c.Curve = "equal_power"
	}
	valid := map[string]bool{}
	for _, m := range MixModes {
		valid[m] = true
	}
	for i, r := range config.AsList(pm["ranges"]) {
		m := config.AsMap(r)
		rg := Range{
			From: config.Clamp(config.Float(m["from"], 0), 0, 1),
			To:   config.Clamp(config.Float(m["to"], 1), 0, 1),
			Mode: config.Str(m["mode"], ""),
		}
		if !valid[rg.Mode] {
			return c, fmt.Errorf("range %d: unsupported mode %q", i+1, rg.Mode)
		}
		if rg.To <= rg.From {
			return c, fmt.Errorf("range %d: 'to' must be greater than 'from'", i+1)
		}
		c.Ranges = append(c.Ranges, rg)
	}
	if len(c.Ranges) == 0 {
		return c, fmt.Errorf("no ranges")
	}
	sort.SliceStable(c.Ranges, func(i, j int) bool { return c.Ranges[i].From < c.Ranges[j].From })
	return c, nil
}

func orTrueAny(v any) any {
	if v == nil {
		return true
	}
	return v
}

// Multi plays one parameter through several modes chosen by its value
// range, mixing their waves: when the value moves into another range the
// modes crossfade (equal-power by default) instead of switching abruptly.
// Each range has its own sub-handler; the mixer renders all audible ones
// every 100 ms, sums intensities by fader gain and weights the frequency by
// each mode's contribution.
type Multi struct {
	deps    *Deps
	Channel string
	Path    string
	cfg     MultiConfig
	subs    []*Shock

	mu         sync.Mutex
	target     []float64 // fader target per range (from the current value)
	gain       []float64 // current fader position per range (slewed)
	lastFed    []float64 // last value fed to each sub
	fed        []bool
	lastTick   float64
	aheadUntil float64
	outputting bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// subSink receives the sub-handlers' output: waves are mixed by Multi
// (subs have no feeder), their timeout clears must not cut the other
// ranges, boosts go straight to the devices.
type subSink struct{ m *Multi }

func (s subSink) BroadcastWave(string, string) {}
func (s subSink) BroadcastClear(string)        {}
func (s subSink) BroadcastBoost(ch string, on bool, v int) {
	s.m.deps.Hub.BroadcastBoost(ch, on, v)
}

// NewMulti creates the mixer for one ranges parameter.
func NewMulti(deps *Deps, channel, path string, cfg MultiConfig) (*Multi, error) {
	m := &Multi{deps: deps, Channel: channel, Path: path, cfg: cfg}
	sd := *deps
	sd.Out = subSink{m}
	sd.Queue = nil // the mixer already went through the queue
	sd.ActivityObserver = nil
	for _, r := range cfg.Ranges {
		h, err := New(&sd, channel, r.Mode)
		if err != nil {
			return nil, err
		}
		h.external = true
		m.subs = append(m.subs, h)
	}
	n := len(cfg.Ranges)
	m.target, m.gain = make([]float64, n), make([]float64, n)
	m.lastFed, m.fed = make([]float64, n), make([]bool, n)
	return m, nil
}

// Mode is the avatar_params mode name (for logs / engine bookkeeping).
func (m *Multi) Mode() string { return RangesMode }

// membership is how much range r plays at value v: 1 inside, ramping over
// the blend zone around each edge.
func (m *Multi) membership(r Range, v float64) float64 {
	b := m.cfg.Blend / 2
	if b <= 0 {
		if v >= r.From && (v < r.To || (r.To >= 1 && v <= r.To)) {
			return 1
		}
		return 0
	}
	lo := config.Clamp((v-(r.From-b))/(2*b), 0, 1)
	hi := config.Clamp(((r.To+b)-v)/(2*b), 0, 1)
	if r.From <= 0 {
		lo = 1 // nothing below 0: no fade-in edge
	}
	if r.To >= 1 {
		hi = 1
	}
	return math.Min(lo, hi)
}

func (m *Multi) remap(r Range, v float64) float64 {
	if !m.cfg.Remap {
		return v
	}
	return clampUnit((v - r.From) / (r.To - r.From))
}

// targetsLocked returns the fader targets for v. Where neighbouring blend
// zones overlap, targets are normalised so the mix stays at full level.
func (m *Multi) targetsLocked(v float64) []float64 {
	t := make([]float64, len(m.cfg.Ranges))
	sum := 0.0
	for i, r := range m.cfg.Ranges {
		t[i] = m.membership(r, v)
		sum += t[i]
	}
	if sum > 1 {
		for i := range t {
			t[i] /= sum
		}
	}
	return t
}

// OSC is the dispatcher callback.
func (m *Multi) OSC(addr string, args []any) {
	val, err := Sanitize(args)
	if err != nil {
		logx.Warn("[mix] Channel %s %s: %v (args=%v)", m.Channel, addr, err, args)
		return
	}
	if obs := m.deps.ActivityObserver; obs != nil {
		obs(addr, val, m.Channel, RangesMode)
	}
	c := &Context{Address: addr, RawArgs: args, Value: val}
	if m.deps.Queue != nil {
		m.deps.Queue.Put(&queue.Command{
			Priority: queue.OSCInteraction, Channel: m.Channel, Action: "osc_trigger",
			Fn: func() { m.Handle(val, c) }, SourceID: m.Channel + "_ranges_" + addr,
		})
		return
	}
	m.Handle(val, c)
}

// Handle routes value v to the ranges that play at it.
func (m *Multi) Handle(v float64, c *Context) {
	if c == nil {
		c = &Context{Value: v, Address: m.Path}
	}
	m.mu.Lock()
	m.target = m.targetsLocked(v)
	type feed struct {
		h *Shock
		v float64
	}
	var feeds []feed
	for i, r := range m.cfg.Ranges {
		in := m.target[i] > 0
		if r.Mode == "boost" {
			// Boost has no wave to fade: on inside its range, off outside.
			bv := 0.0
			if in {
				bv = m.remap(r, v)
			}
			feeds = append(feeds, feed{m.subs[i], bv})
			continue
		}
		if in {
			rv := m.remap(r, v)
			m.lastFed[i], m.fed[i] = rv, true
			feeds = append(feeds, feed{m.subs[i], rv})
		}
	}
	m.mu.Unlock()
	for _, f := range feeds {
		f.h.Handle(f.v, &Context{Address: c.Address, RawArgs: c.RawArgs, Value: f.v})
	}
}

// curveGain maps a fader position to an amplitude.
func (m *Multi) curveGain(g float64) float64 {
	if m.cfg.Curve == "linear" {
		return g
	}
	return math.Sqrt(clampUnit(g)) // equal power: sum of squares stays 1
}

type sample struct{ f, s int }

func decodeSamples(w string, n int) []sample {
	out := make([]sample, n*4)
	ops, err := wave.DecodeOps(w)
	if err != nil {
		return out
	}
	for i := 0; i < n && i < len(ops); i++ {
		for j := 0; j < 4; j++ {
			out[i*4+j] = sample{wave.HexByte(ops[i], j*2), wave.HexByte(ops[i], 8+j*2)}
		}
	}
	return out
}

// tick advances the faders and sends the next mixed ops (paced like the
// standalone feeders: ~200 ms buffered in the APP).
func (m *Multi) tick() {
	now := nowSec()
	m.mu.Lock()
	dt := now - m.lastTick
	if m.lastTick == 0 || dt < 0 || dt > 1 {
		dt = 0.1
	}
	m.lastTick = now
	start := append([]float64(nil), m.gain...)
	rate := math.Inf(1)
	if m.cfg.Crossfade > 0 {
		rate = 1 / m.cfg.Crossfade
	}
	var refeed []int
	for i := range m.gain {
		d := m.target[i] - m.gain[i]
		step := rate * dt
		if math.Abs(d) <= step {
			m.gain[i] = m.target[i]
		} else {
			m.gain[i] += math.Copysign(step, d)
		}
		// A range that is fading out keeps its last level (re-fed so its
		// timeout does not cut it before the fade ends).
		if m.target[i] == 0 && m.gain[i] > 0 && m.fed[i] && m.cfg.Ranges[i].Mode != "boost" {
			refeed = append(refeed, i)
		}
	}
	end := append([]float64(nil), m.gain...)
	if m.cfg.Crossfade <= 0 {
		start = end // instant switch: no ramp inside the window either
	}
	buffered := math.Max(0, m.aheadUntil-now)
	n := config.ClampInt(int(math.Floor((outputLead-buffered)/opSeconds+0.5)), 0, maxOpsSend)
	lastFed := append([]float64(nil), m.lastFed...)
	m.mu.Unlock()
	for _, i := range refeed {
		m.subs[i].Handle(lastFed[i], &Context{Address: m.Path, Value: lastFed[i]})
	}
	if n == 0 {
		return
	}

	type layer struct {
		idx     int
		samples []sample
	}
	var layers []layer
	for i, h := range m.subs {
		if m.cfg.Ranges[i].Mode == "boost" || (start[i] <= 0 && end[i] <= 0) {
			continue
		}
		if w := h.Render(n); w != "" {
			layers = append(layers, layer{i, decodeSamples(w, n)})
		}
	}
	if len(layers) == 0 {
		m.mu.Lock()
		was := m.outputting
		m.outputting = false
		m.mu.Unlock()
		if was {
			m.deps.Hub.BroadcastClear(m.Channel)
		}
		return
	}
	total := n * 4
	ops := make([]string, n)
	for k := 0; k < n; k++ {
		var fb, sb strings.Builder
		for j := 0; j < 4; j++ {
			idx := k*4 + j
			frac := float64(idx+1) / float64(total)
			sum, fw, wsum := 0.0, 0.0, 0.0
			bestF, bestA := 0, -1.0
			for _, l := range layers {
				g := start[l.idx] + (end[l.idx]-start[l.idx])*frac
				a := m.curveGain(g)
				sm := l.samples[idx]
				c := a * float64(sm.s)
				sum += c
				fw += c * float64(sm.f)
				wsum += c
				if a > bestA && sm.f > 0 {
					bestF, bestA = sm.f, a
				}
			}
			f := bestF
			if wsum > 0 {
				f = int(math.Round(fw / wsum))
			}
			if f <= 0 {
				f = 10
			}
			fmt.Fprintf(&fb, "%02X", config.ClampInt(f, 0, 255))
			fmt.Fprintf(&sb, "%02X", config.ClampInt(int(math.Round(sum)), 0, 100))
		}
		ops[k] = fb.String() + sb.String()
	}
	w := wave.EncodeOps(ops)
	m.mu.Lock()
	m.aheadUntil = math.Max(m.aheadUntil, now) + float64(n)*opSeconds
	m.outputting = true
	m.mu.Unlock()
	logx.Debug("[mix] channel=%s param=%s gains=%s %s", m.Channel, m.Path, fmtGains(end), summarizeWave(w))
	m.deps.Hub.BroadcastWave(m.Channel, w)
}

func fmtGains(g []float64) string {
	parts := make([]string, len(g))
	for i, v := range g {
		parts[i] = fmt.Sprintf("%.2f", v)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// Gains returns the current fader positions (for tests / status).
func (m *Multi) Gains() []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]float64(nil), m.gain...)
}

// Start launches the sub-handlers' timers and the mixer.
func (m *Multi) Start() {
	for _, h := range m.subs {
		h.Start()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				func() {
					defer logx.Recover("mixer " + m.Path)
					m.tick()
				}()
			}
		}
	}()
	modes := make([]string, len(m.cfg.Ranges))
	for i, r := range m.cfg.Ranges {
		modes[i] = fmt.Sprintf("%.2f-%.2f:%s", r.From, r.To, r.Mode)
	}
	logx.Info("[mix] Channel %s %s ranges %s (crossfade %.2fs, %s)", m.Channel, m.Path, strings.Join(modes, " "), m.cfg.Crossfade, m.cfg.Curve)
}

// Stop stops the mixer and its sub-handlers and clears the channel.
func (m *Multi) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	for _, h := range m.subs {
		h.Stop()
	}
	m.mu.Lock()
	was := m.outputting
	m.outputting = false
	m.mu.Unlock()
	if was {
		m.deps.Hub.BroadcastClear(m.Channel)
	}
}
