// Package paths defines the data folder layout and migrates the old flat
// layout (everything next to the exe, up to 0.7.21) into it:
//
//	<base>/config/   settings, rules, curves, profiles/, window state, ...
//	<base>/logs/     shocking_vrchat*.log, crash.log, error.log, update.log
//	<base>/wave_presets/, dg-lab/, recordings/   unchanged (user content)
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	ConfigDir = "config"
	LogDir    = "logs"
)

// Config returns a path inside the config folder.
func Config(base string, p ...string) string {
	return filepath.Join(append([]string{base, ConfigDir}, p...)...)
}

// Logs returns a path inside the log folder.
func Logs(base string, p ...string) string {
	return filepath.Join(append([]string{base, LogDir}, p...)...)
}

// Files that lived next to the exe before the folders existed.
var (
	legacyConfig = []string{
		"settings-v0.2.yaml", "settings-advanced-v0.2.yaml", "curve_config.yaml",
		"overlimit_rules.yaml", "param_monitor.json", "window_state.json",
		"shocking_vrchat.vrmanifest",
	}
	legacyConfigDirs = []string{"profiles"}
	legacyLogs       = []string{"shocking_vrchat.log", "crash.log", "error.log", "update.log"}
	legacyLogGlobs   = []string{"shocking_vrchat.*.log"}
)

// Result reports what Migrate did, for logging once the log is open.
type Result struct {
	Moved   []string // "old -> new"
	Skipped []string // old file kept: the new location already has one
	Errors  []error
}

// Migrate creates the folders and moves legacy files into them. It never
// overwrites: when both exist, the new location wins and the old file
// stays (reported in Skipped). Safe to run on every start.
func Migrate(base string) Result {
	var r Result
	for _, d := range []string{Config(base), Logs(base)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			r.Errors = append(r.Errors, err)
		}
	}
	move := func(name, dstDir string) {
		src, dst := filepath.Join(base, name), filepath.Join(dstDir, name)
		if _, err := os.Lstat(src); err != nil {
			return
		}
		if _, err := os.Lstat(dst); err == nil {
			r.Skipped = append(r.Skipped, src)
			return
		}
		if err := os.Rename(src, dst); err != nil {
			r.Errors = append(r.Errors, fmt.Errorf("move %s: %w", name, err))
			return
		}
		r.Moved = append(r.Moved, src+" -> "+dst)
	}
	for _, f := range legacyConfig {
		move(f, Config(base))
	}
	for _, d := range legacyConfigDirs {
		src := filepath.Join(base, d)
		if st, err := os.Stat(src); err != nil || !st.IsDir() {
			continue
		}
		if _, err := os.Stat(Config(base, d)); errors.Is(err, os.ErrNotExist) {
			move(d, Config(base))
			continue
		}
		// Both exist: merge file by file, then drop the old folder if empty.
		entries, _ := os.ReadDir(src)
		for _, e := range entries {
			if !e.IsDir() {
				move(filepath.Join(d, e.Name()), Config(base))
			}
		}
		_ = os.Remove(src) // only succeeds when empty
	}
	for _, f := range legacyLogs {
		move(f, Logs(base))
	}
	for _, g := range legacyLogGlobs {
		matches, _ := filepath.Glob(filepath.Join(base, g))
		for _, m := range matches {
			move(filepath.Base(m), Logs(base))
		}
	}
	return r
}
