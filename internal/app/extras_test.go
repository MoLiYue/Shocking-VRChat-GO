package app

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"shockingvrc/internal/config"
	"shockingvrc/internal/osc"
)

func udpListener(t *testing.T) (*net.UDPConn, int) {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, c.LocalAddr().(*net.UDPAddr).Port
}

// readOSC collects messages until addr arrives (or the deadline).
func readOSC(t *testing.T, c *net.UDPConn, want string, d time.Duration) []osc.Message {
	t.Helper()
	var all []osc.Message
	buf := make([]byte, 2048)
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(deadline)
		n, _, err := c.ReadFrom(buf)
		if err != nil {
			break
		}
		msgs, _ := osc.Parse(buf[:n])
		all = append(all, msgs...)
		for _, m := range msgs {
			if m.Address == want {
				return all
			}
		}
	}
	t.Fatalf("did not receive %s (got %d messages)", want, len(all))
	return nil
}

// OSC router: what VRChat sends us is forwarded to other apps; our own
// listen port is never a target (loop).
func TestOSCForwarding(t *testing.T) {
	e := newEnv(t)
	tgt, port := udpListener(t)
	r := e.postJSON(t, "/api/v1/settings", map[string]any{"osc": map[string]any{"forward": []any{
		map[string]any{"host": "127.0.0.1", "port": port},
		map[string]any{"host": "127.0.0.1", "port": e.oscPort}, // own port: skipped
		map[string]any{"host": "127.0.0.1", "port": 9, "enabled": false},
	}}})
	if r["success"] != true || len(config.AsList(r["restart_needed"])) != 0 {
		t.Fatalf("settings %v", r)
	}
	if got := e.a.forwardTargets(); len(got) != 1 || got[0] != "127.0.0.1:"+strconv.Itoa(port) {
		t.Fatalf("targets %v", got)
	}
	c, _ := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(e.oscPort))
	defer c.Close()
	b, _ := osc.Encode("/avatar/parameters/Anything", float32(0.3))
	_, _ = c.Write(b)
	readOSC(t, tgt, "/avatar/parameters/Anything", 2*time.Second)
	if r := e.postJSON(t, "/api/v1/settings", map[string]any{"osc": map[string]any{"forward": []any{map[string]any{"port": 70000}}}}); r["_status"] != 400 {
		t.Fatalf("bad port accepted %v", r)
	}
}

