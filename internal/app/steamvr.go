package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
)

// Start with SteamVR. SteamVR launches applications whose manifest is
// registered (Steam/config/appconfig.json "manifest_paths") and whose
// per-app config (Steam/config/vrappconfig/<app_key>.vrappconfig) has
// "autolaunch": true. VRCOSC sets the same through the OpenVR API; editing
// the files directly needs no openvr_api.dll and works while SteamVR is not
// running. SteamVR may rewrite these files while it runs, so the setting is
// re-applied each time we start.

const steamVRAppKey = "moliyue.shocking_vrchat"

// steamDir finds the Steam install folder ("" if unknown); var for tests.
var steamDir = platformSteamDir

func (a *App) steamVRManifestPath() string { return a.ConfigPath("shocking_vrchat.vrmanifest") }

// legacyManifestPath is where 0.7.21 and older wrote the manifest.
func (a *App) legacyManifestPath() string { return a.Path("shocking_vrchat.vrmanifest") }

func (a *App) writeSteamVRManifest() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.Abs(exe)
	m := map[string]any{
		"source": "builtin",
		"applications": []any{map[string]any{
			"app_key":             steamVRAppKey,
			"launch_type":         "binary",
			"binary_path_windows": exe,
			"arguments":           "",
			// an overlay-type app: SteamVR starts it alongside, it does not
			// replace the running game
			"is_dashboard_overlay": true,
			"strings": map[string]any{
				"en_us": map[string]any{"name": "Shocking VRChat", "description": "VRChat OSC to DG-LAB Coyote"},
			},
		}},
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	p := a.steamVRManifestPath()
	return p, os.WriteFile(p, b, 0o644)
}

func readJSONFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var m map[string]any
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func writeJSONFile(path string, m map[string]any) error {
	b, err := json.MarshalIndent(m, "", "\t")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".svrc.tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func samePath(a, b string) bool {
	n := func(p string) string { return strings.ToLower(filepath.Clean(strings.ReplaceAll(p, "\\", "/"))) }
	return n(a) == n(b)
}

// setSteamVRAutostart registers (on) or unregisters (off) our manifest and
// sets the autolaunch flag.
func (a *App) setSteamVRAutostart(on bool) error {
	steam := steamDir()
	if steam == "" {
		return errors.New("Steam installation not found")
	}
	manifest, err := a.writeSteamVRManifest()
	if err != nil {
		return err
	}
	appcfg := filepath.Join(steam, "config", "appconfig.json")
	ac, err := readJSONFile(appcfg)
	if err != nil {
		return err // never overwrite a file we could not parse
	}
	var paths []any
	found := false
	for _, p := range config.AsList(ac["manifest_paths"]) {
		s := config.Str(p, "")
		if samePath(s, a.legacyManifestPath()) {
			continue // moved into config/ (re-registered below)
		}
		if samePath(s, manifest) {
			found = true
			if !on {
				continue
			}
		}
		paths = append(paths, p)
	}
	if on && !found {
		paths = append(paths, manifest)
	}
	if paths == nil {
		paths = []any{}
	}
	ac["manifest_paths"] = paths
	if err := writeJSONFile(appcfg, ac); err != nil {
		return err
	}
	vc := filepath.Join(steam, "config", "vrappconfig", steamVRAppKey+".vrappconfig")
	v, err := readJSONFile(vc)
	if err != nil {
		v = map[string]any{}
	}
	v["autolaunch"] = on
	if err := writeJSONFile(vc, v); err != nil {
		return err
	}
	logx.Info("[steamvr] Autostart %v (manifest %s)", on, manifest)
	return nil
}

// steamVRStatus reports what is registered right now.
func (a *App) steamVRStatus() map[string]any {
	steam := steamDir()
	out := map[string]any{"supported": steam != "", "enabled": config.Bool(a.Cfg.Get("general", "steamvr_autostart"), false)}
	if steam == "" {
		return out
	}
	ac, _ := readJSONFile(filepath.Join(steam, "config", "appconfig.json"))
	reg := false
	for _, p := range config.AsList(ac["manifest_paths"]) {
		if samePath(config.Str(p, ""), a.steamVRManifestPath()) {
			reg = true
		}
	}
	v, _ := readJSONFile(filepath.Join(steam, "config", "vrappconfig", steamVRAppKey+".vrappconfig"))
	out["registered"] = reg
	out["autolaunch"] = config.Bool(v["autolaunch"], false)
	return out
}

// ApplySteamVRAutostart re-applies the saved setting (called at startup).
func (a *App) ApplySteamVRAutostart() {
	if !config.Bool(a.Cfg.Get("general", "steamvr_autostart"), false) || steamDir() == "" {
		return
	}
	if err := a.setSteamVRAutostart(true); err != nil {
		logx.Warn("[steamvr] Could not register autostart: %v", err)
	}
}

func (a *App) apiSteamVRGet(*http.Request) (any, error) { return a.steamVRStatus(), nil }

func (a *App) apiSteamVRSet(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	on := config.Bool(data["enabled"], false)
	if steamDir() == "" {
		return nil, httpErr(400, "Steam installation not found")
	}
	if err := a.setSteamVRAutostart(on); err != nil {
		return nil, httpErr(500, "%v", err)
	}
	a.Cfg.Write(func(adv, _ config.M) { config.Sub(adv, true, "general")["steamvr_autostart"] = on })
	if err := a.Cfg.Save(); err != nil {
		return nil, err
	}
	return a.steamVRStatus(), nil
}
