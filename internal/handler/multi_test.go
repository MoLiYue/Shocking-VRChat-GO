package handler

import (
	"math"
	"testing"

	"shockingvrc/internal/config"
	"shockingvrc/internal/device"
	"shockingvrc/internal/wave"
)

func newTestMulti(t *testing.T, pm config.M) (*Multi, *[]string) {
	t.Helper()
	cfg := config.NewStore(t.TempDir())
	cfg.Write(func(adv, basic config.M) {
		config.SyncBasicIntoAdvanced(adv, basic)
		tr := config.Sub(adv, true, "dglab3", "channel_a", "mode_config", "trigger_range")
		tr["bottom"], tr["top"] = 0.0, 1.0
	})
	hub := device.NewHub()
	var sent []string
	hub.WaveObserver = func(_, w string) { sent = append(sent, w) }
	mc, err := ParseMultiConfig(pm)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMulti(&Deps{Cfg: cfg, Hub: hub, Lib: wave.NewLibrary(t.TempDir())}, "A", "/p", mc)
	if err != nil {
		t.Fatal(err)
	}
	return m, &sent
}

func twoRanges(extra config.M) config.M {
	pm := config.M{"mode": RangesMode, "ranges": []any{
		config.M{"from": 0.5, "to": 1.0, "mode": "shock"}, // unsorted on purpose
		config.M{"from": 0.0, "to": 0.5, "mode": "distance"},
	}, "blend": 0.1, "crossfade": 1.0}
	for k, v := range extra {
		pm[k] = v
	}
	return pm
}

func TestParseMultiConfig(t *testing.T) {
	c, err := ParseMultiConfig(twoRanges(nil))
	if err != nil || c.Ranges[0].Mode != "distance" || c.Curve != "equal_power" || !c.Remap {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range []config.M{
		{"ranges": []any{}},
		{"ranges": []any{config.M{"from": 0.5, "to": 0.5, "mode": "shock"}}},
		{"ranges": []any{config.M{"from": 0, "to": 1, "mode": "ranges"}}},
	} {
		if _, err := ParseMultiConfig(bad); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}

// Value ranges set the fader targets; around an edge both neighbours play.
func TestMultiTargets(t *testing.T) {
	m, _ := newTestMulti(t, twoRanges(nil))
	for _, c := range []struct{ v, d, s float64 }{
		{0.2, 1, 0}, {0.45, 1, 0}, {0.5, 0.5, 0.5}, {0.525, 0.25, 0.75}, {0.8, 0, 1}, {1, 0, 1},
	} {
		m.mu.Lock()
		got := m.targetsLocked(c.v)
		m.mu.Unlock()
		if math.Abs(got[0]-c.d) > 1e-9 || math.Abs(got[1]-c.s) > 1e-9 {
			t.Fatalf("v=%v: targets %v, want [%v %v]", c.v, got, c.d, c.s)
		}
	}
}

// Jumping from one range to the other crossfades over `crossfade` seconds
// instead of switching: the old mode fades out while the new fades in, and
// the mixed intensity never drops to silence in between.
func TestMultiCrossfade(t *testing.T) {
	m, sent := newTestMulti(t, twoRanges(nil))
	step := func() {
		m.mu.Lock()
		m.lastTick = nowSec() - 0.1 // 100 ms per tick
		m.aheadUntil = 0            // APP buffer drained
		m.mu.Unlock()
		m.tick()
	}
	m.Handle(0.4, nil) // distance range, value 0.8 within it
	for i := 0; i < 12; i++ {
		step()
	}
	if g := m.Gains(); g[0] != 1 || g[1] != 0 {
		t.Fatalf("settled gains %v", g)
	}
	m.Handle(0.9, nil) // into the shock range
	var prevD float64 = 1
	minLevel := 100
	for i := 0; i < 12; i++ {
		*sent = nil
		step()
		g := m.Gains()
		if prevD-g[0] > 0.11 { // 0.1 per 100 ms tick (+ scheduling slack)
			t.Fatalf("tick %d: distance gain fell %.2f -> %.2f (faster than crossfade)", i, prevD, g[0])
		}
		prevD = g[0]
		if len(*sent) == 1 {
			ops, _ := wave.DecodeOps((*sent)[0])
			for _, op := range ops {
				for j := 0; j < 4; j++ {
					if s := wave.HexByte(op, 8+j*2); s < minLevel {
						minLevel = s
					}
				}
			}
		}
	}
	if g := m.Gains(); g[0] != 0 || g[1] != 1 {
		t.Fatalf("after crossfade gains %v", g)
	}
	if minLevel < 50 {
		t.Fatalf("mix dipped to %d%% during the crossfade (equal-power should keep it up)", minLevel)
	}
}

// With a single audible range the mix is exactly that mode's output.
func TestMultiSingleRangePassThrough(t *testing.T) {
	m, sent := newTestMulti(t, config.M{"ranges": []any{config.M{"from": 0, "to": 1, "mode": "distance"}}, "crossfade": 0})
	m.Handle(0.5, nil)
	m.mu.Lock()
	m.aheadUntil = 0
	m.mu.Unlock()
	m.tick()
	if len(*sent) != 1 {
		t.Fatalf("sent %v", *sent)
	}
	ops, _ := wave.DecodeOps((*sent)[0])
	if len(ops) != 2 || wave.HexByte(ops[1], 8) != 50 {
		t.Fatalf("ops %v, want 2 ops ending at 50%%", ops)
	}
}
