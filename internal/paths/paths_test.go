package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMigrate(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "settings-v0.2.yaml"), "basic")
	write(t, filepath.Join(base, "settings-advanced-v0.2.yaml"), "adv")
	write(t, filepath.Join(base, "overlimit_rules.yaml"), "rules")
	write(t, filepath.Join(base, "profiles", "a.yaml"), "pa")
	write(t, filepath.Join(base, "profiles", "b.yaml"), "pb-old")
	write(t, filepath.Join(base, "shocking_vrchat.log"), "log")
	write(t, filepath.Join(base, "shocking_vrchat.2026-11-12_02-40-24.log"), "old log")
	write(t, filepath.Join(base, "crash.log"), "crash")
	write(t, filepath.Join(base, "wave_presets", "x.json"), "{}")
	// Already migrated files win over leftovers.
	write(t, Config(base, "curve_config.yaml"), "new curve")
	write(t, filepath.Join(base, "curve_config.yaml"), "old curve")
	write(t, Config(base, "profiles", "b.yaml"), "pb-new")

	r := Migrate(base)
	if len(r.Errors) > 0 {
		t.Fatal(r.Errors)
	}
	for p, want := range map[string]string{
		Config(base, "settings-v0.2.yaml"):                    "basic",
		Config(base, "settings-advanced-v0.2.yaml"):           "adv",
		Config(base, "overlimit_rules.yaml"):                  "rules",
		Config(base, "curve_config.yaml"):                     "new curve",
		Config(base, "profiles", "a.yaml"):                    "pa",
		Config(base, "profiles", "b.yaml"):                    "pb-new",
		Logs(base, "shocking_vrchat.log"):                     "log",
		Logs(base, "shocking_vrchat.2026-11-12_02-40-24.log"): "old log",
		Logs(base, "crash.log"):                               "crash",
		filepath.Join(base, "wave_presets", "x.json"):         "{}",
		filepath.Join(base, "curve_config.yaml"):              "old curve", // kept, reported
	} {
		if got := read(t, p); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	for _, gone := range []string{"settings-v0.2.yaml", "shocking_vrchat.log", "crash.log", filepath.Join("profiles", "a.yaml")} {
		if _, err := os.Stat(filepath.Join(base, gone)); err == nil {
			t.Errorf("%s still at the old place", gone)
		}
	}
	if len(r.Skipped) != 2 { // curve_config.yaml, profiles/b.yaml
		t.Errorf("skipped %v", r.Skipped)
	}
	// Idempotent.
	if r2 := Migrate(base); len(r2.Moved) != 0 || len(r2.Errors) != 0 {
		t.Fatalf("second run %+v", r2)
	}
}

// Old profiles folder moves as a whole when there is no new one.
func TestMigrateProfilesDir(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "profiles", "a.yaml"), "pa")
	Migrate(base)
	if read(t, Config(base, "profiles", "a.yaml")) != "pa" {
		t.Fatal("not moved")
	}
	if _, err := os.Stat(filepath.Join(base, "profiles")); err == nil {
		t.Fatal("old folder left")
	}
}
