//go:build !windows

package app

// platformSteamDir: SteamVR autostart is only supported on Windows.
func platformSteamDir() string { return "" }
