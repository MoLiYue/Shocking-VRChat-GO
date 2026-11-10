package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestMergeDefaults(t *testing.T) {
	def := M{"a": 1, "b": M{"c": 2, "d": 3}}
	cur := M{"b": M{"c": 9}, "x": "y"}
	got := AsMap(MergeDefaults(def, cur))
	if got["a"] != 1 || got["x"] != "y" || Get(got, "b", "c") != 9 || Get(got, "b", "d") != 3 {
		t.Fatalf("%v", got)
	}
	// defaults must not be aliased
	Sub(got, false, "b")["d"] = 100
	if Get(def, "b", "d") != 3 {
		t.Fatal("aliasing")
	}
}

func TestNormalizeAvatarParams(t *testing.T) {
	ch := M{"mode": "shock", "avatar_params": []any{"/a", M{"path": "/b", "enabled": false}}}
	NormalizeAvatarParams(ch)
	l := AsList(ch["avatar_params"])
	if AsMap(l[0])["mode"] != "shock" || AsMap(l[0])["enabled"] != true || AsMap(l[1])["enabled"] != false {
		t.Fatalf("%v", l)
	}
}

func TestStoreInitSaveReload(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Init(); err != ErrInited {
		t.Fatalf("first init: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("second init: %v", err)
	}
	if Str(s.Get("ws", "master_uuid"), "") == "" {
		t.Fatal("master_uuid not generated")
	}
	if Int(s.Get("dglab3", "channel_a", "strength_limit"), 0) != 5 {
		t.Fatal("basic not synced into advanced")
	}
	if _, _, changed, _ := s.LoadIfChanged(); changed {
		t.Fatal("unexpected change")
	}
	time.Sleep(20 * time.Millisecond)
	b, _ := os.ReadFile(filepath.Join(dir, FileBasic))
	if err := os.WriteFile(filepath.Join(dir, FileBasic), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(dir, FileBasic), time.Now().Add(time.Second), time.Now().Add(time.Second))
	if !s.Changed() {
		t.Fatal("change not detected")
	}
}

func TestVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, FileBasic), []byte("version: v0.1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, FileAdvanced), []byte("version: v0.2\n"), 0o644)
	if err := NewStore(dir).Init(); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestCoercion(t *testing.T) {
	if Int("12", 0) != 12 || Int(3.9, 0) != 3 || Int("x", 7) != 7 {
		t.Fatal("Int")
	}
	if Float("0.5", 0) != 0.5 || Float(nil, 2) != 2 {
		t.Fatal("Float")
	}
	if !Equal(M{"a": 1}, M{"a": 1.0}) {
		t.Fatal("Equal numeric")
	}
}

// Concurrent Write+Save calls must leave the files equal to the final
// in-memory state, and our own saves must not look like external edits.
func TestConcurrentSave(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Init(); err != nil && err != ErrInited {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Write(func(adv, _ M) { Sub(adv, true, "general")["test_counter"] = i })
			if err := s.Save(); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if _, _, changed, _ := s.LoadIfChanged(); changed {
		t.Fatal("own saves reported as external change")
	}
	adv, _, err := s.LoadFiles()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Get(adv, "general", "test_counter"), s.Get("general", "test_counter"); !Equal(got, want) {
		t.Fatalf("file has %v, memory has %v", got, want)
	}
}

// overlimit_max was removed in 0.7.7; old files are cleaned on load.
func TestOverlimitMaxDropped(t *testing.T) {
	adv := M{"dglab3": M{"channel_a": M{"overlimit_max": 20}}}
	basic := M{"dglab3": M{"channel_a": M{"overlimit_max": 20, "strength_limit": 5}}}
	SyncBasicIntoAdvanced(adv, basic)
	if Get(adv, "dglab3", "channel_a", "overlimit_max") != nil || Get(basic, "dglab3", "channel_a", "overlimit_max") != nil {
		t.Fatal("overlimit_max not removed")
	}
}

// The per-feature OSC send targets became one shared osc.send_host/port.
func TestMigrateOSCSendTarget(t *testing.T) {
	adv := M{"chatbox": M{"target_host": "127.0.0.1", "target_port": 9000, "enabled": true},
		"sps": M{"ogb_target_host": "192.168.1.9", "ogb_target_port": 9100}}
	MigrateOSCSendTarget(adv)
	if Get(adv, "osc", "send_host") != "192.168.1.9" || Int(Get(adv, "osc", "send_port"), 0) != 9100 {
		t.Fatalf("non-default target should win: %v", adv["osc"])
	}
	if Get(adv, "chatbox", "target_host") != nil || Get(adv, "sps", "ogb_target_port") != nil || Get(adv, "chatbox", "enabled") != true {
		t.Fatalf("old keys not removed / others touched: %v", adv)
	}
	keep := M{"osc": M{"send_host": "10.0.0.2", "send_port": 9002}, "chatbox": M{"target_host": "1.2.3.4", "target_port": 1}}
	MigrateOSCSendTarget(keep)
	if Get(keep, "osc", "send_host") != "10.0.0.2" {
		t.Fatal("existing shared target must not be overwritten")
	}
}
