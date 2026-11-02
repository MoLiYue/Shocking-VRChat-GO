package wave

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// PulsePrefix is the .pulse export header.
const PulsePrefix = "Dungeonlab+pulse:"

var freqTable = buildFreqTable()

func buildFreqTable() []int {
	t := []int{}
	for ms := 10; ms < 50; ms++ {
		t = append(t, ms)
	}
	for ms := 50; ms < 80; ms += 2 {
		t = append(t, ms)
	}
	for ms := 80; ms < 100; ms += 5 {
		t = append(t, ms)
	}
	for ms := 100; ms < 200; ms += 10 {
		t = append(t, ms)
	}
	steps := []int{33, 33, 34}
	for ms, i := 200, 0; ms <= 400; i++ {
		t = append(t, ms)
		if ms == 400 {
			break
		}
		ms += steps[i%3]
	}
	for ms := 450; ms < 600; ms += 50 {
		t = append(t, ms)
	}
	for ms := 600; ms <= 1000; ms += 100 {
		t = append(t, ms)
	}
	return t
}

// SliderToIntervalMs maps a frequency slider (0..83) to a pulse interval.
func SliderToIntervalMs(slider int) int { return freqTable[clampi(slider, 0, 83)] }

// SliderToFreqByte maps a slider to the protocol freq byte (10..240).
func SliderToFreqByte(slider int) int { return clampi(SliderToIntervalMs(slider), 10, 240) }

// PulseHeader is the optional "rest,speed,param3=" header.
type PulseHeader struct {
	RestTicks int    `json:"rest_ticks"`
	Speed     int    `json:"speed"`
	Param3    *int   `json:"param3"`
	Raw       string `json:"raw"`
}

// PulsePoint is (strength%, anchor flag).
type PulsePoint struct {
	Value float64
	Flag  int
}

// PulseSection is one "+section+" block.
type PulseSection struct {
	FreqLow  int
	FreqHigh int
	Duration int
	FreqMode int
	Enabled  bool
	Points   []PulsePoint
	Raw      string
}

var nonNumeric = regexp.MustCompile(`[^\d.\-]`)

func parseFloatLoose(s string) float64 {
	if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return f
	}
	c := nonNumeric.ReplaceAllString(s, "")
	if f, err := strconv.ParseFloat(c, 64); err == nil {
		return f
	}
	return 0
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ParsePulse parses .pulse text into header and sections.
func ParsePulse(text string) (*PulseHeader, []PulseSection, error) {
	text = strings.TrimSpace(text)
	body := strings.TrimPrefix(text, PulsePrefix)
	var header *PulseHeader
	sectionsStr := body
	if i := strings.Index(body, "="); i >= 0 {
		hs := body[:i]
		sectionsStr = body[i+1:]
		parts := []string{}
		for _, p := range strings.Split(hs, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		h := &PulseHeader{Speed: 1, Raw: hs}
		if len(parts) > 0 {
			f, err := strconv.ParseFloat(parts[0], 64)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid header rest_ticks: %q", parts[0])
			}
			h.RestTicks = int(f)
		}
		if len(parts) > 1 && isDigits(parts[1]) {
			h.Speed, _ = strconv.Atoi(parts[1])
		}
		if len(parts) > 2 {
			f, err := strconv.ParseFloat(parts[2], 64)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid header param3: %q", parts[2])
			}
			v := int(f)
			h.Param3 = &v
		}
		header = h
	}
	sections := []PulseSection{}
	for _, raw := range strings.Split(sectionsStr, "+section+") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		metaStr, pointsStr := raw, ""
		if i := strings.Index(raw, "/"); i >= 0 {
			metaStr, pointsStr = raw[:i], raw[i+1:]
		}
		meta := []int{}
		for _, tok := range strings.Split(metaStr, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			if f, err := strconv.ParseFloat(tok, 64); err == nil {
				meta = append(meta, int(f))
			}
		}
		sec := PulseSection{Raw: raw, FreqMode: 1, Enabled: true}
		if len(meta) > 0 {
			sec.FreqLow = meta[0]
		}
		sec.FreqHigh = sec.FreqLow
		if len(meta) > 1 {
			sec.FreqHigh = meta[1]
		}
		if len(meta) > 2 {
			sec.Duration = meta[2]
		}
		if len(meta) > 3 {
			sec.FreqMode = meta[3]
		}
		if len(meta) > 4 {
			sec.Enabled = meta[4] != 0
		}
		if strings.TrimSpace(pointsStr) != "" {
			for _, p := range strings.Split(pointsStr, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				pt := PulsePoint{}
				if i := strings.LastIndex(p, "-"); i >= 0 {
					pt.Value = parseFloatLoose(p[:i])
					if f, err := strconv.ParseFloat(p[i+1:], 64); err == nil {
						pt.Flag = int(f)
					}
				} else {
					pt.Value = parseFloatLoose(p)
				}
				sec.Points = append(sec.Points, pt)
			}
		}
		sections = append(sections, sec)
	}
	return header, sections, nil
}

