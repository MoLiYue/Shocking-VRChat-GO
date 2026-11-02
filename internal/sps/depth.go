// Package sps aggregates SPS1/SPS2 (OGB) OSC signals into a single 0..1
// insertion depth. Port of srv/sps_depth.py (algorithm from Neko-VRC / OGB).
package sps

import (
	"math"
	"sort"
	"strings"
)

const (
	AvatarParameterPrefix = "/avatar/parameters/"
	OGBParameterPrefix    = AvatarParameterPrefix + "OGB/"

	Socket = "sps_socket"
	Plug   = "sps_plug"

	closeGateSignal = "PenOthersClose"
)

var socketPairSignals = map[string]bool{
	"PenSelfNewRoot": true, "PenSelfNewTip": true,
	"PenOthersNewRoot": true, "PenOthersNewTip": true,
}

// SocketSignals are the socket signals that matter for depth.
var SocketSignals = []string{
	"PenSelfNewRoot", "PenSelfNewTip",
	"PenOthersNewRoot", "PenOthersNewTip",
	"PenSelf", "PenOthers",
	closeGateSignal,
}

// PlugSignals are the plug signals that matter for depth.
var PlugSignals = []string{"PenSelf", "PenOthers"}

var validSignals = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range SocketSignals {
		m[s] = true
	}
	for _, s := range PlugSignals {
		m[s] = true
	}
	return m
}()

var plugSignalSet = map[string]bool{"PenSelf": true, "PenOthers": true}

// IsOGBAddress reports whether address is in the OGB namespace.
func IsOGBAddress(address string) bool { return strings.HasPrefix(address, OGBParameterPrefix) }

// ParseOGBAddress parses /avatar/parameters/OGB/<Orf|Pen>/<zone>/<signal>.
func ParseOGBAddress(address string) (triggerType, zone, signal string, ok bool) {
	if !IsOGBAddress(address) {
		return
	}
	parts := strings.Split(strings.Trim(address, "/"), "/")
	if len(parts) != 6 || parts[0] != "avatar" || parts[1] != "parameters" || parts[2] != "OGB" {
		return
	}
	switch parts[3] {
	case "Orf":
		triggerType = Socket
	case "Pen":
		triggerType = Plug
	default:
		return "", "", "", false
	}
	zone, signal = parts[4], parts[5]
	if zone == "" || !validSignals[signal] {
		return "", "", "", false
	}
	if triggerType == Plug && !plugSignalSet[signal] {
		return "", "", "", false
	}
	return triggerType, zone, signal, true
}

// Kind returns "Orf" for sockets and "Pen" for plugs.
func Kind(triggerType string) string {
	if triggerType == Socket {
		return "Orf"
	}
	return "Pen"
}

func clampUnit(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Min(1, math.Max(0, v))
}

type lengthDetector struct {
	length      *float64
	recent      []float64
	penetrating *float64
}

const maxSamples = 8

func (d *lengthDetector) recalc() {
	if len(d.recent) < 4 {
		d.length = d.penetrating
		return
	}
	s := append([]float64{}, d.recent...)
	sort.Float64s(s)
	best, bestDiff := 1, math.Inf(1)
	for i := 1; i < len(s); i++ {
		if diff := math.Abs(s[i] - s[i-1]); diff < bestDiff {
			best, bestDiff = i, diff
		}
	}
	v := s[best]
	d.length = &v
}

func (d *lengthDetector) reset() {
	d.length, d.recent, d.penetrating = nil, nil, nil
}

func (d *lengthDetector) update(root, tip *float64) {
	if root == nil || tip == nil {
		return
	}
	r, t := *root, *tip
	if r < 0.01 || t < 0.01 {
		d.reset()
		return
	}
	if r > 0.95 {
		return
	}
	length := t - r
	if length < 0.02 {
		return
	}
	if t > 0.99 {
		if d.penetrating == nil || length > *d.penetrating {
			v := length
			d.penetrating = &v
			d.recalc()
		}
		return
	}
	d.recent = append([]float64{length}, d.recent...)
	if len(d.recent) > maxSamples {
		d.recent = d.recent[:maxSamples]
	}
	d.recalc()
}

// Calculator aggregates one OGB zone into a depth.
type Calculator struct {
	TriggerType  string
	values       map[string]float64
	hasCloseGate bool
	detectors    map[string]*lengthDetector
}

// NewCalculator creates a calculator for a socket or plug.
func NewCalculator(triggerType string) *Calculator {
	return &Calculator{
		TriggerType: triggerType,
		values:      map[string]float64{},
		detectors:   map[string]*lengthDetector{"Self": {}, "Others": {}},
	}
}

func (c *Calculator) val(k string) *float64 {
	if v, ok := c.values[k]; ok {
		return &v
	}
	return nil
}

// Update feeds one signal and returns the new depth.
func (c *Calculator) Update(signal string, value float64) float64 {
	if signal == closeGateSignal {
		c.hasCloseGate = true
	}
	c.values[signal] = clampUnit(value)
	if c.TriggerType == Socket && socketPairSignals[signal] {
		for _, owner := range []string{"Self", "Others"} {
			if signal == "Pen"+owner+"NewRoot" || signal == "Pen"+owner+"NewTip" {
				c.detectors[owner].update(c.val("Pen"+owner+"NewRoot"), c.val("Pen"+owner+"NewTip"))
			}
		}
	}
	return c.Depth()
}

// Forget drops one signal.
func (c *Calculator) Forget(signal string) {
	delete(c.values, signal)
	for _, owner := range []string{"Self", "Others"} {
		if signal == "Pen"+owner+"NewRoot" || signal == "Pen"+owner+"NewTip" {
			c.detectors[owner].reset()
		}
	}
}

// Reset clears all state.
func (c *Calculator) Reset() {
	c.values = map[string]float64{}
	c.hasCloseGate = false
	for _, d := range c.detectors {
		d.reset()
	}
}

func (c *Calculator) newSocketDepth(owner string) *float64 {
	root, tip := c.val("Pen"+owner+"NewRoot"), c.val("Pen"+owner+"NewTip")
	if root == nil || tip == nil || (*root <= 0 && *tip <= 0) {
		return nil
	}
	l := c.detectors[owner].length
	res := 0.0
	if l != nil && *l != 0 && *tip > 0.99 {
		res = clampUnit(1 - (1-*root)/(*l))
	}
	return &res
}

func (c *Calculator) socketDepth(owner string) float64 {
	if d := c.newSocketDepth(owner); d != nil {
		return *d
	}
	if owner == "Others" && c.hasCloseGate && c.values[closeGateSignal] == 0 {
		return 0
	}
	return c.values["Pen"+owner]
}

// Depth returns the max of Self and Others.
func (c *Calculator) Depth() float64 {
	if c.TriggerType == Socket {
		return math.Max(c.socketDepth("Self"), c.socketDepth("Others"))
	}
	return math.Max(c.values["PenSelf"], c.values["PenOthers"])
}