func fakePulsoid(t *testing.T, status int, bpm int) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if r.URL.Query().Get("access_token") != "secret-token-1234" {
			w.WriteHeader(401)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for i := 0; i < 50; i++ {
			b, _ := json.Marshal(map[string]any{"measured_at": time.Now().UnixMilli(), "data": map[string]any{"heart_rate": bpm}})
			if c.WriteMessage(websocket.TextMessage, b) != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPulsoid(t *testing.T) {
	srv := fakePulsoid(t, 0, 120)
	old := pulsoidURL
	pulsoidURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	defer func() { pulsoidURL = old }()
	e := newEnv(t)
	vr, vrPort := udpListener(t)
	e.postJSON(t, "/api/v1/settings", map[string]any{"osc": map[string]any{"send_port": vrPort}})
	r := e.postJSON(t, "/api/v1/pulsoid", map[string]any{"enabled": true, "token": "secret-token-1234", "min_bpm": 60, "max_bpm": 180})
	t.Cleanup(e.a.stopPulsoid)
	if r["has_token"] != true || r["token"] != nil || r["token_hint"] != "…1234" {
		t.Fatalf("token must never be returned: %v", r)
	}
	// sent to VRChat with VRCOSC-compatible names
	msgs := readOSC(t, vr, vrcoscHRPrefix+"Value", 3*time.Second)
	var v any
	for _, m := range msgs {
		if m.Address == vrcoscHRPrefix+"Value" {
			v = m.Args[0]
		}
	}
	if config.Int(v, 0) != 120 {
		t.Fatalf("VRCOSC value %v", v)
	}
	// injected as virtual parameters inside the program
	deadline := time.Now().Add(2 * time.Second)
	for {
		e.a.overlimitMu.Lock()
		bpm, norm := e.a.oscParamState[pulsoidParamPrefix+"BPM"], e.a.oscParamState[pulsoidParamPrefix+"HeartRate"]
		e.a.overlimitMu.Unlock()
		if config.Int(bpm, 0) == 120 && config.Float(norm, -1) == 0.5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("virtual params bpm=%v norm=%v", bpm, norm)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if g := e.getJSON(t, "/api/v1/pulsoid"); g["connected"] != true || config.Int(g["bpm"], 0) != 120 {
		t.Fatalf("status %v", g)
	}
	// the token never leaves through the config APIs
	cfg := e.getJSON(t, "/api/v1/config")
	resp, _ := http.Get(e.web.URL + "/api/v1/config/export")
	exp, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(exp), "secret-token") || config.Str(config.Get(cfg, "advanced", "pulsoid", "token"), "") != "" {
		t.Fatal("token leaked through config/export")
	}
}

func TestPulsoidBadToken(t *testing.T) {
	srv := fakePulsoid(t, 401, 0)
	old := pulsoidURL
	pulsoidURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	defer func() { pulsoidURL = old }()
	e := newEnv(t)
	e.postJSON(t, "/api/v1/pulsoid", map[string]any{"enabled": true, "token": "wrong-token"})
	t.Cleanup(e.a.stopPulsoid)
	deadline := time.Now().Add(3 * time.Second)
	for {
		g := e.getJSON(t, "/api/v1/pulsoid")
		if es := config.Str(g["error"], ""); es != "" {
			if !strings.Contains(es, "401") || strings.Contains(es, "wrong-token") {
				t.Fatalf("error %q", es)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no error reported")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSteamVRAutostart(t *testing.T) {
	steam := t.TempDir()
	old := steamDir
	steamDir = func() string { return steam }
	defer func() { steamDir = old }()
	appcfg := filepath.Join(steam, "config", "appconfig.json")
	_ = os.MkdirAll(filepath.Dir(appcfg), 0o755)
	_ = os.WriteFile(appcfg, []byte(`{"manifest_paths":["C:\\Other\\app.vrmanifest"],"other":1}`), 0o644)
	e := newEnv(t)
	r := e.postJSON(t, "/api/v1/steamvr", map[string]any{"enabled": true})
	if r["registered"] != true || r["autolaunch"] != true || r["enabled"] != true {
		t.Fatalf("enable %v", r)
	}
	ac, _ := readJSONFile(appcfg)
	if l := config.AsList(ac["manifest_paths"]); len(l) != 2 || config.Int(ac["other"], 0) != 1 {
		t.Fatalf("appconfig %v", ac)
	}
	m, _ := readJSONFile(e.a.steamVRManifestPath())
	app := config.AsMap(config.AsList(m["applications"])[0])
	if app["app_key"] != steamVRAppKey || app["is_dashboard_overlay"] != true || config.Str(app["binary_path_windows"], "") == "" {
		t.Fatalf("manifest %v", m)
	}
	// idempotent, then off
	e.postJSON(t, "/api/v1/steamvr", map[string]any{"enabled": true})
	if l := config.AsList(func() map[string]any { a, _ := readJSONFile(appcfg); return a }()["manifest_paths"]); len(l) != 2 {
		t.Fatalf("registered twice: %v", l)
	}
	r = e.postJSON(t, "/api/v1/steamvr", map[string]any{"enabled": false})
	if r["registered"] != false || r["autolaunch"] != false {
		t.Fatalf("disable %v", r)
	}
	// a file we cannot parse is never overwritten
	_ = os.WriteFile(appcfg, []byte(`{broken`), 0o644)
	if r := e.postJSON(t, "/api/v1/steamvr", map[string]any{"enabled": true}); r["_status"] == 200 {
		t.Fatalf("broken appconfig: %v", r)
	}
	if b, _ := os.ReadFile(appcfg); string(b) != `{broken` {
		t.Fatal("broken appconfig was overwritten")
	}
}