// ConvertPulse converts .pulse text to ops/wavestrs.
func ConvertPulse(text string, maxOpsPerMsg int) (ops, wavestrs []string, header *PulseHeader, sections []PulseSection, err error) {
	header, sections, err = ParsePulse(text)
	if err != nil {
		return
	}
	speed := 1
	if header != nil {
		speed = header.Speed
	}
	if speed <= 0 {
		err = fmt.Errorf("invalid speed %d", speed)
		return
	}
	slotsPerPoint := 4 / speed
	if slotsPerPoint <= 0 {
		slotsPerPoint = 1
	}
	ops = []string{}
	for _, sec := range sections {
		if !sec.Enabled || len(sec.Points) == 0 {
			continue
		}
		n := len(sec.Points)
		duration := sec.Duration
		if duration < 0 {
			duration = 0
		}
		repeats := 1
		if duration > 0 && n < duration {
			repeats = int(math.Ceil(float64(duration) / float64(n)))
		}
		strengths := make([]float64, n)
		for i, p := range sec.Points {
			strengths[i] = clampf(p.Value, 0, 100)
		}
		fLow, fHigh := SliderToFreqByte(sec.FreqLow), SliderToFreqByte(sec.FreqHigh)
		total := repeats * n
		var allS []float64
		var allF []int
		for pi := 0; pi < total; pi++ {
			unit := pi % n
			rep := pi / n
			s := strengths[unit]
			var freq int
			switch sec.FreqMode {
			case 2:
				t := 0.0
				if total > 1 {
					t = float64(pi) / float64(total-1)
				}
				freq = Round(float64(fLow) + float64(fHigh-fLow)*t)
			case 3:
				t := 0.0
				if n > 1 {
					t = float64(unit) / float64(n-1)
				}
				freq = Round(float64(fLow) + float64(fHigh-fLow)*t)
			case 4:
				t := 0.0
				if repeats > 1 {
					t = float64(rep) / float64(repeats-1)
				}
				freq = Round(float64(fLow) + float64(fHigh-fLow)*t)
			default:
				freq = fLow
			}
			for k := 0; k < slotsPerPoint; k++ {
				allS = append(allS, s)
				allF = append(allF, freq)
			}
		}
		for len(allS)%4 != 0 {
			allS = append(allS, allS[len(allS)-1])
			allF = append(allF, allF[len(allF)-1])
		}
		for i := 0; i < len(allS); i += 4 {
			var sb strings.Builder
			for j := 0; j < 4; j++ {
				fmt.Fprintf(&sb, "%02X", clampi(allF[i+j], 10, 240))
			}
			for j := 0; j < 4; j++ {
				fmt.Fprintf(&sb, "%02X", clampi(Round(allS[i+j]), 0, 100))
			}
			ops = append(ops, sb.String())
		}
	}
	if header != nil && header.RestTicks > 0 {
		rest := header.RestTicks * slotsPerPoint
		rest += ((-rest)%4 + 4) % 4
		for i := 0; i < rest/4; i++ {
			ops = append(ops, "0000000000000000")
		}
	}
	if maxOpsPerMsg <= 0 {
		maxOpsPerMsg = 86
	}
	wavestrs = OpsToWavestrs(ops, maxOpsPerMsg)
	return
}

// PreviewSections builds the preview_sections metadata for the UI.
func PreviewSections(sections []PulseSection) []map[string]any {
	out := []map[string]any{}
	for _, sec := range sections {
		if !sec.Enabled || len(sec.Points) == 0 {
			continue
		}
		n := len(sec.Points)
		duration := sec.Duration
		if duration < 0 {
			duration = 0
		}
		repeats := 1
		if duration > 0 {
			repeats = int(math.Ceil(float64(duration) / float64(n)))
			if repeats < 1 {
				repeats = 1
			}
		}
		pts := make([]map[string]any, n)
		for i, p := range sec.Points {
			pts[i] = map[string]any{"strength": Round(clampf(p.Value, 0, 100)), "anchor": p.Flag == 1}
		}
		out = append(out, map[string]any{
			"freq_low":  sec.FreqLow,
			"freq_high": sec.FreqHigh,
			"freq_mode": sec.FreqMode,
			"duration":  duration,
			"n_points":  n,
			"repeats":   repeats,
			"points":    pts,
		})
	}
	return out
}
