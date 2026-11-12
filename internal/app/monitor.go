package app

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/sps"
)

// Parameter monitor: records which OSC parameters (by default PCS and OGB)
// appeared, with a timestamped history of their value changes. Kept in
// memory, persisted to param_monitor.json so it survives restarts.

const (
	monitorFile        = "param_monitor.json"
	monitorHistoryMax  = 500  // change points kept per parameter
	monitorEntriesMax  = 2000 // parameters kept (least recently seen dropped)
	monitorSaveEvery   = 30 * time.Second
	monitorPrefixCheck = 2 * time.Second
)

var defaultMonitorPrefixes = []string{
	sps.AvatarParameterPrefix + "pcs/",
	sps.OGBParameterPrefix,
}

type monPoint struct {
	T float64 `json:"t"` // unix seconds
	V any     `json:"v"`
}

type monEntry struct {
	Address   string     `json:"address"`
	Category  string     `json:"category"` // first path segment: pcs, ogb, ...
	Type      string     `json:"type"`     // float, int, bool, string
	FirstSeen float64    `json:"first_seen"`
	LastSeen  float64    `json:"last_seen"`
	Count     int64      `json:"count"`   // messages received
	Changes   int64      `json:"changes"` // value changes recorded
	LastValue any        `json:"last_value"`
	Min       *float64   `json:"min,omitempty"`
	Max       *float64   `json:"max,omitempty"`
	History   []monPoint `json:"history,omitempty"`
	// Declared: listed in the avatar's VRChat OSC config (Avatar = its
	// name); Count 0 means declared but not received yet.
	Declared bool   `json:"declared,omitempty"`
	Avatar   string `json:"avatar,omitempty"`
}

type paramMonitor struct {
	a  *App
	mu sync.Mutex

	entries map[string]*monEntry
	dirty   bool

	prefixes      []string
	enabled       bool
	prefixChecked time.Time
}

func newParamMonitor(a *App) *paramMonitor {
	return &paramMonitor{a: a, entries: map[string]*monEntry{}, enabled: false,
		prefixes: append([]string(nil), defaultMonitorPrefixes...)}
}

func (m *paramMonitor) path() string { return m.a.ConfigPath(monitorFile) }

// refreshSettingsLocked re-reads enabled/prefixes from the config (cheap
// enough every few seconds; record() runs for every OSC message).
func (m *paramMonitor) refreshSettingsLocked(now time.Time) {
	if now.Sub(m.prefixChecked) < monitorPrefixCheck {
		return
	}
	m.prefixChecked = now
	m.enabled = config.Bool(m.a.Cfg.Get("monitor", "enabled"), false)
	if l := config.AsList(m.a.Cfg.Get("monitor", "prefixes")); l != nil {
		ps := []string{}
		for _, p := range l {
			if s := strings.TrimSpace(config.Str(p, "")); s != "" {
				ps = append(ps, normalizeMonitorPrefix(s))
			}
		}
		m.prefixes = ps
	}
}

func normalizeMonitorPrefix(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = sps.AvatarParameterPrefix + p
	}
	return p
}

func (m *paramMonitor) matches(addr string) bool {
	for _, p := range m.prefixes {
		if strings.HasPrefix(addr, p) {
			return true
		}
	}
	return false
}

func monCategory(addr string) string {
	rest := strings.TrimPrefix(addr, sps.AvatarParameterPrefix)
	if rest == addr {
		return "other"
	}
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return strings.ToLower(rest[:i])
	}
	return "other"
}

func monType(v any) string {
	switch v.(type) {
	case bool:
		return "bool"
	case int, int32, int64:
		return "int"
	case float32, float64:
		return "float"
	case string:
		return "string"
	case nil:
		return "nil"
	}
	return fmt.Sprintf("%T", v)
}

func monNumber(v any) (float64, bool) {
	if _, ok := v.(string); ok {
		return 0, false // strings are compared as text, not parsed
	}
	f, ok := config.ToFloat(v)
	if ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
		return 0, false
	}
	return f, ok
}

func monChanged(prev, cur any) bool {
	pf, ok1 := monNumber(prev)
	cf, ok2 := monNumber(cur)
	if ok1 && ok2 {
		return math.Abs(pf-cf) >= 1e-4
	}
	return fmt.Sprint(prev) != fmt.Sprint(cur)
}

