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
	w := h.buildDynamicWave(mc, "shock", 0, 0.5, "", 1)
	h.mu.Unlock()
	if ops, _ := wave.DecodeOps(w); len(ops) != 10 || ops[0] != "0A0A0A0A32323232" {
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
