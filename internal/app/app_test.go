package app

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
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

func freePort(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type env struct {
	a       *App
	web     *httptest.Server
	wsPort  int
	oscPort int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	// seed presets from the repo so preset APIs have data
	src, _ := filepath.Glob("../../wave_presets/*.json")
	_ = os.MkdirAll(filepath.Join(dir, "wave_presets"), 0o755)
	for _, f := range src[:3] {
		b, _ := os.ReadFile(f)
		_ = os.WriteFile(filepath.Join(dir, "wave_presets", filepath.Base(f)), b, 0o644)
	}
	a := New(dir)
	if err := a.Init(); err != nil {
		t.Fatal(err)
	}
	e := &env{a: a, wsPort: freePort(t, "tcp"), oscPort: freePort(t, "udp")}
	a.Cfg.Write(func(adv, basic config.M) {
		config.Sub(adv, true, "ws")["listen_host"] = "127.0.0.1"
		config.Sub(adv, true, "ws")["listen_port"] = e.wsPort
		config.Sub(adv, true, "osc")["listen_port"] = e.oscPort
		config.Sub(adv, true, "sps")["ogb_enabled_heartbeat"] = false
		for _, ch := range []string{"channel_a", "channel_b"} {
			config.Sub(basic, true, "dglab3", ch)["strength_limit"] = 50
		}
		config.SyncBasicIntoAdvanced(adv, basic)
	})
	_ = a.Cfg.Save()
	a.refreshLimits()
	a.Engine.Start()
	if !a.Engine.Running() {
		t.Fatal("engine failed to start")
	}
	e.web = httptest.NewServer(a.Handler())
	t.Cleanup(func() {
		e.web.Close()
		a.Engine.Stop(false)
	})
	return e
}

func (e *env) getJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	resp, err := http.Get(e.web.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v: %s", path, err, b)
	}
	m["_status"] = resp.StatusCode
	return m
}

func (e *env) postJSON(t *testing.T, path string, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(e.web.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	rb, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(rb, &m)
	if m == nil {
		m = map[string]any{}
	}
	m["_status"] = resp.StatusCode
	return m
}

// mockApp emulates the DG-LAB APP side of the V3 socket protocol.
type mockApp struct {
	t    *testing.T
	c    *websocket.Conn
	msgs chan map[string]string
}

func dialMockApp(t *testing.T, e *env) *mockApp {
	t.Helper()
	uuid := config.Str(e.a.Cfg.Get("ws", "master_uuid"), "")
	c, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+strconv.Itoa(e.wsPort)+"/"+uuid, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := &mockApp{t: t, c: c, msgs: make(chan map[string]string, 1000)}
	go func() {
		for {
			_, b, err := c.ReadMessage()
			if err != nil {
				close(m.msgs)
				return
			}
			var v map[string]string
			_ = json.Unmarshal(b, &v)
			m.msgs <- v
		}
	}()
	t.Cleanup(func() { c.Close() })
	return m
}

func (m *mockApp) send(v map[string]string) {
	b, _ := json.Marshal(v)
	if err := m.c.WriteMessage(websocket.TextMessage, b); err != nil {
		m.t.Fatal(err)
	}
}

// waitFor returns the first message satisfying pred.
func (m *mockApp) waitFor(pred func(map[string]string) bool, d time.Duration) map[string]string {
	m.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case v, ok := <-m.msgs:
			if !ok {
				m.t.Fatal("connection closed")
			}
			if pred(v) {
				return v
			}
		case <-deadline:
			m.t.Fatal("timeout waiting for message")
		}
	}
}

