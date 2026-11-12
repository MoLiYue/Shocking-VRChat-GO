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
	e := newEnv(t)
	// 0.7.21 registered the manifest next to the exe; that entry is replaced.
	legacy, _ := json.Marshal(e.a.legacyManifestPath())
	_ = os.WriteFile(appcfg, []byte(`{"manifest_paths":["C:\\Other\\app.vrmanifest",`+string(legacy)+`],"other":1}`), 0o644)
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

func TestParseHRMeasurement(t *testing.T) {
	for _, c := range []struct {
		b    []byte
		bpm  int
		ok   bool
		name string
	}{
		{[]byte{0x00, 72}, 72, true, "uint8"},
		{[]byte{0x01, 0x2C, 0x01}, 300, true, "uint16 LE"},
		{[]byte{0x16, 95, 0x10, 0x03}, 95, true, "uint8 + RR intervals"},
		{[]byte{0x00, 0}, 0, false, "zero"},
		{[]byte{0x01, 0x2C}, 0, false, "short uint16"},
		{[]byte{0x00}, 0, false, "empty"},
	} {
		if bpm, ok := parseHRMeasurement(c.b); bpm != c.bpm || ok != c.ok {
			t.Errorf("%s: got %d %v", c.name, bpm, ok)
		}
	}
}

// Without a Bluetooth adapter the receiver reports an error instead of
// failing; settings are validated.
func TestBLEHRSettings(t *testing.T) {
	e := newEnv(t)
	t.Cleanup(e.a.stopBLEHR)
	if r := e.postJSON(t, "/api/v1/ble_hr", map[string]any{"min_bpm": 150, "max_bpm": 100}); r["_status"] != 400 {
		t.Fatalf("bad range accepted %v", r)
	}
	r := e.postJSON(t, "/api/v1/ble_hr", map[string]any{"enabled": true, "device": "aa:bb:cc:dd:ee:ff", "device_name": "Strap"})
	if r["enabled"] != true || r["device"] != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("set %v", r)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		g := e.getJSON(t, "/api/v1/ble_hr")
		if s := config.Str(g["status"], ""); s == "error" || s == "scanning" {
			break // no adapter here: error; with one: scanning
		}
		if time.Now().After(deadline) {
			t.Fatalf("status %v", g)
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.postJSON(t, "/api/v1/ble_hr", map[string]any{"enabled": false})
	if g := e.getJSON(t, "/api/v1/ble_hr"); g["status"] != "off" {
		t.Fatalf("after disable %v", g["status"])
	}
}

func TestNormaliseHypeRateID(t *testing.T) {
	for in, want := range map[string]string{
		"ABCD": "ABCD", " ab12 ": "ab12", "https://app.hyperate.io/ABCD": "ABCD",
		"app.hyperate.io/ABCD/?x=1": "ABCD", "hyperate.io/ABCD#y": "ABCD", "": "", "a b": "", "x/../": "",
	} {
		if got := normaliseHypeRateID(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// Fake HypeRate (Phoenix channel): join hr:<id>, heartbeat, hr_update.
func TestHypeRate(t *testing.T) {
	up := websocket.Upgrader{}
	joined := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "dev-key-9876" {
			w.WriteHeader(403)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, raw, err := c.ReadMessage()
		if err != nil {
			return
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		joined <- config.Str(m["topic"], "") + "/" + config.Str(m["event"], "")
		_ = c.WriteJSON(map[string]any{"topic": m["topic"], "event": "phx_reply", "payload": map[string]any{"status": "ok"}, "ref": m["ref"]})
		for i := 0; i < 40; i++ {
			if c.WriteJSON(map[string]any{"topic": m["topic"], "event": "hr_update", "payload": map[string]any{"hr": 88}}) != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer srv.Close()
	old := hyperateURL
	hyperateURL = "ws" + strings.TrimPrefix(srv.URL, "http")
	defer func() { hyperateURL = old }()
	e := newEnv(t)
	if r := e.postJSON(t, "/api/v1/hyperate", map[string]any{"session_id": "not valid!"}); r["_status"] != 400 {
		t.Fatalf("invalid id accepted %v", r)
	}
	r := e.postJSON(t, "/api/v1/hyperate", map[string]any{"enabled": true, "session_id": "https://app.hyperate.io/AB12", "api_key": "dev-key-9876"})
	t.Cleanup(e.a.stopHypeRate)
	if r["session_id"] != "AB12" || r["has_key"] != true || r["api_key"] != nil {
		t.Fatalf("set %v", r)
	}
	select {
	case j := <-joined:
		if j != "hr:AB12/phx_join" {
			t.Fatalf("joined %q", j)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no join")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		g := e.getJSON(t, "/api/v1/hyperate")
		e.a.overlimitMu.Lock()
		bpm := e.a.oscParamState[hrParamPrefix+"BPM"]
		e.a.overlimitMu.Unlock()
		if g["connected"] == true && config.Int(g["bpm"], 0) == 88 && config.Int(bpm, 0) == 88 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status %v bpm param %v", g, bpm)
		}
		time.Sleep(30 * time.Millisecond)
	}
	resp, _ := http.Get(e.web.URL + "/api/v1/config/export")
	exp, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(exp), "dev-key-9876") {
		t.Fatal("API key leaked through export")
	}
}
