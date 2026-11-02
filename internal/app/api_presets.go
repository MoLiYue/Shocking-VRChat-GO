package app

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"shockingvrc/internal/config"
	"shockingvrc/internal/queue"
	"shockingvrc/internal/wave"
)

func (a *App) apiWaveSimulate(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, httpErr(400, "Invalid or missing JSON body")
	}
	mode := config.Str(data["mode"], "distance")
	ch := strings.ToLower(config.Str(data["channel"], "a"))
	input := config.Clamp(config.Float(data["input_value"], 0.5), 0, 1)
	params := orMap(data["params"])
	tr := config.AsMap(params["trigger_range"])
	if tr == nil {
		tr = config.M{"bottom": 0.0, "top": 0.8}
	}
	bottom, top := config.Float(tr["bottom"], 0.0), config.Float(tr["top"], 0.8)
	normalized := 0.0
	if top > bottom && input > bottom {
		normalized = math.Min(1, (input-bottom)/(top-bottom))
	}
	eff := normalized
	if pts := a.curvePoints("channel_" + ch); len(pts) > 0 {
		eff = applyCurveSim(normalized, pts)
	}
	preset := config.Str(params["wave_preset"], "")
	scale := config.Clamp(config.Float(params["wave_scale"], 1.0), 0, 1)
	floor := config.Clamp(config.Float(params["texture_floor"], 0.0), 0, 1)
	var p *wave.Preset
	if preset != "" {
		p = a.Lib.Get(preset)
	}
	samples := []float64{}
	durationMs := 0.0
	switch mode {
	case "shock":
		dur := config.Clamp(config.Float(params["duration"], 2.0), 0.5, 10)
		total := int(dur * 40)
		for i := 0; i < total; i++ {
			env := math.Max(0, 1-float64(i)/math.Max(1, float64(total-1)))
			v := env * scale * eff
			if p != nil && len(p.TextureSamples) > 0 {
				v = config.Clamp(p.TextureSamples[i%len(p.TextureSamples)]*scale*env*eff, 0, 1)
			}
			samples = append(samples, v)
		}
		durationMs = dur * 1000
	case "distance", "touch":
		win := config.ClampInt(config.Int(params["wave_window_ops"], 4), 1, 16)
		step := config.Clamp(config.Float(params["wave_sample_step"], 1.0), 0.25, 8)
		total := win * 4
		for i := 0; i < total; i++ {
			if p != nil && len(p.TextureSamples) > 0 {
				n := float64(len(p.TextureSamples))
				pos := math.Mod(float64(i)*step, n)
				left := int(pos) % len(p.TextureSamples)
				right := (left + 1) % len(p.TextureSamples)
				frac := pos - math.Floor(pos)
				tex := p.TextureSamples[left]*(1-frac) + p.TextureSamples[right]*frac
				samples = append(samples, config.Clamp((floor+tex*(1-floor))*scale*eff, 0, 1))
			} else {
				samples = append(samples, scale*eff)
			}
		}
		durationMs = float64(total * 25)
	}
	for i := range samples {
		samples[i] = round4(samples[i])
	}
	var wp any
	if preset != "" {
		wp = preset
	}
	return map[string]any{
		"samples":            samples,
		"effective_strength": round4(eff),
		"duration_ms":        durationMs,
		"info": map[string]any{
			"mode": mode, "input_value": input, "normalized": round4(normalized),
			"wave_preset": wp, "total_samples": len(samples),
		},
	}, nil
}

func (a *App) apiPresetSamples(r *http.Request) (any, error) {
	name := r.PathValue("name")
	p := a.Lib.Get(name)
	if p == nil {
		return nil, httpErr(404, "preset not found")
	}
	return map[string]any{
		"name": name, "texture_samples": p.TextureSamples, "strength_samples": p.StrengthSamples,
		"freq_samples": p.FreqSamples, "num_ops": len(p.Ops),
	}, nil
}