func TestEndToEndV3DeviceAndOSC(t *testing.T) {
	e := newEnv(t)
	app := dialMockApp(t, e)
	master := config.Str(e.a.Cfg.Get("ws", "master_uuid"), "")

	// 1. server announces our device id
	hello := app.waitFor(func(v map[string]string) bool { return v["type"] == "bind" }, 2*time.Second)
	devID := hello["clientId"]
	if hello["message"] != "targetId" || devID == "" {
		t.Fatalf("hello %v", hello)
	}
	// 2. bind with wrong controller is ignored, correct one acknowledged
	app.send(map[string]string{"type": "bind", "clientId": "bogus", "targetId": devID, "message": "DGLAB"})
	app.send(map[string]string{"type": "bind", "clientId": master, "targetId": devID, "message": "DGLAB"})
	ack := app.waitFor(func(v map[string]string) bool { return v["type"] == "bind" }, 2*time.Second)
	if ack["clientId"] != master || ack["message"] != "200" {
		t.Fatalf("bind ack %v", ack)
	}
	// 3. APP reports strength 10/10 with max 100/100 -> server follows limit 50
	app.send(map[string]string{"type": "msg", "clientId": master, "targetId": devID, "message": "strength-10+10+100+100"})
	follow := app.waitFor(func(v map[string]string) bool { return strings.HasPrefix(v["message"], "strength-1+2+") }, 2*time.Second)
	if follow["message"] != "strength-1+2+50" {
		t.Fatalf("follow %v", follow)
	}
	st := e.getJSON(t, "/api/v1/status")
	if devs := st["devices"].([]any); len(devs) != 1 {
		t.Fatalf("status devices %v", st)
	}

	// 4. OSC distance input -> pulse on channel A
	c, err := osc.NewClient("127.0.0.1", e.oscPort)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(30 * time.Millisecond):
				_ = c.Send("/avatar/parameters/Shock/TouchAreaA", float32(0.6))
			}
		}
	}()
	pulse := app.waitFor(func(v map[string]string) bool { return strings.HasPrefix(v["message"], "pulse-A:") }, 3*time.Second)
	close(stop)
	var ops []string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(pulse["message"], "pulse-A:")), &ops); err != nil || len(ops) == 0 {
		t.Fatalf("pulse payload %v", pulse)
	}
	// 5. after input stops, the channel is cleared
	app.waitFor(func(v map[string]string) bool { return v["message"] == "clear-1" }, 3*time.Second)

	// 6. OSC activity + wave history were recorded
	act := e.getJSON(t, "/api/v1/osc_activity")
	if len(act["events"].([]any)) == 0 {
		t.Fatal("no osc activity")
	}

	// 7. API shock goes through the command queue to the device
	e.getJSON(t, "/api/v1/shock/B/0.5")
	app.waitFor(func(v map[string]string) bool { return strings.HasPrefix(v["message"], "pulse-B:") }, 2*time.Second)

	// 8. strength API
	r := e.getJSON(t, "/api/v1/strength/A/set/7")
	if r["result"] != "OK" {
		t.Fatal(r)
	}
	app.waitFor(func(v map[string]string) bool { return v["message"] == "strength-1+2+7" }, 2*time.Second)

	// 9. engine restart keeps device connected
	e.a.Engine.Restart(true)
	if e.a.Hub.Count() != 1 {
		t.Fatal("device dropped on engine restart")
	}
}

