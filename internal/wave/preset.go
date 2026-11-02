package wave

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"shockingvrc/internal/logx"
)

// Preset is a loaded wave preset with decoded sample arrays.
type Preset struct {
	Name            string
	Ops             []string
	Wavestrs        []string
	Header          any
	PreviewSections any
	FreqSamples     []float64
	StrengthSamples []float64
	TextureSamples  []float64
}

// Library is the process-wide preset store (WavePresetLibrary singleton).
type Library struct {
	mu       sync.RWMutex
	dir      string
	presets  map[string]*Preset
	order    []string
	warnLast map[string]time.Time
	warnMu   sync.Mutex
}

// NewLibrary creates a library over dir and loads it.
func NewLibrary(dir string) *Library {
	l := &Library{dir: dir, warnLast: map[string]time.Time{}}
	l.Reload()
	return l
}

// Dir returns the preset directory.
func (l *Library) Dir() string { return l.dir }

func decodeSamples(ops []string) (freq, strength, texture []float64) {
	freq = []float64{}
	strength = []float64{}
	for _, op := range ops {
		for i := 0; i < 8; i += 2 {
			freq = append(freq, float64(HexByte(op, i)))
		}
		for i := 8; i < 16; i += 2 {
			strength = append(strength, float64(HexByte(op, i))/100.0)
		}
	}
	texture = make([]float64, len(strength))
	maxS := 0.0
	for _, s := range strength {
		if s > maxS {
			maxS = s
		}
	}
	if maxS > 0 {
		for i, s := range strength {
			texture[i] = s / maxS
		}
	}
	return
}

// BuildPreset converts a raw preset JSON object into a Preset.
func BuildPreset(raw map[string]any, fallbackName string) (*Preset, error) {
	name, _ := raw["name"].(string)
	if name == "" {
		name = fallbackName
	}
	var ops []string
	if v, ok := raw["ops"]; ok {
		o, err := CoerceOps(v)
		if err != nil {
			return nil, err
		}
		ops = o
	} else if v, ok := raw["wavestrs"]; ok {
		list, _ := v.([]any)
		for _, ws := range list {
			o, err := CoerceOps(ws)
			if err != nil {
				return nil, err
			}
			ops = append(ops, o...)
		}
	} else {
		return nil, fmt.Errorf("preset must contain `ops` or `wavestrs`")
	}
	if ops == nil {
		ops = []string{}
	}
	f, s, t := decodeSamples(ops)
	return &Preset{
		Name:            name,
		Ops:             ops,
		Wavestrs:        OpsToWavestrs(ops, 86),
		Header:          raw["header"],
		PreviewSections: raw["preview_sections"],
		FreqSamples:     f,
		StrengthSamples: s,
		TextureSamples:  t,
	}, nil
}

// Reload rescans the preset directory.
func (l *Library) Reload() {
	presets := map[string]*Preset{}
	order := []string{}
	files, err := filepath.Glob(filepath.Join(l.dir, "*.json"))
	if err != nil || files == nil {
		if _, statErr := os.Stat(l.dir); statErr != nil {
			logx.Warn("[preset] Directory not found: %s", l.dir)
		}
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			logx.Error("[preset] Failed to load %s: %v", filepath.Base(f), err)
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			logx.Error("[preset] Failed to load %s: %v", filepath.Base(f), err)
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		p, err := BuildPreset(raw, stem)
		if err != nil {
			logx.Error("[preset] Failed to load %s: %v", filepath.Base(f), err)
			continue
		}
		if _, exists := presets[p.Name]; !exists {
			order = append(order, p.Name)
		}
		presets[p.Name] = p
		logx.Info("[preset] Loaded: %s (%d ops)", p.Name, len(p.Ops))
	}
	l.mu.Lock()
	l.presets, l.order = presets, order
	l.mu.Unlock()
}

// Names returns preset names in load order.
func (l *Library) Names() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]string{}, l.order...)
}

// Get returns the named preset or nil (logs a throttled warning when missing).
func (l *Library) Get(name string) *Preset {
	if name == "" {
		return nil
	}
	l.mu.RLock()
	p := l.presets[name]
	l.mu.RUnlock()
	if p == nil {
		l.warnMu.Lock()
		if time.Since(l.warnLast[name]) > 10*time.Second {
			l.warnLast[name] = time.Now()
			l.warnMu.Unlock()
			logx.Warn("[preset] Not found: %s", name)
		} else {
			l.warnMu.Unlock()
		}
	}
	return p
}

// SampleLooped linearly samples a looped sequence at a fractional position.
func SampleLooped(seq []float64, pos float64) float64 {
	n := len(seq)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return seq[0]
	}
	p := math.Mod(pos, float64(n))
	if p < 0 {
		p += float64(n)
	}
	left := int(math.Floor(p))
	right := (left + 1) % n
	frac := p - float64(left)
	return seq[left%n]*(1-frac) + seq[right]*frac
}

// BuildScaledWindow returns windowSize ops starting at startIndex, scaled.
func (l *Library) BuildScaledWindow(name string, startIndex, windowSize int, scale float64) string {
	p := l.Get(name)
	if p == nil || len(p.Ops) == 0 || windowSize <= 0 {
		return ""
	}
	ops := make([]string, windowSize)
	n := len(p.Ops)
	for i := 0; i < windowSize; i++ {
		idx := ((startIndex+i)%n + n) % n
		ops[i] = ScaleOp(p.Ops[idx], scale)
	}
	return EncodeOps(ops)
}

// BuildResampledWindow resamples the preset texture/freq at fractional
// positions to produce windowOps ops (build_resampled_window).
func (l *Library) BuildResampledWindow(name string, start float64, windowOps int, waveScale, textureFloor, sampleStep float64) string {
	p := l.Get(name)
	if p == nil || len(p.FreqSamples) == 0 || len(p.TextureSamples) == 0 || windowOps <= 0 {
		return ""
	}
	total := windowOps * 4
	waveScale = clampf(waveScale, 0, 1)
	textureFloor = clampf(textureFloor, 0, 1)
	freqs := make([]int, total)
	strs := make([]int, total)
	for i := 0; i < total; i++ {
		pos := start + float64(i)*sampleStep
		tex := SampleLooped(p.TextureSamples, pos)
		f := SampleLooped(p.FreqSamples, pos)
		if f < 0.5 && tex <= 0 {
			continue // explicit silence
		}
		lifted := textureFloor + tex*(1-textureFloor)
		freqs[i] = clampi(Round(f), 0, 255)
		strs[i] = clampi(Round(lifted*waveScale*100), 0, 100)
	}
	ops := make([]string, 0, windowOps)
	for i := 0; i < total; i += 4 {
		var sb strings.Builder
		for j := 0; j < 4; j++ {
			fmt.Fprintf(&sb, "%02X", freqs[i+j])
		}
		for j := 0; j < 4; j++ {
			fmt.Fprintf(&sb, "%02X", strs[i+j])
		}
		ops = append(ops, sb.String())
	}
	return EncodeOps(ops)
}

// BuildScaledWavestrs scales the whole preset and chunks it.
func (l *Library) BuildScaledWavestrs(name string, scale float64) []string {
	p := l.Get(name)
	if p == nil || len(p.Ops) == 0 {
		return nil
	}
	ops := make([]string, len(p.Ops))
	for i, op := range p.Ops {
		ops[i] = ScaleOp(op, scale)
	}
	return OpsToWavestrs(ops, 86)
}
