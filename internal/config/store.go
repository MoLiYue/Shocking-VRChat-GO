package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	FileVersion      = "v0.2"
	FileAdvanced     = "settings-advanced-" + FileVersion + ".yaml"
	FileBasic        = "settings-" + FileVersion + ".yaml"
	DefaultShockWave = `["0A0A0A0A64646464","0A0A0A0A64646464","0A0A0A0A64646464","0A0A0A0A64646464","0A0A0A0A64646464","0A0A0A0A64646464","0A0A0A0A64646464","0A0A0A0A64646464","0A0A0A0A64646464","0A0A0A0A64646464"]`
)

// Channels lists the config channel keys in order.
var Channels = []string{"channel_a", "channel_b"}

// ChannelKey maps "A"/"a" to "channel_a".
func ChannelKey(ch string) string {
	if ch == "B" || ch == "b" {
		return "channel_b"
	}
	return "channel_a"
}

func param(path, mode string) M { return M{"path": path, "mode": mode} }

// DefaultBasic returns SETTINGS_BASIC defaults.
func DefaultBasic() M {
	return M{
		"dglab3": M{
			"channel_a": M{
				"avatar_params": []any{
					param("/avatar/parameters/pcs/contact/enterPass", "distance"),
					param("/avatar/parameters/Shock/TouchAreaA", "distance"),
					param("/avatar/parameters/Shock/TouchAreaC", "distance"),
					param("/avatar/parameters/Shock/wildcard/*", "distance"),
				},
				"mode":           "distance",
				"strength_limit": 5,
				"overlimit_max":  20,
			},
			"channel_b": M{
				"avatar_params": []any{
					param("/avatar/parameters/pcs/contact/enterPass", "distance"),
					param("/avatar/parameters/lms-penis-proximityA*", "distance"),
					param("/avatar/parameters/Shock/TouchAreaB", "distance"),
					param("/avatar/parameters/Shock/TouchAreaC", "distance"),
				},
				"mode":           "distance",
				"strength_limit": 5,
				"overlimit_max":  20,
			},
		},
		"version": FileVersion,
	}
}

// DefaultModeConfig returns the per-channel mode_config defaults.
func DefaultModeConfig() M {
	return M{
		"shock": M{
			"duration":    2,
			"wave":        DefaultShockWave,
			"wave_preset": nil,
			"wave_scale":  1.0,
		},
		"distance": M{
			"freq_ms":     10,
			"wave_preset": nil,
			"wave_scale":  1.0,
		},
		"touch": M{
			"freq_ms":      10,
			"wave_preset":  nil,
			"wave_scale":   1.0,
			"n_derivative": 1,
			"derivative_params": []any{
				M{"bottom": 0.0, "top": 1.0},
				M{"bottom": 0.0, "top": 4.0},
				M{"bottom": 0.0, "top": 40.0},
				M{"bottom": 0.0, "top": 400.0},
			},
		},
		"combo": M{
			"enabled":         false,
			"switch_duration": 0.3,
		},
		"trigger_range": M{
			"bottom": 0.0,
			"top":    0.8,
		},
	}
}

// DefaultAdvanced returns SETTINGS defaults.
func DefaultAdvanced() M {
	return M{
		"SERVER_IP": nil,
		"dglab3": M{
			"channel_a": M{"mode_config": DefaultModeConfig()},
			"channel_b": M{"mode_config": DefaultModeConfig()},
		},
		"ws": M{
			"master_uuid": nil,
			"listen_host": "0.0.0.0",
			"listen_port": 28846,
			"v4_enabled":  true,
		},
		"osc": M{
			"listen_host": "127.0.0.1",
			"listen_port": 9001,
		},
		"web_server": M{
			"listen_host": "0.0.0.0",
			"listen_port": 8800,
		},
		"log_level": "INFO",
		"version":   FileVersion,
		"general": M{
			"auto_open_qr_web_page": true,
			"local_ip_detect": M{
				"host": "223.5.5.5",
				"port": 80,
			},
			"github_mirror": "",
			// Windows: closing the window hides it to the tray instead of exiting.
			"minimize_to_tray": false,
		},
		"chatbox": M{
			"enabled":           false,
			"target_host":       "127.0.0.1",
			"target_port":       9000,
			"interval_seconds":  3.0,
			"send_notification": true,
			"trigger_sfx":       false,
			"message_template":  "Shocking VRChat ⚡ A:{strength_a} B:{strength_b}",
		},
		"sps": M{
			"enabled":               true,
			"ogb_enabled_heartbeat": true,
			"ogb_enabled_interval":  5.0,
			"ogb_target_host":       "127.0.0.1",
			"ogb_target_port":       9000,
		},
	}
}

// NormalizeAvatarParams converts string entries into {path,mode,enabled} dicts.
func NormalizeAvatarParams(ch M) {
	defMode := Str(ch["mode"], "distance")
	out := []any{}
	for _, e := range AsList(ch["avatar_params"]) {
		switch t := e.(type) {
		case string:
			out = append(out, M{"path": t, "mode": defMode, "enabled": true})
		case map[string]any:
			enabled := true
			if v, ok := t["enabled"]; ok {
				enabled = Bool(v, true)
			}
			mode := defMode
			if v, ok := t["mode"]; ok && v != nil {
				mode = Str(v, defMode)
			}
			out = append(out, M{"path": Str(t["path"], ""), "mode": mode, "enabled": enabled})
		}
	}
	ch["avatar_params"] = out
}