func TestV4RelayHandshake(t *testing.T) {
	e := newEnv(t)
	u := "ws" + strings.TrimPrefix(e.web.URL, "http") + "/ws/dglab-v4?tid=" + e.a.V4.ControllerID
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	read := func() map[string]any {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, b, err := c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	if m := read(); m["type"] != "hello" {
		t.Fatal(m)
	}
	if m := read(); m["type"] != "controller_attached" || m["clientId"] != e.a.V4.ControllerID {
		t.Fatal(m)
	}
	// server asks for devices after ~1s
	m := read()
	if config.Get(m, "data", "m") != "devices.get" {
		t.Fatal(m)
	}
	snap := map[string]any{"type": "message", "data": map[string]any{"t": "ev", "ev": "devices.snapshot", "devices": []any{
		map[string]any{"slotId": "slot-x", "props": map[string]any{"intensityA": 0, "intensityB": 0},
			"slotState": map[string]any{"channelA": map[string]any{"intensityMax": 80}, "channelB": map[string]any{"intensityMax": 80}}},
	}}}
	b, _ := json.Marshal(snap)
	_ = c.WriteMessage(websocket.TextMessage, b)
	time.Sleep(100 * time.Millisecond)
	e.getJSON(t, "/api/v1/sendwave/A/2/0A0A0A0A64646464")
	m = read()
	data := config.AsMap(m["data"])
	op := config.AsMap(data["data"])
	if data["m"] != "device.op" || op["s"] != "slot-x" || config.Int(op["t"], -1) != 0 || config.Int(op["d"], 0) != 200 {
		t.Fatalf("pulse op %v", m)
	}
	// wrong tid is rejected
	bad, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(e.web.URL, "http")+"/ws/dglab-v4?tid=nope", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	_, _, _ = bad.ReadMessage()
	_, b2, _ := bad.ReadMessage()
	if !strings.Contains(string(b2), "controller_not_found") {
		t.Fatalf("got %s", b2)
	}
}

func TestSettingsAPIs(t *testing.T) {
	e := newEnv(t)
	// strength limit persist
	r := e.postJSON(t, "/api/v1/strength_limit", map[string]any{"channel_a": 300, "overlimit_b": 33})
	if config.Int(r["channel_a"], 0) != 200 || config.Int(r["overlimit_b"], 0) != 33 {
		t.Fatal(r)
	}
	b, _ := os.ReadFile(filepath.Join(e.a.BaseDir, config.FileBasic))
	if !strings.Contains(string(b), "strength_limit: 200") {
		t.Fatalf("not saved: %s", b)
	}
	// mode config whitelist
	e.postJSON(t, "/api/v1/mode_config/a/touch", map[string]any{"n_derivative": 2, "bogus": 1, "trigger_range": map[string]any{"top": 0.7}})
	g := e.getJSON(t, "/api/v1/mode_config/a/touch")
	if config.Int(config.Get(g, "config", "n_derivative"), 0) != 2 || config.Get(g, "config", "bogus") != nil ||
		config.Float(config.Get(g, "trigger_range", "top"), 0) != 0.7 {
		t.Fatal(g)
	}
	if r := e.getJSON(t, "/api/v1/mode_config/c/touch"); r["_status"] != 400 {
		t.Fatal(r)
	}
	// curves with path keys
	r = e.postJSON(t, "/api/v1/curve/avatar/parameters/x", map[string]any{"points": []any{
		map[string]any{"x": 1, "y": 2}, map[string]any{"x": 0, "y": 0}}})
	if r["key"] != "avatar/parameters/x" {
		t.Fatal(r)
	}
	pts := config.AsList(r["points"])
	if config.Float(config.AsMap(pts[0])["x"], -1) != 0 || config.Float(config.AsMap(pts[1])["y"], -1) != 1 {
		t.Fatal(pts)
	}
	// overlimit rules
	e.postJSON(t, "/api/v1/overlimit_rules", map[string]any{"rules": []any{map[string]any{
		"name": "r", "channel": "a", "limit_value": 90, "condition": map[string]any{"param": "/p", "operator": ">", "value": 0.5}}}})
	e.a.overlimitOSCHook("/p", []any{0.9})
	o := e.getJSON(t, "/api/v1/overlimit_rules")
	if config.Int(config.Get(o, "effective", "A"), 0) != 90 {
		t.Fatal(o)
	}
	// profiles
	req, _ := http.NewRequest("PUT", e.web.URL+"/api/v1/profiles/测试 1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err, resp.StatusCode)
	}
	pl := e.getJSON(t, "/api/v1/profiles")
	if l := config.AsList(pl["profiles"]); len(l) != 1 || l[0] != "测试_1" {
		t.Fatal(pl)
	}
	if r := e.postJSON(t, "/api/v1/profiles/测试_1", nil); r["success"] != true {
		t.Fatal(r)
	}
	// sps validation
	if r := e.postJSON(t, "/api/v1/sps", map[string]any{"ogb_target_port": 70000}); r["_status"] != 400 {
		t.Fatal(r)
	}
	// config export/import round trip
	resp, _ = http.Get(e.web.URL + "/api/v1/config/export")
	exp, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "cfg.json")
	_, _ = fw.Write(exp)
	_ = mw.Close()
	resp, err = http.Post(e.web.URL+"/api/v1/config/import", mw.FormDataContentType(), &body)
	if err != nil || resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("import %v %d %s", err, resp.StatusCode, b)
	}
	resp.Body.Close()
	// unknown api -> 404 JSON, SPA routes -> html
	if r := e.getJSON(t, "/api/v1/nope"); r["_status"] != 404 {
		t.Fatal(r)
	}
	resp, _ = http.Get(e.web.URL + "/mode/touch")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatal(resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPresetImportAndSimulate(t *testing.T) {
	e := newEnv(t)
	pulse := "Dungeonlab+pulse:0,1,1=30,0,10,1,1/0.00-1,50.00-0,100.00-1"
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "my-wave.pulse")
	_, _ = fw.Write([]byte(pulse))
	_ = mw.Close()
	resp, err := http.Post(e.web.URL+"/api/v1/wave_presets/import", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&r)
	resp.Body.Close()
	if r["name"] != "my-wave" || config.Int(r["ops"], 0) == 0 {
		t.Fatal(r)
	}
	names := config.AsList(e.getJSON(t, "/api/v1/wave_presets")["presets"])
	found := false
	for _, n := range names {
		found = found || n == "my-wave"
	}
	if !found {
		t.Fatal(names)
	}
	if p := e.getJSON(t, "/api/v1/wave_presets/my-wave/preview"); len(config.AsList(p["sections"])) != 1 {
		t.Fatal(p)
	}
	sim := e.postJSON(t, "/api/v1/wave_simulate", map[string]any{"mode": "shock", "input_value": 0.4, "params": map[string]any{"duration": 1}})
	if len(config.AsList(sim["samples"])) != 40 || config.Float(sim["effective_strength"], 0) != 0.5 {
		t.Fatal(sim)
	}
}

