package app

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
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
	"shockingvrc/internal/handler"
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
	if config.Int(r["channel_a"], 0) != 200 || r["overlimit_b"] != nil {
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
		"name": "r", "channel": "a", "limit_boost": 40, "condition": map[string]any{"param": "/p", "operator": ">", "value": 0.5}}}})
	e.a.overlimitOSCHook("/p", []any{0.9})
	e.a.overlimitTick(time.Now())
	o := e.getJSON(t, "/api/v1/overlimit_rules")
	if config.Int(config.Get(o, "effective", "A"), 0) != 40 {
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

func TestStrengthAPIRespectsLimitAndGuard(t *testing.T) {
	e := newEnv(t) // limits are 50
	app := dialMockApp(t, e)
	master := config.Str(e.a.Cfg.Get("ws", "master_uuid"), "")
	devID := app.waitFor(func(v map[string]string) bool { return v["type"] == "bind" }, 2*time.Second)["clientId"]
	app.send(map[string]string{"type": "bind", "clientId": master, "targetId": devID, "message": "DGLAB"})
	app.send(map[string]string{"type": "msg", "clientId": master, "targetId": devID, "message": "strength-0+0+200+200"})
	time.Sleep(100 * time.Millisecond)
	e.getJSON(t, "/api/v1/strength/A/set/200")
	got := app.waitFor(func(v map[string]string) bool { return strings.HasPrefix(v["message"], "strength-1+2+") }, 2*time.Second)
	if got["message"] != "strength-1+2+50" {
		t.Fatalf("set must clamp to limit, got %v", got)
	}

	// A web page cannot trigger output: cross-site request is rejected.
	req, _ := http.NewRequest("GET", e.web.URL+"/api/v1/shock/all/5", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("cross-site shock got %d", resp.StatusCode)
	}
	// Live websocket from another origin is refused too.
	h := http.Header{"Origin": []string{"https://evil.example"}}
	if _, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(e.web.URL, "http")+"/ws/live", h); err == nil {
		t.Fatal("cross-origin /ws/live accepted")
	}
	// ...and the DG-LAB socket server only accepts the secret master_uuid
	// path, so a web page (or DNS rebinding) cannot connect to it.
	r2, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(e.wsPort)+"/x", nil)
	r2.Host = "evil.example"
	resp, err = http.DefaultClient.Do(r2)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("wrong path on ws port got %d", resp.StatusCode)
	}
	if _, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+strconv.Itoa(e.wsPort)+"/"+master+"x", h); err == nil {
		t.Fatal("ws port accepted a wrong uuid")
	}
}

func TestUILocale(t *testing.T) {
	e := newEnv(t)
	if e.a.UILocale() != "zh" {
		t.Fatal("default locale")
	}
	e.postJSON(t, "/api/v1/ui/locale", map[string]any{"locale": "en"})
	if e.a.UILocale() != "en" {
		t.Fatal("locale not saved")
	}
}

