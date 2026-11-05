package handler

import (
	"math"
	"testing"

	"shockingvrc/internal/config"
	"shockingvrc/internal/device"
	"shockingvrc/internal/wave"
)

func newTestHandler(t *testing.T, mode string) *Shock {
	t.Helper()
	cfg := config.NewStore(t.TempDir())
	cfg.Write(func(adv, basic config.M) { config.SyncBasicIntoAdvanced(adv, basic) })
	h, err := New(&Deps{Cfg: cfg, Hub: device.NewHub(), Lib: wave.NewLibrary(t.TempDir())}, "A", mode)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestSanitize(t *testing.T) {
	cases := []struct {
		in   any
		want float64
	}{
		{true, 1}, {false, 0}, {0.3, 0.3}, {1.7, 1}, {-1.0, 0}, {math.NaN(), 0}, {math.Inf(1), 0}, {5, 1}, {-2, 0}, {0, 0},
	}
	for _, c := range cases {
		got, err := Sanitize([]any{c.in})
		if err != nil || got != c.want {
			t.Errorf("Sanitize(%v) = %v %v", c.in, got, err)
		}
	}
	if _, err := Sanitize([]any{"str"}); err == nil {
		t.Error("string should fail")
	}
}

func TestBadMode(t *testing.T) {
	_, err := New(&Deps{}, "A", "nope")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNormalizeDistance(t *testing.T) {
	h := newTestHandler(t, "distance")
	mc := config.M{"trigger_range": config.M{"bottom": 0.0, "top": 0.8}}
	if v := h.normalizeDistance(mc, 0.4); math.Abs(v-0.5) > 1e-9 {
		t.Fatal(v)
	}
	if v := h.normalizeDistance(mc, 0.95); v != 1 {
		t.Fatal(v)
	}
	if v := h.normalizeDistance(config.M{"trigger_range": config.M{"bottom": 0.5, "top": 0.5}}, 0.9); v != 0 {
		t.Fatal("invalid range must give 0")
	}
}

func TestInterpolateCurve(t *testing.T) {
	pts := []Point{{0, 0}, {0.5, 0.2}, {1, 1}}
	if v := InterpolateCurve(0.25, pts); math.Abs(v-0.1) > 1e-9 {
		t.Fatal(v)
	}
	if v := InterpolateCurve(0.75, pts); math.Abs(v-0.6) > 1e-9 {
		t.Fatal(v)
	}
	if InterpolateCurve(1.5, pts) != 1 || InterpolateCurve(-1, pts) != 0 {
		t.Fatal("bounds")
	}
}

func TestDistanceHandlerSetsStrength(t *testing.T) {
	h := newTestHandler(t, "distance")
	h.Handle(0.4, &Context{Address: "/x"})
	h.mu.Lock()
	got, cleared := h.bgCurrentStrength, h.isCleared
	h.mu.Unlock()
	if math.Abs(got-0.5) > 1e-9 || cleared {
		t.Fatalf("strength=%v cleared=%v", got, cleared)
	}
}

func TestShockEnvelope(t *testing.T) {
	h := newTestHandler(t, "shock")
	h.Handle(0.9, &Context{})
	mc := h.modeConfig()
	h.mu.Lock()
	start := h.shockStartedAt
	s0 := h.shockStrengthLocked(mc, start+0.0001)
	s1 := h.shockStrengthLocked(mc, start+1.0)
	s2 := h.shockStrengthLocked(mc, start+3.0)
	h.mu.Unlock()
	if s0 < 0.99 || math.Abs(s1-0.5) > 1e-6 || s2 != 0 {
		t.Fatalf("envelope %v %v %v", s0, s1, s2)
	}
	// shock wave fallback is scaled default wave
	h.mu.Lock()
	w := h.buildDynamicWave(mc, "shock", 0, 0.5, "", 1, 2)
	h.mu.Unlock()
	if ops, _ := wave.DecodeOps(w); len(ops) != 2 || ops[0] != "0A0A0A0A32323232" {
		t.Fatalf("shock wave %s", w)
	}
}

func TestComputeDerivative(t *testing.T) {
	h := newTestHandler(t, "touch")
	h.mu.Lock()
	for i := 0; i < 10; i++ {
		h.touchDist = append(h.touchDist, tdPoint{float64(i) * 0.1, float64(i) * 0.05})
	}
	h.mu.Unlock()
	d := h.ComputeDerivative()
	// linear motion: velocity 0.5/s, acceleration ~0
	if math.Abs(d[1]-0.5) > 1e-9 || math.Abs(d[2]) > 1e-9 {
		t.Fatalf("derivatives %v", d)
	}
}

func TestTouchPresetBands(t *testing.T) {
	h := newTestHandler(t, "touch")
	mc := config.M{"touch": config.M{
		"wave_preset": "base", "wave_scale": 0.9,
		"preset_bands": []any{
			config.M{"threshold": 0.25, "wave_preset": "low", "wave_scale": 0.2},
			config.M{"threshold": 0.55, "wave_preset": "mid"},
		},
	}}
	if p, s := h.resolveTouchProfile(mc, 0.1); p != "base" || s != 0.9 {
		t.Fatal(p, s)
	}
	if p, s := h.resolveTouchProfile(mc, 0.3); p != "low" || s != 0.2 {
		t.Fatal(p, s)
	}
	if p, s := h.resolveTouchProfile(mc, 0.9); p != "mid" || s != 0.9 {
		t.Fatal(p, s)
	}
}

func TestComboShortTouchFiresShock(t *testing.T) {
	h := newTestHandler(t, "combo")
	h.Handle(0.5, &Context{})
	h.Handle(0.0, &Context{})
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.comboShockFired || h.shockStartedAt == 0 {
		t.Fatal("short touch should fire shock")
	}
}

func TestSPSAggregationInOSC(t *testing.T) {
	h := newTestHandler(t, "distance")
	h.OSC("/avatar/parameters/OGB/Pen/Dick/PenOthers", []any{0.6})
	h.mu.Lock()
	got := h.bgCurrentStrength
	h.mu.Unlock()
	if math.Abs(got-0.75) > 1e-9 { // 0.6 / 0.8 trigger top
		t.Fatalf("strength %v", got)
	}
}

// Feeders keep only ~200 ms queued in the APP instead of sending a whole
// window every 100 ms tick (which piled up seconds of stale output).
func TestOutputPacing(t *testing.T) {
	h := newTestHandler(t, "distance")
	sent := 0
	h.deps.Hub.WaveObserver = func(_, w string) {
		ops, _ := wave.DecodeOps(w)
		sent += len(ops)
	}
	h.mu.Lock()
	h.bgCurrentStrength = 0.5
	h.mu.Unlock()
	var last float64
	h.distanceTick(&last)
	if sent != 2 {
		t.Fatalf("first tick sent %d ops, want 2 (200 ms lead)", sent)
	}
	h.distanceTick(&last)
	if sent != 2 {
		t.Fatalf("buffer full: sent %d ops, want none", sent)
	}
	h.mu.Lock()
	h.aheadUntil = nowSec() + 0.1 // 100 ms played
	h.mu.Unlock()
	h.distanceTick(&last)
	if sent != 3 {
		t.Fatalf("steady state: sent %d ops total, want 3", sent)
	}
	// Over a simulated 2 s of ticks the APP never holds more than lead+1 op.
	for i := 0; i < 20; i++ {
		h.mu.Lock()
		h.aheadUntil -= 0.1 // as if 100 ms elapsed
		h.mu.Unlock()
		h.distanceTick(&last)
		h.mu.Lock()
		buffered := h.aheadUntil - nowSec()
		h.mu.Unlock()
		if buffered > outputLead+opSeconds+0.01 {
			t.Fatalf("tick %d: %.2fs buffered", i, buffered)
		}
	}
}

// With follow_value the shock strength follows the input: stronger input
// raises it at once, weaker input lets it decay down to that level.
func TestShockFollowValue(t *testing.T) {
	h := newTestHandler(t, "shock")
	h.deps.Cfg.Write(func(adv, _ config.M) {
		sc := config.Sub(adv, true, "dglab3", "channel_a", "mode_config", "shock")
		sc["follow_value"] = true
		sc["min_strength"] = 0.2
		sc["duration"] = 2
		tr := config.Sub(adv, true, "dglab3", "channel_a", "mode_config", "trigger_range")
		tr["bottom"], tr["top"] = 0.0, 1.0
	})
	peak := func() float64 {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.shockPeak
	}
	h.Handle(0.5, &Context{})
	if got := peak(); math.Abs(got-0.6) > 1e-9 { // 0.2 + 0.8*0.5
		t.Fatalf("peak %v, want 0.6", got)
	}
	h.Handle(0.25, &Context{}) // weaker: keep decaying from 0.6
	if got := peak(); math.Abs(got-0.6) > 1e-9 {
		t.Fatalf("weaker input restarted shock: peak %v", got)
	}
	h.Handle(1.0, &Context{})
	if got := peak(); got != 1 {
		t.Fatalf("stronger input: peak %v, want 1", got)
	}
	// Without follow_value every trigger is full strength (old behaviour).
	h2 := newTestHandler(t, "shock")
	h2.Handle(0.1, &Context{})
	h2.mu.Lock()
	defer h2.mu.Unlock()
	if h2.shockPeak != 1 {
		t.Fatalf("default peak %v", h2.shockPeak)
	}
}
