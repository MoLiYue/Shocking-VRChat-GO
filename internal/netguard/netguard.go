// Package netguard protects the unauthenticated local HTTP/WebSocket servers
// from being driven by web pages the user happens to have open.
//
// Two browser attacks matter here:
//
//   - Cross-site requests: any page can fire GET/POST at 127.0.0.1:8800
//     (e.g. <img src=".../api/v1/shock/all/10">). Browsers mark these with
//     Sec-Fetch-Site / Origin, so we reject cross-site API calls.
//   - DNS rebinding: a page on evil.example re-resolves its own name to
//     127.0.0.1 and becomes "same-origin". Such requests carry an unknown
//     Host header, so only IP literals, localhost and this machine's own
//     name are accepted.
//
// Non-browser clients (DG-LAB APP, curl, VRChat world string loaders) send
// neither Origin nor Sec-Fetch-* headers and are unaffected.
package netguard

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"shockingvrc/internal/logx"
)

// Guard holds the host allow-list.
type Guard struct {
	mu    sync.RWMutex
	extra map[string]bool
	// Name is used in log messages ("web", "ws").
	Name string
}

// New creates a guard; extraHosts are additional accepted Host names.
func New(name string, extraHosts ...string) *Guard {
	g := &Guard{Name: name}
	g.SetExtraHosts(extraHosts)
	return g
}

// SetExtraHosts replaces the user-configured host names.
func (g *Guard) SetExtraHosts(hosts []string) {
	m := map[string]bool{}
	for _, h := range hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			m[h] = true
		}
	}
	g.mu.Lock()
	g.extra = m
	g.mu.Unlock()
}

func hostOnly(hostport string) string {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	return strings.ToLower(strings.Trim(h, "[]."))
}

var machineNames = func() []string {
	n, err := os.Hostname()
	if err != nil || n == "" {
		return nil
	}
	n = strings.ToLower(n)
	short := strings.SplitN(n, ".", 2)[0]
	return []string{n, short, short + ".local", short + ".lan", short + ".home"}
}()

// HostAllowed reports whether the Host header names this machine.
func (g *Guard) HostAllowed(hostport string) bool {
	h := hostOnly(hostport)
	if h == "" {
		return true // HTTP/1.0 without Host: not a browser
	}
	if net.ParseIP(h) != nil || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	for _, n := range machineNames {
		if h == n {
			return true
		}
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.extra[h]
}

// sameOrigin reports whether Origin points at the host being requested.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// CrossSite reports whether r was initiated by a different web site.
func CrossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	}
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		return !sameOrigin(o, r.Host)
	} else if o == "null" {
		return true // sandboxed iframe / file:// page
	}
	return false
}

// isTopLevelPageLoad allows following a link to the UI from another site.
func isTopLevelPageLoad(r *http.Request) bool {
	return r.Method == http.MethodGet && r.Header.Get("Sec-Fetch-Mode") == "navigate" &&
		!strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/ws/")
}

// Wrap returns a handler that rejects rebinding and cross-site requests.
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.HostAllowed(r.Host) {
			logx.Warn("[%s] Rejected request with unknown Host %q (possible DNS rebinding) from %s", g.Name, r.Host, r.RemoteAddr)
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if CrossSite(r) && !isTopLevelPageLoad(r) {
			logx.Warn("[%s] Rejected cross-site %s %s (Origin=%q, Sec-Fetch-Site=%q)", g.Name, r.Method, r.URL.Path,
				r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Site"))
			http.Error(w, "cross-site request blocked", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CheckOrigin is a websocket.Upgrader CheckOrigin that applies the same rules.
func (g *Guard) CheckOrigin(r *http.Request) bool {
	return g.HostAllowed(r.Host) && !CrossSite(r)
}