// record is called for every incoming OSC message.
func (m *paramMonitor) record(addr string, args []any) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshSettingsLocked(now)
	if !m.enabled {
		return
	}
	if addr == "/avatar/change" && len(args) > 0 {
		// VRChat sends this when an avatar loads (also right after VRChat
		// starts): import the avatar's full parameter list so parameters
		// show up before they are first triggered.
		if id, ok := args[0].(string); ok && id != "" {
			go m.importAvatarConfig(id)
		}
		return
	}
	if !m.matches(addr) {
		return
	}
	var v any
	if len(args) > 0 {
		v = args[0]
		if f, ok := v.(float64); ok {
			v = math.Round(f*1e6) / 1e6 // float32 noise -> stable JSON
		}
	}
	t := float64(now.UnixMilli()) / 1000
	e := m.entries[addr]
	if e == nil {
		if len(m.entries) >= monitorEntriesMax {
			m.evictLocked()
		}
		e = &monEntry{Address: addr, Category: monCategory(addr), FirstSeen: t}
		m.entries[addr] = e
	}
	first := e.Count == 0
	if first {
		e.FirstSeen = t // declared entries: first actually received
	}
	e.Count++
	e.LastSeen = t
	e.Type = monType(v)
	if first || monChanged(e.LastValue, v) {
		e.Changes++
		e.History = append(e.History, monPoint{t, v})
		if len(e.History) > monitorHistoryMax {
			e.History = append([]monPoint(nil), e.History[len(e.History)-monitorHistoryMax:]...)
		}
	}
	e.LastValue = v
	if f, ok := monNumber(v); ok {
		if e.Min == nil || f < *e.Min {
			e.Min = &f
		}
		if e.Max == nil || f > *e.Max {
			e.Max = &f
		}
	}
	m.dirty = true
}

func (m *paramMonitor) evictLocked() {
	var oldest string
	ot := math.Inf(1)
	for k, e := range m.entries {
		if e.LastSeen < ot {
			oldest, ot = k, e.LastSeen
		}
	}
	delete(m.entries, oldest)
}

type monFile struct {
	Version int         `json:"version"`
	Saved   float64     `json:"saved"`
	Params  []*monEntry `json:"params"`
}

func (m *paramMonitor) snapshotLocked(withHistory bool) []*monEntry {
	out := make([]*monEntry, 0, len(m.entries))
	for _, e := range m.entries {
		c := *e
		if withHistory {
			c.History = append([]monPoint(nil), e.History...)
		} else {
			c.History = nil
		}
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	return out
}

func (m *paramMonitor) load() {
	b, err := os.ReadFile(m.path())
	if err != nil {
		return
	}
	var f monFile
	if err := json.Unmarshal(b, &f); err != nil {
		logx.Warn("[monitor] Ignoring unreadable %s: %v", monitorFile, err)
		return
	}
	m.mu.Lock()
	for _, e := range f.Params {
		if e != nil && e.Address != "" {
			m.entries[e.Address] = e
		}
	}
	n := len(m.entries)
	m.mu.Unlock()
	logx.Info("[monitor] Loaded %d recorded parameters", n)
}

func (m *paramMonitor) save() error {
	m.mu.Lock()
	if !m.dirty {
		m.mu.Unlock()
		return nil
	}
	f := monFile{Version: 1, Saved: float64(time.Now().UnixMilli()) / 1000, Params: m.snapshotLocked(true)}
	m.dirty = false
	m.mu.Unlock()
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := m.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path())
}

// saveLoop persists the monitor periodically (and once more on exit).
func (m *paramMonitor) saveLoop(stop <-chan struct{}) {
	defer logx.Recover("monitor save")
	t := time.NewTicker(monitorSaveEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := m.save(); err != nil {
				logx.Warn("[monitor] Save failed: %v", err)
			}
		}
	}
}

// ---------------------------------------------------------------- API

func (a *App) apiMonitorList(r *http.Request) (any, error) {
	m := a.monitor
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	cat := strings.ToLower(r.URL.Query().Get("category"))
	m.mu.Lock()
	list := m.snapshotLocked(false)
	hist := map[string]int{}
	for k, e := range m.entries {
		hist[k] = len(e.History)
	}
	enabled, prefixes := m.enabled, append([]string(nil), m.prefixes...)
	m.mu.Unlock()
	out := []any{}
	cats := map[string]int{}
	for _, e := range list {
		cats[e.Category]++
		if cat != "" && e.Category != cat {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Address), q) {
			continue
		}
		b, _ := json.Marshal(e)
		var row map[string]any
		_ = json.Unmarshal(b, &row)
		row["history_len"] = hist[e.Address]
		out = append(out, row)
	}
	return map[string]any{"enabled": enabled, "prefixes": prefixes, "params": out, "categories": cats,
		"now": float64(time.Now().UnixMilli()) / 1000}, nil
}

func (a *App) apiMonitorHistory(r *http.Request) (any, error) {
	addr := r.URL.Query().Get("address")
	m := a.monitor
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[addr]
	if e == nil {
		return nil, httpErr(404, "unknown parameter")
	}
	return map[string]any{"address": addr, "points": append([]monPoint(nil), e.History...)}, nil
}