func TestLiveWebSocketLogs(t *testing.T) {
	e := newEnv(t)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(e.web.URL, "http")+"/ws/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.WriteJSON(map[string]any{"subscribe": []string{"log"}, "ping": true})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, b, err := c.ReadMessage()
	if err != nil || string(b) != `{"pong":true}` {
		t.Fatalf("%v %s", err, b)
	}
	time.Sleep(50 * time.Millisecond)
	e.postJSON(t, "/api/v1/recorder/start", nil)
	for {
		_, b, err = c.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"topic":"log"`) && strings.Contains(string(b), "Started recording") {
			break
		}
	}
	r := e.postJSON(t, "/api/v1/recorder/stop", nil)
	if r["success"] != true {
		t.Fatal(r)
	}
	if l := config.AsList(e.getJSON(t, "/api/v1/recordings")["recordings"]); len(l) != 1 {
		t.Fatal(l)
	}
	if r := e.getJSON(t, "/api/v1/recordings/../x"); r["_status"] == 200 {
		t.Fatal("path traversal not rejected")
	}
}

func TestFormatTemplate(t *testing.T) {
	v := map[string]any{"strength_a": 5, "mode_a": "touch"}
	if s := formatTemplate("A:{strength_a} {mode_a} {{x}}", v); s != "A:5 touch {x}" {
		t.Fatal(s)
	}
	if s := formatTemplate("{nope}", v); !strings.HasPrefix(s, "[template error") {
		t.Fatal(s)
	}
}

func TestVersionNewer(t *testing.T) {
	if !VersionNewer("0.7.0", "0.6.3-go") || VersionNewer("0.6.3", "0.6.3-go") || VersionNewer("x", "1") {
		t.Fatal("VersionNewer")
	}
}

func TestShowWindowAndTraySetting(t *testing.T) {
	e := newEnv(t)
	called := make(chan struct{}, 1)
	e.a.OnShowWindow = func() { called <- struct{}{} }
	e.a.TrayAvailable = true
	if r := e.postJSON(t, "/api/v1/ui/show", nil); r["success"] != true {
		t.Fatal(r)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("OnShowWindow not called")
	}
	if st := e.getJSON(t, "/api/v1/status"); st["tray_available"] != true {
		t.Fatal(st)
	}
	if v := config.Bool(e.a.Cfg.Get("general", "minimize_to_tray"), true); v {
		t.Fatal("minimize_to_tray should default to false")
	}
	e.postJSON(t, "/api/v1/settings", map[string]any{"minimize_to_tray": true})
	if !config.Bool(e.a.Cfg.Get("general", "minimize_to_tray"), false) {
		t.Fatal("minimize_to_tray not saved")
	}
}

func TestParamsSaveKeepsStrengthLimit(t *testing.T) {
	e := newEnv(t)
	e.postJSON(t, "/api/v1/strength_limit", map[string]any{"channel_a": 77})
	// The params page no longer sends strength_limit; it must not reset it.
	e.postJSON(t, "/api/v1/params/a", map[string]any{"params": []any{map[string]any{"path": "/x", "mode": "distance"}}})
	if v := config.Int(e.a.Cfg.GetBasic("dglab3", "channel_a", "strength_limit"), 0); v != 77 {
		t.Fatalf("strength_limit changed to %d", v)
	}
}
