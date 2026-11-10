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

			// OSC router: forward everything VRChat sends us to other apps
			// ([{host, port, enabled}]), so they keep working although only
			// one program can listen on VRChat's output port.
			"forward": []any{},
			// Where we send OSC to VRChat (chatbox, OGB heartbeat).
			"send_host": "127.0.0.1",
			"send_port": 9000,
		},
		"web_server": M{
			"listen_host": "0.0.0.0",
			"listen_port": 8800,
			// Extra Host names accepted besides IPs, localhost and this PC's
			// name (DNS-rebinding protection), e.g. a custom LAN DNS name.
			"allowed_hosts": []any{},
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
			// Last UI language, for native strings (tray menu).
			"ui_locale": "zh",
			// Last effective UI theme ("dark"/"light"): window background
			// and title bar colour before the page has loaded.
			"ui_theme": "dark",
		},
		"chatbox": M{
			"enabled":           false,
			"interval_seconds":  3.0,
			"send_notification": true,
			"trigger_sfx":       false,
			"message_template":  "Shocking VRChat ⚡ A:{strength_a} B:{strength_b}",
		},
		// Optional Bluetooth heart rate (see internal/app/ble_hr.go).
		"ble_hr": M{
			"enabled":        false,
			"device":         "", // address; "" = first heart-rate device found
			"device_name":    "",
			"min_bpm":        40.0,
			"max_bpm":        200.0,
			"send_to_vrchat": true,
		},
		// Optional HypeRate heart rate (see internal/app/hyperate.go).
		"hyperate": M{
			"enabled":        false,
			"session_id":     "",
			"api_key":        "",
			"min_bpm":        40.0,
			"max_bpm":        200.0,
			"send_to_vrchat": true,
		},
		// Optional Pulsoid heart rate (see internal/app/pulsoid.go).
		"pulsoid": M{
			"enabled":        false,
			"token":          "",
			"min_bpm":        40.0,
			"max_bpm":        200.0,
			"send_to_vrchat": true,
		},
		// Parameter monitor: records which parameters appeared and how
		// they changed (see internal/app/monitor.go).
		"monitor": M{
			"enabled":  false, // opt-in (Settings page)
			"prefixes": []any{"/avatar/parameters/pcs/", "/avatar/parameters/OGB/"},
		},
		"sps": M{
			"enabled":               true,
			"ogb_enabled_heartbeat": true,
			"ogb_enabled_interval":  5.0,
		},
	}
}

// RangesKeys are the extra avatar_params fields of a "ranges" parameter.
var RangesKeys = []string{"ranges", "crossfade", "blend", "crossfade_curve", "remap"}

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
			e := M{"path": Str(t["path"], ""), "mode": mode, "enabled": enabled}
			for _, k := range RangesKeys { // settings of "ranges" (multi-mode) params
				if v, ok := t[k]; ok {
					e[k] = DeepCopy(v)
				}
			}
			out = append(out, e)
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
		// overlimit_max (removed in 0.7.7, superseded by overlimit rule
		// boosts) is dropped from both trees so the next save cleans it up.
		delete(a, "overlimit_max")
		delete(b, "overlimit_max")
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
	// fileMu serializes file I/O: concurrent Saves must not interleave
	// (an older snapshot overwriting a newer one), and the hot-reload
	// check must not read the advanced file written but the basic file
	// not yet written by a Save in progress.
	fileMu sync.Mutex
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
	MigrateOSCSendTarget(advRaw)
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
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync() // a crash/power loss must not leave an empty config
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
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
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
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

// LoadIfChanged reloads both files if either changed on disk since the
// last Save/snapshot (an external edit). The mtimes are re-snapshotted
// even on error so a broken file is reported once, not every poll.
func (s *Store) LoadIfChanged() (adv, basic M, changed bool, err error) {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if !s.Changed() {
		return nil, nil, false, nil
	}
	adv, basic, err = s.LoadFiles()
	s.UpdateMtimes()
	return adv, basic, true, err
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

// MigrateOSCSendTarget moves the per-feature OSC send targets (chatbox
// target_host/port, sps ogb_target_host/port; removed in 0.7.17) into the
// shared osc.send_host/send_port. A non-default old target wins.
func MigrateOSCSendTarget(adv M) {
	if adv == nil {
		return
	}
	o := Sub(adv, true, "osc")
	_, hasHost := o["send_host"]
	_, hasPort := o["send_port"]
	ch, sp := AsMap(adv["chatbox"]), AsMap(adv["sps"])
	if !hasHost || !hasPort {
		// first non-default old target wins, else the first one present
		host, port := "", 0
		for _, src := range []struct {
			m      M
			hk, pk string
		}{{ch, "target_host", "target_port"}, {sp, "ogb_target_host", "ogb_target_port"}} {
			h, p := Str(src.m[src.hk], ""), Int(src.m[src.pk], 0)
			if h == "" && p == 0 {
				continue
			}
			nonDefault := (h != "" && h != "127.0.0.1") || (p != 0 && p != 9000)
			if host == "" || nonDefault {
				host, port = h, p
			}
			if nonDefault {
				break
			}
		}
		if !hasHost && host != "" {
			o["send_host"] = host
		}
		if !hasPort && port > 0 {
			o["send_port"] = port
		}
	}
	for _, m := range []M{ch, sp} {
		if m != nil {
			delete(m, "target_host")
			delete(m, "target_port")
			delete(m, "ogb_target_host")
			delete(m, "ogb_target_port")
		}
	}
}