// apiMonitorChanges is a merged timeline of recent changes of all params.
func (a *App) apiMonitorChanges(r *http.Request) (any, error) {
	since := config.Float(r.URL.Query().Get("since"), 0)
	limit := config.ClampInt(config.Int(r.URL.Query().Get("limit"), 200), 1, 2000)
	type change struct {
		Address string  `json:"address"`
		T       float64 `json:"t"`
		V       any     `json:"v"`
	}
	var out []change
	m := a.monitor
	m.mu.Lock()
	for _, e := range m.entries {
		for i := len(e.History) - 1; i >= 0 && e.History[i].T > since; i-- {
			out = append(out, change{e.Address, e.History[i].T, e.History[i].V})
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].T > out[j].T })
	if len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []change{}
	}
	return map[string]any{"changes": out}, nil
}

func (a *App) apiMonitorSettings(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	a.Cfg.Write(func(adv, _ config.M) {
		mc := config.Sub(adv, true, "monitor")
		if v, ok := data["enabled"]; ok {
			mc["enabled"] = config.Bool(v, true)
		}
		if l := config.AsList(data["prefixes"]); l != nil {
			ps := []any{}
			for _, p := range l {
				if s := strings.TrimSpace(config.Str(p, "")); s != "" {
					ps = append(ps, normalizeMonitorPrefix(s))
				}
			}
			mc["prefixes"] = ps
		}
	})
	if err := a.Cfg.Save(); err != nil {
		return nil, err
	}
	a.monitor.mu.Lock()
	a.monitor.prefixChecked = time.Time{}
	a.monitor.refreshSettingsLocked(time.Now())
	a.monitor.mu.Unlock()
	return map[string]any{"success": true}, nil
}

func (a *App) apiMonitorClear(*http.Request) (any, error) {
	m := a.monitor
	m.mu.Lock()
	n := len(m.entries)
	m.entries = map[string]*monEntry{}
	m.dirty = true
	m.mu.Unlock()
	_ = m.save()
	logx.Info("[monitor] Cleared %d recorded parameters", n)
	return map[string]any{"success": true, "cleared": n}, nil
}

func (a *App) apiMonitorExport(*http.Request) (any, error) {
	m := a.monitor
	m.mu.Lock()
	f := monFile{Version: 1, Saved: float64(time.Now().UnixMilli()) / 1000, Params: m.snapshotLocked(true)}
	m.mu.Unlock()
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	name := "param_monitor_" + time.Now().Format("20060102_150405") + ".json"
	return rawResponse{
		contentType: "application/json",
		headers:     map[string]string{"Content-Disposition": `attachment; filename="` + name + `"`},
		body:        b,
	}, nil
}

// ---------------------------------------------------------------- avatar config

// vrchatOSCDir is VRChat's generated OSC config folder
// (%USERPROFILE%\AppData\LocalLow\VRChat\VRChat\OSC); var for tests.
var vrchatOSCDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "AppData", "LocalLow", "VRChat", "VRChat", "OSC")
}

type avatarOSCConfig struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Parameters []struct {
		Name  string `json:"name"`
		Input *struct {
			Address string `json:"address"`
			Type    string `json:"type"`
		} `json:"input"`
		Output *struct {
			Address string `json:"address"`
			Type    string `json:"type"`
		} `json:"output"`
	} `json:"parameters"`
}

// findAvatarConfig returns the newest <OSC>/usr_*/Avatars/<id>.json.
func findAvatarConfig(id string) string {
	dir := vrchatOSCDir()
	if dir == "" || strings.ContainsAny(id, `/\:`) || strings.Contains(id, "..") {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*", "Avatars", id+".json"))
	best, bestT := "", time.Time{}
	for _, f := range matches {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(bestT) {
			best, bestT = f, st.ModTime()
		}
	}
	return best
}

func (m *paramMonitor) importAvatarConfig(id string) {
	defer logx.Recover("monitor avatar import")
	path := findAvatarConfig(id)
	if path == "" {
		logx.Debug("[monitor] No VRChat OSC config found for avatar %s", id)
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	// VRChat writes the file with a UTF-8 BOM.
	b = []byte(strings.TrimPrefix(string(b), "\ufeff"))
	var cfg avatarOSCConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		logx.Warn("[monitor] Unreadable avatar config %s: %v", filepath.Base(path), err)
		return
	}
	t := float64(time.Now().UnixMilli()) / 1000
	added := 0
	m.mu.Lock()
	for _, p := range cfg.Parameters {
		addr, typ := "", ""
		if p.Output != nil {
			addr, typ = p.Output.Address, p.Output.Type
		} else if p.Input != nil {
			addr, typ = p.Input.Address, p.Input.Type
		}
		if addr == "" || !m.matches(addr) {
			continue
		}
		e := m.entries[addr]
		if e == nil {
			if len(m.entries) >= monitorEntriesMax {
				m.evictLocked()
			}
			e = &monEntry{Address: addr, Category: monCategory(addr), FirstSeen: t, Type: strings.ToLower(typ)}
			m.entries[addr] = e
			added++
		}
		e.Declared, e.Avatar = true, cfg.Name
	}
	if added > 0 {
		m.dirty = true
	}
	m.mu.Unlock()
	logx.Info("[monitor] Avatar %q: %d parameters listed, %d new", cfg.Name, len(cfg.Parameters), added)
}