func TestUpdateScript(t *testing.T) {
	s := updateScript(1234, `C:\SV 中文`, `C:\SV 中文\shocking_vrchat.exe`, `C:\T\x\pkg`, `C:\T`)
	for _, want := range []string{
		`tasklist /FI "PID eq 1234" /NH 2>nul | find /I "shocking_vrchat.exe"`,
		"/R:10 /W:1",           // no endless robocopy retry
		`"settings-v0.2.yaml"`, // config never overwritten
		`/XC /XN /XO`,          // presets: add only
		`start "" "C:\SV 中文\shocking_vrchat.exe"`,
		`rmdir /s /q "C:\T"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q", want)
		}
	}
	if strings.Contains(s, "timeout ") {
		t.Error("timeout needs an interactive console; use ping for delays")
	}
	if strings.Contains(s, "pause") {
		t.Error("hidden updater must never pause")
	}
	if !strings.Contains(s, "\r\n") {
		t.Error("batch file needs CRLF")
	}
}

func TestReleasePageFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/MoLiYue/Shocking-VRChat-GO/releases/tag/v9.9.9", http.StatusFound)
	}))
	defer srv.Close()
	a := New(t.TempDir())
	a.Cfg.Write(func(adv, _ config.M) { config.Sub(adv, true, "general")["github_mirror"] = srv.URL })
	tag, err := a.latestTagFromPage()
	if err != nil || tag != "v9.9.9" {
		t.Fatalf("tag=%q err=%v", tag, err)
	}
	r, err := a.checkViaReleasePage()
	if err != nil || r["update_available"] != true ||
		r["download_url"] != "https://github.com/"+GitHubRepo+"/releases/download/v9.9.9/shocking_vrchat_go_windows_x64.zip" {
		t.Fatal(r, err)
	}
}

func TestUITheme(t *testing.T) {
	e := newEnv(t)
	var got []bool
	e.a.OnTheme = func(d bool) { got = append(got, d) }
	if !e.a.UIThemeDark() {
		t.Fatal("default should be dark")
	}
	e.postJSON(t, "/api/v1/ui/theme", map[string]any{"theme": "light"})
	if e.a.UIThemeDark() || len(got) != 1 || got[0] {
		t.Fatal(got)
	}
}

func TestParamsValidation(t *testing.T) {
	e := newEnv(t)
	r := e.postJSON(t, "/api/v1/params", map[string]any{
		"a": map[string]any{"params": []any{
			map[string]any{"path": "Shock/X", "mode": "distance"},
			map[string]any{"path": "/avatar/parameters/Shock/X", "mode": "touch"}, // duplicate after prefixing
			map[string]any{"path": "", "mode": "distance"},
			map[string]any{"path": "Bad Path", "mode": "distance"},
			map[string]any{"path": "Shock/Y", "mode": "nope"},
		}},
	})
	if r["_status"] != 400 {
		t.Fatal(r)
	}
	codes := map[int]string{}
	for _, x := range config.AsList(r["errors"]) {
		m := config.AsMap(x)
		codes[config.Int(m["index"], -1)] = config.Str(m["code"], "")
	}
	want := map[int]string{1: "duplicate", 2: "empty_path", 3: "bad_path", 4: "bad_mode"}
	for i, c := range want {
		if codes[i] != c {
			t.Errorf("row %d: got %q want %q (all %v)", i, codes[i], c, codes)
		}
	}
	// nothing was saved
	if l := config.AsList(e.a.Cfg.GetBasic("dglab3", "channel_a", "avatar_params")); len(l) != 4 {
		t.Fatalf("config changed on validation error: %v", l)
	}
}

func TestParamsSaveAllAppliesLive(t *testing.T) {
	e := newEnv(t)
	before := map[string]*handler.Shock{}
	for _, h := range e.a.Engine.Handlers() {
		before[h.Channel+"/"+h.Mode] = h
	}
	srvBefore := e.a.Engine.oscSrv
	r := e.postJSON(t, "/api/v1/params", map[string]any{
		"a": map[string]any{"params": []any{
			map[string]any{"path": "Shock/TouchAreaA", "mode": "distance"},
			map[string]any{"path": "Shock/NewShock", "mode": "shock"},
		}, "default_mode": "distance"},
		"b": map[string]any{"params": []any{}},
	})
	if r["success"] != true {
		t.Fatal(r)
	}
	if e.a.Engine.oscSrv != srvBefore {
		t.Fatal("OSC server was recreated")
	}
	after := map[string]*handler.Shock{}
	for _, h := range e.a.Engine.Handlers() {
		after[h.Channel+"/"+h.Mode] = h
	}
	if after["A/distance"] == nil || after["A/distance"] != before["A/distance"] {
		t.Fatal("unchanged handler was not reused")
	}
	if after["A/shock"] == nil || after["B/distance"] != nil {
		t.Fatalf("handler set wrong: %v", after)
	}
	// new route works immediately
	c, _ := osc.NewClient("127.0.0.1", e.oscPort)
	defer c.Close()
	_ = c.Send("/avatar/parameters/Shock/NewShock", float32(0.9))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range e.a.activity.list() {
			if ev.Address == "/avatar/parameters/Shock/NewShock" && ev.Mode == "shock" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("new param not routed after live apply")
}

// The DG-LAB APP may send an Origin header or connect via a hostname; the
// browser guard must not block the device endpoints (V3 port and V4 relay).
func TestDeviceEndpointsBypassBrowserGuard(t *testing.T) {
	e := newEnv(t)
	hdr := http.Header{"Origin": {"http://localhost"}, "Sec-Fetch-Site": {"cross-site"}}
	hdr.Set("Host", "my-pc.example")
	uuid := config.Str(e.a.Cfg.Get("ws", "master_uuid"), "")
	v3 := "ws://127.0.0.1:" + strconv.Itoa(e.wsPort) + "/" + uuid
	v4 := "ws" + strings.TrimPrefix(e.web.URL, "http") + "/ws/dglab-v4?tid=" + e.a.V4.ControllerID
	for name, u := range map[string]string{"v3": v3, "v4": v4} {
		c, resp, err := websocket.DefaultDialer.Dial(u, hdr)
		if err != nil {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			t.Fatalf("%s: dial failed (HTTP %d): %v", name, code, err)
		}
		c.Close()
	}
	// The management API on the same web port stays protected.
	req, _ := http.NewRequest("GET", e.web.URL+"/api/v1/status", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site API request: got %d, want 403", resp.StatusCode)
	}
}

// Reverting a boost subtracts only what the boost actually added: when it
// was capped to +0 (strength already at the limit) nothing is subtracted,
// instead of dropping the channel to 0.
func TestBoostRevertSubtractsOnlyApplied(t *testing.T) {
	e := newEnv(t) // limits are 50
	app := dialMockApp(t, e)
	master := config.Str(e.a.Cfg.Get("ws", "master_uuid"), "")
	devID := app.waitFor(func(v map[string]string) bool { return v["type"] == "bind" }, 2*time.Second)["clientId"]
	app.send(map[string]string{"type": "bind", "clientId": master, "targetId": devID, "message": "DGLAB"})
	strengthMsgs := func(d time.Duration) []string {
		var out []string
		deadline := time.After(d)
		for {
			select {
			case v := <-app.msgs:
				if strings.HasPrefix(v["message"], "strength-1+") {
					out = append(out, v["message"])
				}
			case <-deadline:
				return out
			}
		}
	}
	time.Sleep(2200 * time.Millisecond) // let connection_init (strength 1) pass
	app.send(map[string]string{"type": "msg", "clientId": master, "targetId": devID, "message": "strength-50+0+200+200"})
	strengthMsgs(200 * time.Millisecond)

	e.a.Hub.BroadcastBoost("A", true, 30)
	e.a.Hub.BroadcastBoost("A", false, 30)
	if got := strengthMsgs(300 * time.Millisecond); len(got) != 0 {
		t.Fatalf("capped boost must not change strength, got %v", got)
	}
}

// Downloads have no overall deadline (slow mirrors), but a stalled
// connection is aborted after downloadIdle without data.
func TestDownloadIdleTimeout(t *testing.T) {
	old := downloadIdle
	downloadIdle = 300 * time.Millisecond
	defer func() { downloadIdle = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		if r.URL.Path == "/slow" { // slow but steady: total > idle, must succeed
			for i := 0; i < 5; i++ {
				_, _ = w.Write([]byte("chunk"))
				fl.Flush()
				time.Sleep(150 * time.Millisecond)
			}
			return
		}
		_, _ = w.Write([]byte("start")) // then stall
		fl.Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	e := newEnv(t)
	dir := t.TempDir()
	n, err := e.a.download(srv.URL+"/slow", filepath.Join(dir, "a"), 0)
	if err != nil || n != 25 {
		t.Fatalf("slow download: n=%d err=%v", n, err)
	}
	start := time.Now()
	if _, err := e.a.download(srv.URL+"/stall", filepath.Join(dir, "b"), 0); err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("stalled download: err=%v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("stall not detected promptly")
	}
}

// Overlimit rules raise the cap BY limit_boost and decay back linearly over
// decay_seconds; only a new false -> true transition re-arms the boost.
func TestOverlimitBoostDecays(t *testing.T) {
	e := newEnv(t) // strength limits are 50
	e.postJSON(t, "/api/v1/overlimit_rules", map[string]any{"rules": []any{
		map[string]any{"name": "big", "channel": "a", "limit_boost": 30, "decay_seconds": 10,
			"condition": map[string]any{"param": "/p", "operator": "==", "value": 1}},
		map[string]any{"name": "hold", "channel": "b", "limit_boost": 20, "decay_seconds": 0,
			"condition": map[string]any{"param": "/q", "operator": "==", "value": 1}},
	}})
	a := e.a
	boosts := func(at time.Time) map[string]int {
		a.overlimitMu.Lock()
		defer a.overlimitMu.Unlock()
		return a.overlimitBoostsLocked(at)
	}
	a.overlimitOSCHook("/p", []any{1.0})
	a.overlimitOSCHook("/q", []any{1.0})
	a.overlimitMu.Lock()
	t0 := a.overlimitState[0].triggered
	a.overlimitMu.Unlock()
	for _, c := range []struct {
		dt   float64
		a, b int
	}{{0, 30, 20}, {5, 15, 20}, {9.9, 1, 20}, {10, 0, 20}, {60, 0, 20}} {
		got := boosts(t0.Add(time.Duration(c.dt * float64(time.Second))))
		if got["A"] != c.a || got["B"] != c.b {
			t.Fatalf("t=%vs: got %v, want A=%d B=%d", c.dt, got, c.a, c.b)
		}
	}
	// Still true: no re-arm. Hold rule drops as soon as its condition ends.
	a.overlimitOSCHook("/p", []any{1.0})
	a.overlimitOSCHook("/q", []any{0.0})
	if got := boosts(t0.Add(11 * time.Second)); got["A"] != 0 || got["B"] != 0 {
		t.Fatalf("got %v, want no boost", got)
	}
	// false -> true re-arms the full boost.
	a.overlimitOSCHook("/p", []any{0.0})
	a.overlimitOSCHook("/p", []any{1.0})
	if got := boosts(time.Now()); got["A"] != 30 {
		t.Fatalf("re-trigger: got %v", got)
	}
	// The cap applied to devices is limit + boost.
	a.overlimitTick(time.Now())
	if got := config.Int(config.Get(e.getJSON(t, "/api/v1/overlimit_rules"), "effective", "A"), 0); got != 30 {
		t.Fatalf("effective A = %d", got)
	}
}

// Old rules with an absolute limit_value are converted to a boost.
func TestOverlimitMigratesLimitValue(t *testing.T) {
	e := newEnv(t) // strength limits are 50
	path := e.a.overlimitPath()
	if err := config.WriteYAMLAtomic(path, map[string]any{"rules": []any{
		map[string]any{"name": "old", "channel": "a", "limit_value": 80,
			"condition": map[string]any{"param": "/p", "operator": "==", "value": 1}},
	}}); err != nil {
		t.Fatal(err)
	}
	e.a.loadOverlimitRules()
	r := config.AsMap(config.AsList(e.getJSON(t, "/api/v1/overlimit_rules")["rules"])[0])
	if config.Int(r["limit_boost"], -1) != 30 || r["limit_value"] != nil || config.Float(r["decay_seconds"], -1) != defaultOverlimitDecay {
		t.Fatalf("migrated rule: %v", r)
	}
	v, _ := config.ReadYAMLFile(path)
	if r := config.AsMap(config.AsList(config.AsMap(v)["rules"])[0]); config.Int(r["limit_boost"], -1) != 30 {
		t.Fatalf("migration not saved: %v", r)
	}
}

// Boost changes are pushed to the APP: cap rises to limit+boost and
// follows the decay back down.
func TestLimitBoostPushedToDevice(t *testing.T) {
	e := newEnv(t) // limits are 50
	app := dialMockApp(t, e)
	master := config.Str(e.a.Cfg.Get("ws", "master_uuid"), "")
	devID := app.waitFor(func(v map[string]string) bool { return v["type"] == "bind" }, 2*time.Second)["clientId"]
	app.send(map[string]string{"type": "bind", "clientId": master, "targetId": devID, "message": "DGLAB"})
	app.send(map[string]string{"type": "msg", "clientId": master, "targetId": devID, "message": "strength-50+0+200+200"})
	time.Sleep(100 * time.Millisecond)
	isSetA := func(v map[string]string) bool { return strings.HasPrefix(v["message"], "strength-1+2+") }
	e.a.Hub.SetLimitBoost(30, 0)
	if got := app.waitFor(isSetA, 2*time.Second)["message"]; got != "strength-1+2+80" {
		t.Fatalf("boost push: %s", got)
	}
	e.a.Hub.SetLimitBoost(10, 0)
	if got := app.waitFor(isSetA, 2*time.Second)["message"]; got != "strength-1+2+60" {
		t.Fatalf("decay push: %s", got)
	}
	e.a.Hub.SetLimitBoost(0, 0)
	if got := app.waitFor(isSetA, 2*time.Second)["message"]; got != "strength-1+2+50" {
		t.Fatalf("back to normal: %s", got)
	}
}

func TestDecayCurves(t *testing.T) {
	custom := []any{ // hold full for half the time, then drop to 20%, then 0
		map[string]any{"x": 0.5, "y": 1},
		map[string]any{"x": 0.6, "y": 0.2},
	}
	cases := []struct {
		curve any
		t     float64
		want  float64
	}{
		{"linear", 0.25, 0.75},
		{nil, 0.25, 0.75}, // default linear
		{"ease_out", 0.5, 0.25},
		{"ease_in", 0.5, 0.75},
		{"smoothstep", 0.5, 0.5},
		{"hold", 0.99, 1},
		{"exp", 0, 1},
		{"unknown", 0.5, 0.5},
		{custom, 0.3, 1},
		{custom, 0.55, 0.6},
		{custom, 0.8, 0.1}, // (0.6,0.2) -> implicit (1,0)
		{"hold", 1, 0},     // every curve ends at 0
		{custom, 1.5, 0},
		{"ease_in", -1, 1},
	}
	for _, c := range cases {
		if got := decayRemaining(c.curve, c.t); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("decayRemaining(%v, %v) = %v, want %v", c.curve, c.t, got, c.want)
		}
	}
}

// The settings page sends every field on save; unchanged values must not
// restart the engine (drops device connections) or the whole program.
func TestSettingsSaveRestartsOnlyOnChange(t *testing.T) {
	e := newEnv(t)
	cfg := e.getJSON(t, "/api/v1/config")
	adv := config.AsMap(cfg["advanced"])
	body := map[string]any{
		"osc":        config.AsMap(adv["osc"]),
		"ws":         map[string]any{"listen_port": config.Get(adv, "ws", "listen_port"), "v4_enabled": true},
		"web_server": map[string]any{"listen_port": config.Get(adv, "web_server", "listen_port"), "listen_host": config.Get(adv, "web_server", "listen_host")},
		"log_level":  "INFO",
	}
	r := e.postJSON(t, "/api/v1/settings", body)
	if l := config.AsList(r["restart_needed"]); len(l) != 0 {
		t.Fatalf("unchanged settings requested restart: %v", l)
	}
	body["log_level"] = "DEBUG" // not a restart setting either
	if l := config.AsList(e.postJSON(t, "/api/v1/settings", body)["restart_needed"]); len(l) != 0 {
		t.Fatalf("log level change requested restart: %v", l)
	}
}

// Change operators fire once per qualifying change (10 -> 11), not while
// a comparison holds; the condition value is the minimum change.
func TestOverlimitChangeOperators(t *testing.T) {
	e := newEnv(t)
	rule := func(name, op string, v float64) map[string]any {
		return map[string]any{"name": name, "channel": "a", "limit_boost": 10, "decay_seconds": 5,
			"condition": map[string]any{"param": "/c", "operator": op, "value": v}}
	}
	e.postJSON(t, "/api/v1/overlimit_rules", map[string]any{"rules": []any{
		rule("changed", "changed", 0), rule("up2", "increased", 2), rule("down", "decreased", 0),
	}})
	a := e.a
	fired := func() []bool {
		a.overlimitMu.Lock()
		defer a.overlimitMu.Unlock()
		out := make([]bool, len(a.overlimitState))
		for i, st := range a.overlimitState {
			out[i] = !st.triggered.IsZero()
			a.overlimitState[i].triggered = time.Time{} // reset for the next step
		}
		return out
	}
	steps := []struct {
		v    any
		want [3]bool
	}{
		{10.0, [3]bool{false, false, false}}, // first value: nothing to compare
		{10.0, [3]bool{false, false, false}}, // unchanged
		{11.0, [3]bool{true, false, false}},  // +1: changed, but below min 2 for "up2"
		{13.0, [3]bool{true, true, false}},   // +2
		{12.5, [3]bool{true, false, true}},   // decrease
		{true, [3]bool{false, false, true}},  // bool true = 1 (12.5 -> 1)
	}
	steps[5].want[0] = true
	for i, s := range steps {
		a.overlimitOSCHook("/c", []any{s.v})
		a.overlimitOSCHook("/other", []any{1.0}) // other params never fire these rules
		got := fired()
		if len(got) != 3 || got[0] != s.want[0] || got[1] != s.want[1] || got[2] != s.want[2] {
			t.Fatalf("step %d (%v): fired %v, want %v", i, s.v, got, s.want)
		}
	}
	// A change gives the full boost, which then decays (decay 0 is not
	// "hold" for change rules: there is nothing to hold for).
	a.overlimitOSCHook("/c", []any{20.0})
	if b := func() map[string]int {
		a.overlimitMu.Lock()
		defer a.overlimitMu.Unlock()
		return a.overlimitBoostsLocked(time.Now())
	}(); b["A"] != 10 {
		t.Fatalf("boost after change: %v", b)
	}
}

func TestParamsUsed(t *testing.T) {
	e := newEnv(t)
	e.postJSON(t, "/api/v1/overlimit_rules", map[string]any{"rules": []any{map[string]any{
		"name": "r1", "channel": "a", "limit_boost": 10, "decay_seconds": 5,
		"condition": map[string]any{"param": "/avatar/parameters/Shock/TouchAreaA", "operator": "changed", "value": 0}}}})
	e.a.overlimitOSCHook("/avatar/parameters/Shock/TouchAreaA", []any{0.5})
	r := e.getJSON(t, "/api/v1/params/used")
	found := false
	for _, p := range config.AsList(r["params"]) {
		m := config.AsMap(p)
		if m["path"] != "/avatar/parameters/Shock/TouchAreaA" {
			continue
		}
		found = true
		kinds := map[string]bool{}
		for _, s := range config.AsList(m["sources"]) {
			kinds[config.Str(config.AsMap(s)["kind"], "")] = true
		}
		if !kinds["param"] || !kinds["overlimit"] || config.Float(m["last_value"], -1) != 0.5 {
			t.Fatalf("entry: %v", m)
		}
	}
	if !found {
		t.Fatalf("TouchAreaA missing: %v", r)
	}
}