func headerField(h any, k string, def any) any {
	if m := config.AsMap(h); m != nil {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return def
}

func (a *App) apiPresetPreview(r *http.Request) (any, error) {
	name := r.PathValue("name")
	p := a.Lib.Get(name)
	if p == nil {
		return nil, httpErr(404, "preset not found")
	}
	if l := config.AsList(p.PreviewSections); len(l) > 0 {
		return map[string]any{
			"name": name, "speed": headerField(p.Header, "speed", 1),
			"rest_ticks": headerField(p.Header, "rest_ticks", 0), "sections": l,
		}, nil
	}
	if fn, ok := safeFile(name + ".pulse"); ok {
		if b, err := os.ReadFile(a.Path("dg-lab", fn)); err == nil {
			hdr, secs, err := wave.ParsePulse(string(b))
			if err == nil {
				speed, rest := 1, 0
				if hdr != nil {
					speed, rest = hdr.Speed, hdr.RestTicks
				}
				return map[string]any{"name": name, "speed": speed, "rest_ticks": rest, "sections": wave.PreviewSections(secs)}, nil
			}
		}
	}
	strengths := []map[string]any{}
	for _, op := range p.Ops {
		for i := 0; i < 4; i++ {
			strengths = append(strengths, map[string]any{"strength": wave.HexByte(op, 8+i*2), "anchor": true})
		}
	}
	return map[string]any{
		"name": name, "speed": headerField(p.Header, "speed", 1), "rest_ticks": headerField(p.Header, "rest_ticks", 0),
		"sections": []any{map[string]any{
			"freq_low": 0, "freq_high": 0, "freq_mode": 1, "duration": len(strengths),
			"n_points": len(strengths), "repeats": 1, "points": strengths,
		}},
	}, nil
}

func (a *App) apiPresetImport(r *http.Request) (any, error) {
	filename, b, err := readUpload(r)
	if err != nil {
		return nil, err
	}
	filename = filepath.Base(strings.ReplaceAll(filename, `\`, "/"))
	if filename == "" || filename == "." {
		return nil, httpErr(400, "empty filename")
	}
	content := strings.TrimSpace(strings.ToValidUTF8(string(b), "\uFFFD"))
	presetDir := a.Lib.Dir()
	_ = os.MkdirAll(presetDir, 0o755)
	writeJSONFile := func(name string, v any) error {
		out, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(presetDir, name+".json"), out, 0o644)
	}
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	switch {
	case strings.HasSuffix(filename, ".pulse"):
		ops, wavestrs, hdr, secs, err := wave.ConvertPulse(content, 86)
		if err != nil {
			return nil, httpErr(400, "pulse parse failed: %v", err)
		}
		if len(ops) == 0 {
			return nil, httpErr(400, "no valid wave data in file")
		}
		var h any
		if hdr != nil {
			h = hdr
		}
		if err := writeJSONFile(stem, map[string]any{
			"name": stem, "source_file": filename, "num_ops": len(ops), "header": h,
			"preview_sections": wave.PreviewSections(secs), "wavestrs": wavestrs,
		}); err != nil {
			return nil, err
		}
		_ = os.MkdirAll(a.Path("dg-lab"), 0o755)
		_ = os.WriteFile(a.Path("dg-lab", filename), []byte(content), 0o644)
		a.Lib.Reload()
		return map[string]any{"result": "OK", "name": stem, "ops": len(ops)}, nil
	case strings.HasSuffix(filename, ".json"):
		var data config.M
		if err := json.Unmarshal([]byte(content), &data); err != nil {
			return nil, httpErr(400, "invalid JSON: %v", err)
		}
		_, hasW := data["wavestrs"]
		_, hasO := data["ops"]
		if !hasW && !hasO {
			return nil, httpErr(400, `JSON must contain "wavestrs" or "ops"`)
		}
		name := config.Str(data["name"], "")
		if name == "" {
			name = stem
		}
		if _, ok := safeFile(name + ".json"); !ok {
			return nil, httpErr(400, "invalid preset name")
		}
		data["name"] = name
		if err := writeJSONFile(name, data); err != nil {
			return nil, err
		}
		a.Lib.Reload()
		return map[string]any{"result": "OK", "name": name, "ops": orZero(data["num_ops"])}, nil
	}
	return nil, httpErr(400, "unsupported format, use .pulse or .json")
}

func (a *App) apiWavePresetSend(r *http.Request) (any, error) {
	name := r.PathValue("name")
	dur, err := pathInt(r, "duration")
	if err != nil {
		return nil, err
	}
	p := a.Lib.Get(name)
	if p == nil {
		return nil, httpErr(404, "preset not found")
	}
	ch := strings.ToUpper(r.PathValue("channel"))
	if ch != "A" && ch != "B" {
		ch = "A"
	}
	dur = config.ClampInt(dur, 1, 10)
	need := dur * 10
	ops := make([]string, 0, need)
	for len(ops) < need && len(p.Ops) > 0 {
		for _, op := range p.Ops {
			if len(ops) == need {
				break
			}
			ops = append(ops, op)
		}
	}
	for i := 0; i < len(ops); i += 80 {
		end := min(i+80, len(ops))
		a.Queue.PutWave(queue.API, ch, wave.EncodeOps(ops[i:end]), "api_wave_preset")
	}
	return map[string]any{"result": "OK", "ops_sent": len(ops)}, nil
}
