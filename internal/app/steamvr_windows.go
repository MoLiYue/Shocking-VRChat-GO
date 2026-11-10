//go:build windows

package app

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// platformSteamDir reads HKCU\Software\Valve\Steam\SteamPath.
func platformSteamDir() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	p, _, err := k.GetStringValue("SteamPath")
	if err != nil || p == "" {
		return ""
	}
	p = filepath.FromSlash(p)
	if st, err := os.Stat(filepath.Join(p, "config")); err != nil || !st.IsDir() {
		return ""
	}
	return p
}