// SyncBasicIntoAdvanced copies basic channel fields into the advanced tree.
func SyncBasicIntoAdvanced(adv, basic M) {
	for _, ch := range Channels {
		a := Sub(adv, true, "dglab3", ch)
		b := Sub(basic, true, "dglab3", ch)
		a["avatar_params"] = DeepCopy(b["avatar_params"])
		a["mode"] = b["mode"]
		a["strength_limit"] = b["strength_limit"]
		if v, ok := b["overlimit_max"]; ok {
			a["overlimit_max"] = v
		}
		NormalizeAvatarParams(a)
	}
}

// StripBasic removes the basic-owned fields from a copy of the advanced tree.
func StripBasic(adv M) M {
	ret := CopyMap(adv)
	for _, ch := range Channels {
		if c := Sub(ret, false, "dglab3", ch); c != nil {
			delete(c, "avatar_params")
			delete(c, "mode")
			delete(c, "strength_limit")
		}
	}
	return ret
}

// Store is the process-wide settings holder.
type Store struct {
	mu     sync.RWMutex
	adv    M
	basic  M
	dir    string
	mtimes map[string]time.Time
}

// ErrInited is returned by Init when default files were just created.
var ErrInited = errors.New("config files created")

// NewStore creates a store rooted at dir with default values.
func NewStore(dir string) *Store {
	return &Store{adv: DefaultAdvanced(), basic: DefaultBasic(), dir: dir, mtimes: map[string]time.Time{}}
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// Dir returns the config directory.
func (s *Store) Dir() string { return s.dir }

// Read runs f with read access to the live trees. f must not retain them.
func (s *Store) Read(f func(adv, basic M)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f(s.adv, s.basic)
}

// Write runs f with write access to the live trees.
func (s *Store) Write(f func(adv, basic M)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s.adv, s.basic)
}

// Get returns a deep copy of the advanced value at path.
func (s *Store) Get(path ...string) any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return DeepCopy(Get(s.adv, path...))
}

// GetBasic returns a deep copy of the basic value at path.
func (s *Store) GetBasic(path ...string) any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return DeepCopy(Get(s.basic, path...))
}

// Snapshot returns deep copies of both trees.
func (s *Store) Snapshot() (adv, basic M) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return CopyMap(s.adv), CopyMap(s.basic)
}

// Replace swaps in new trees.
func (s *Store) Replace(adv, basic M) {
	s.mu.Lock()
	s.adv, s.basic = adv, basic
	s.mu.Unlock()
}

// LoadFiles reads both files and returns merged, synced trees (not applied).
func (s *Store) LoadFiles() (adv, basic M, err error) {
	basicRaw, err := readYAML(s.path(FileBasic))
	if err != nil {
		return nil, nil, err
	}
	advRaw, err := readYAML(s.path(FileAdvanced))
	if err != nil {
		return nil, nil, err
	}
	basic = AsMap(MergeDefaults(DefaultBasic(), basicRaw))
	adv = AsMap(MergeDefaults(DefaultAdvanced(), advRaw))
	SyncBasicIntoAdvanced(adv, basic)
	return adv, basic, nil
}

func readYAML(path string) (M, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	m, _ := DeepCopy(v).(map[string]any)
	if m == nil {
		m = M{}
	}
	return m, nil
}

// WriteYAMLAtomic writes v to path through a temp file + rename.
func WriteYAMLAtomic(path string, v any) error {
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadYAMLFile reads any YAML file into a generic tree.
func ReadYAMLFile(path string) (any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return DeepCopy(v), nil
}

// Save persists both trees and records their mtimes so the hot-reload
// watcher does not treat our own write as an external edit.
func (s *Store) Save() error {
	s.mu.RLock()
	adv, basic := CopyMap(s.adv), CopyMap(s.basic)
	s.mu.RUnlock()
	if err := WriteYAMLAtomic(s.path(FileAdvanced), adv); err != nil {
		return err
	}
	if err := WriteYAMLAtomic(s.path(FileBasic), basic); err != nil {
		return err
	}
	s.UpdateMtimes()
	return nil
}

// UpdateMtimes snapshots current file mtimes.
func (s *Store) UpdateMtimes() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range []string{FileAdvanced, FileBasic} {
		if st, err := os.Stat(s.path(n)); err == nil {
			s.mtimes[n] = st.ModTime()
		}
	}
}

// Changed reports whether either config file changed since last snapshot.
func (s *Store) Changed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for n, prev := range s.mtimes {
		st, err := os.Stat(s.path(n))
		if err != nil {
			return true
		}
		if !st.ModTime().Equal(prev) {
			return true
		}
	}
	return false
}

// Init loads the config files, creating defaults on first run (ErrInited).
func (s *Store) Init() error {
	_, e1 := os.Stat(s.path(FileAdvanced))
	_, e2 := os.Stat(s.path(FileBasic))
	if e1 != nil || e2 != nil {
		s.Write(func(adv, basic M) {
			Sub(adv, true, "ws")["master_uuid"] = NewUUID()
			SyncBasicIntoAdvanced(adv, basic)
		})
		if err := s.Save(); err != nil {
			return err
		}
		return ErrInited
	}
	adv, basic, err := s.LoadFiles()
	if err != nil {
		return err
	}
	if Str(adv["version"], "") != FileVersion || Str(basic["version"], "") != FileVersion {
		return fmt.Errorf("config version mismatch: delete %s and %s and restart", FileBasic, FileAdvanced)
	}
	if Get(adv, "ws", "master_uuid") == nil {
		Sub(adv, true, "ws")["master_uuid"] = NewUUID()
	}
	s.Replace(adv, basic)
	s.UpdateMtimes()
	return nil
}

// NewUUID returns a random RFC 4122 v4 UUID string.
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ShortID returns 8 random hex chars (uuid4().hex[:8]).
func ShortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b)
}
