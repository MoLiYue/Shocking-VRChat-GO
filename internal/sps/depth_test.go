package sps

import (
	"math"
	"testing"
)

type step struct {
	sig string
	v   float64
}

func run(c *Calculator, steps []step) []float64 {
	out := make([]float64, len(steps))
	for i, s := range steps {
		out[i] = math.Round(c.Update(s.sig, s.v)*1e6) / 1e6
	}
	return out
}

func eq(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9 {
			return false
		}
	}
	return true
}

// Expected values come from the Python srv/sps_depth.py.
func TestSocketRootTipMatchesPython(t *testing.T) {
	c := NewCalculator(Socket)
	got := run(c, []step{
		{"PenOthersNewRoot", 0.3}, {"PenOthersNewTip", 0.6}, {"PenOthersNewRoot", 0.4}, {"PenOthersNewTip", 0.7},
		{"PenOthersNewRoot", 0.5}, {"PenOthersNewTip", 0.8}, {"PenOthersNewRoot", 0.6}, {"PenOthersNewTip", 0.9},
		{"PenOthersNewRoot", 0.8}, {"PenOthersNewTip", 1.0}, {"PenOthersNewRoot", 0.9}, {"PenSelf", 0.4},
		{"PenOthersClose", 0},
	})
	want := []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0.5, 0.5, 0.5}
	if !eq(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestLegacyCloseGate(t *testing.T) {
	c := NewCalculator(Socket)
	got := run(c, []step{{"PenOthers", 0.5}, {"PenOthersClose", 0}, {"PenOthersClose", 1}, {"PenSelf", 0.7}})
	if want := []float64{0.5, 0, 0.5, 0.7}; !eq(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	c.Reset()
	if c.Depth() != 0 {
		t.Fatal("reset")
	}
}

func TestParseOGBAddress(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
		tt   string
	}{
		{"/avatar/parameters/OGB/Orf/Pussy/PenOthersNewTip", true, Socket},
		{"/avatar/parameters/OGB/Pen/Dick/PenOthers", true, Plug},
		{"/avatar/parameters/OGB/Pen/Dick/PenOthersNewTip", false, ""},
		{"/avatar/parameters/OGB/Orf/Pussy/TouchSelf", false, ""},
		{"/avatar/parameters/OGB/Orf/PenSelf", false, ""},
		{"/avatar/parameters/Other/x", false, ""},
	}
	for _, c := range cases {
		tt, _, _, ok := ParseOGBAddress(c.addr)
		if ok != c.ok || tt != c.tt {
			t.Errorf("%s: got %v %s", c.addr, ok, tt)
		}
	}
}

func TestClampNaN(t *testing.T) {
	c := NewCalculator(Plug)
	if d := c.Update("PenSelf", math.NaN()); d != 0 {
		t.Fatalf("NaN should clamp to 0, got %v", d)
	}
	if d := c.Update("PenOthers", 5); d != 1 {
		t.Fatalf("clamp >1, got %v", d)
	}
}
