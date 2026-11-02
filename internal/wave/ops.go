// Package wave implements DG-LAB wave op helpers, the preset library and
// the .pulse converter.
//
// A wave "op" is a 16-hex-char string: 4 freq bytes + 4 strength bytes,
// describing 100ms of output (4 x 25ms). A "wavestr" is a compact JSON array
// of ops, e.g. ["0A0A0A0A64646464"].
package wave

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// DefaultWave is srv.DEFAULT_WAVE (a ramp-up wave).
const DefaultWave = `["0A0A0A0A00000000","0A0A0A0A0A0A0A0A","0A0A0A0A14141414","0A0A0A0A1E1E1E1E","0A0A0A0A28282828","0A0A0A0A32323232","0A0A0A0A3C3C3C3C","0A0A0A0A46464646","0A0A0A0A50505050","0A0A0A0A5A5A5A5A","0A0A0A0A64646464"]`

// Round mimics Python 3 round() (banker's rounding).
func Round(f float64) int { return int(math.RoundToEven(f)) }

func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// HexByte parses two hex chars at op[i:i+2] (0 on error).
func HexByte(op string, i int) int {
	if i+2 > len(op) {
		return 0
	}
	v, err := strconv.ParseUint(op[i:i+2], 16, 8)
	if err != nil {
		return 0
	}
	return int(v)
}

// EncodeOps serializes ops as compact JSON.
func EncodeOps(ops []string) string {
	if ops == nil {
		ops = []string{}
	}
	b, _ := json.Marshal(ops)
	return string(b)
}

// DecodeOps parses a wavestr into its ops.
func DecodeOps(wavestr string) ([]string, error) {
	var ops []string
	if err := json.Unmarshal([]byte(wavestr), &ops); err != nil {
		return nil, err
	}
	return ops, nil
}

// CoerceOps validates raw wave data (JSON string or list) into upper-case ops.
func CoerceOps(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	var parsed any = raw
	if s, ok := raw.(string); ok {
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			return nil, err
		}
	}
	list, ok := parsed.([]any)
	if !ok {
		return nil, fmt.Errorf("wave data must be a JSON array")
	}
	ops := make([]string, 0, len(list))
	for _, o := range list {
		s, ok := o.(string)
		if !ok || len(s) != 16 {
			return nil, fmt.Errorf("invalid wave op: %v", o)
		}
		ops = append(ops, strings.ToUpper(s))
	}
	return ops, nil
}

// OpsToWavestrs chunks ops into wavestrs of at most chunk ops each.
func OpsToWavestrs(ops []string, chunk int) []string {
	out := []string{}
	for i := 0; i < len(ops); i += chunk {
		end := i + chunk
		if end > len(ops) {
			end = len(ops)
		}
		out = append(out, EncodeOps(ops[i:end]))
	}
	return out
}

// ScaleOp multiplies the 4 strength bytes of op by scale (clamped 0..1).
func ScaleOp(op string, scale float64) string {
	scale = clampf(scale, 0, 1)
	if len(op) < 16 {
		return op
	}
	var sb strings.Builder
	sb.WriteString(op[:8])
	for i := 8; i < 16; i += 2 {
		v := clampi(Round(float64(HexByte(op, i))*scale), 0, 100)
		fmt.Fprintf(&sb, "%02X", v)
	}
	return sb.String()
}

// ScaleWavestr scales every op of a wavestr; returns input unchanged on error.
func ScaleWavestr(wavestr string, scale float64) string {
	ops, err := DecodeOps(wavestr)
	if err != nil {
		return wavestr
	}
	for i := range ops {
		ops[i] = ScaleOp(ops[i], scale)
	}
	return EncodeOps(ops)
}

// ApplyCurve evaluates an easing curve on t in [0,1].
func ApplyCurve(t float64, curve string) (float64, error) {
	t = clampf(t, 0, 1)
	switch curve {
	case "linear":
		return t, nil
	case "smoothstep":
		return t * t * (3.0 - 2.0*t), nil
	case "ease_in_out_sine":
		return 0.5 - 0.5*math.Cos(math.Pi*t), nil
	}
	return 0, fmt.Errorf("unknown curve: %s", curve)
}

// Generate100ms builds a single 100ms op ramping from -> to with smoothstep,
// as ShockHandler.generate_wave_100ms_improved.
func Generate100ms(freq int, from, to float64) (string, error) {
	if from < 0 || from > 1 || to < 0 || to > 1 {
		return "", fmt.Errorf("from_ and to_ must be in [0,1]")
	}
	freq = clampi(freq, 0, 255)
	const steps = 4
	strengths := make([]int, steps)
	for i := 1; i <= steps; i++ {
		t, _ := ApplyCurve(float64(i)/steps, "smoothstep")
		v := from + (to-from)*t
		strengths[i-1] = clampi(Round(v*100), 0, 100)
	}
	strengths[steps-1] = clampi(Round(to*100), 0, 100)
	var sb strings.Builder
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&sb, "%02X", freq)
	}
	for _, s := range strengths {
		fmt.Fprintf(&sb, "%02X", s)
	}
	return EncodeOps([]string{sb.String()}), nil
}

// RepeatOp returns a wavestr containing op repeated n times.
func RepeatOp(op string, n int) string {
	ops := make([]string, n)
	for i := range ops {
		ops[i] = op
	}
	return EncodeOps(ops)
}
