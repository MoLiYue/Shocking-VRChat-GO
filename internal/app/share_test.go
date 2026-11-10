package app

import (
	"archive/zip"
	"bytes"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shockingvrc/internal/config"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func postFile(t *testing.T, e *env, path, field, name string, data []byte, fields map[string]string) (*http.Response, []byte) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if data != nil {
		fw, _ := mw.CreateFormFile(field, name)
		_, _ = fw.Write(data)
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	resp, err := http.Post(e.web.URL+path, mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}

// shareSetup gives channel A a ranges param, a disabled param, a curve and
// an overlimit rule, and sets a strength limit that must never travel.
func shareSetup(t *testing.T, e *env) {
	t.Helper()
	r := e.postJSON(t, "/api/v1/params", map[string]any{"a": map[string]any{"params": []any{
		map[string]any{"path": "pcs/contact/enterPass", "mode": "distance"},
		map[string]any{"path": "Shock/Off", "mode": "shock", "enabled": false},
		map[string]any{"path": "R", "mode": "ranges", "ranges": []any{
			map[string]any{"from": 0, "to": 0.5, "mode": "touch"}, map[string]any{"from": 0.5, "to": 1, "mode": "shock"}}},
	}}})
	if r["success"] != true {
		t.Fatal(r)
	}
	e.postJSON(t, "/api/v1/curve/"+"%2Favatar%2Fparameters%2Fpcs%2Fcontact%2FenterPass", map[string]any{"points": []any{
		map[string]any{"x": 0, "y": 0}, map[string]any{"x": 1, "y": 0.5}}})
	e.postJSON(t, "/api/v1/overlimit_rules", map[string]any{"rules": []any{map[string]any{
		"name": "big", "channel": "a", "limit_boost": 80, "decay_seconds": 5,
		"condition": map[string]any{"param": "/avatar/parameters/pcs/x", "operator": "changed", "value": 0}}}})
}

func TestShareBuildAndCodeRoundTrip(t *testing.T) {
	e := newEnv(t)
	shareSetup(t, e)
	c := e.getJSON(t, "/api/v1/share/code?title=测试")
	code := config.Str(c["code"], "")
	if !strings.HasPrefix(code, shareCodePrefix) {
		t.Fatalf("code %q", code)
	}
	s, kind, err := decodeShareInput([]byte(" " + code + "\n"))
	if err != nil || kind != "code" || s["title"] != "测试" {
		t.Fatalf("%v %v %v", s, kind, err)
	}
	params := config.AsList(config.Get(s, "channels", "a", "params"))
	if len(params) != 2 { // disabled param left out
		t.Fatalf("params %v", params)
	}
	if p := config.AsMap(params[0]); p["path"] != "pcs/contact/enterPass" || p["enabled"] != nil {
		t.Fatalf("param %v (short path, no enabled flag)", p)
	}
	mc := config.AsMap(config.Get(s, "mode_config", "a"))
	if mc["touch"] == nil || mc["shock"] == nil || mc["distance"] == nil || mc["trigger_range"] == nil || mc["combo"] != nil {
		t.Fatalf("mode_config %v", mc)
	}
	if config.AsMap(s["curves"])["pcs/contact/enterPass"] == nil {
		t.Fatalf("curves %v", s["curves"])
	}
	if _, ok := config.AsMap(s["channels"])["strength_limit"]; ok || s["avatar"] != nil {
		t.Fatal("strength limit / avatar must not be shared")
	}
	for _, k := range []string{"ws", "web_server", "osc", "SERVER_IP"} {
		if _, ok := s[k]; ok {
			t.Fatalf("network setting %s shared", k)
		}
	}
}

func TestSharePNGAndSvrc(t *testing.T) {
	e := newEnv(t)
	shareSetup(t, e)
	resp, card := postFile(t, e, "/api/v1/share/png", "image", "card.png", tinyPNG(t), map[string]string{"title": "卡片"})
	if resp.StatusCode != 200 || !bytes.HasPrefix(card, pngSig) {
		t.Fatalf("png export %d", resp.StatusCode)
	}
	if _, err := png.Decode(bytes.NewReader(card)); err != nil {
		t.Fatalf("card is no longer a valid PNG: %v", err)
	}
	s, kind, err := decodeShareInput(card)
	if err != nil || kind != "png" || s["title"] != "卡片" {
		t.Fatalf("png decode %v %v %v", s, kind, err)
	}
	// A re-encoded image (chat apps) has no data: reported as png_stripped.
	resp, b := postFile(t, e, "/api/v1/share/preview", "file", "x.png", tinyPNG(t), nil)
	if resp.StatusCode != 400 || !strings.Contains(string(b), "png_stripped") {
		t.Fatalf("stripped png: %d %s", resp.StatusCode, b)
	}
	resp, svrc := postFile(t, e, "/api/v1/share/svrc", "image", "card.png", tinyPNG(t), nil)
	if resp.StatusCode != 200 || !bytes.HasPrefix(svrc, []byte("PK")) {
		t.Fatalf("svrc export %d", resp.StatusCode)
	}
	if _, kind, err := decodeShareInput(svrc); err != nil || kind != "svrc" {
		t.Fatalf("svrc decode %v %v", kind, err)
	}
}

func TestShareZipSafety(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../../evil.json")
	_, _ = w.Write([]byte(`{"format":"svrc-share"}`))
	_ = zw.Close()
	if _, _, err := decodeShareInput(buf.Bytes()); err == nil {
		t.Fatal("zip without share.json accepted")
	}
	buf.Reset()
	zw = zip.NewWriter(&buf)
	w, _ = zw.Create("share.json")
	_, _ = w.Write([]byte(`{"format":"svrc-share","pad":"` + strings.Repeat("a", shareMaxJSON) + `"}`))
	_ = zw.Close()
	if _, _, err := decodeShareInput(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized share.json: %v", err)
	}
}

// Preview shows the diff without changing anything; apply merges, never
// imports strength limits, imports rules disabled and backs up first.
func TestSharePreviewAndApply(t *testing.T) {
	src := newEnv(t)
	shareSetup(t, src)
	code := config.Str(src.getJSON(t, "/api/v1/share/code?title=x")["code"], "")

	e := newEnv(t) // the receiver: has another param and limit 50
	e.postJSON(t, "/api/v1/params", map[string]any{"a": map[string]any{"params": []any{
		map[string]any{"path": "Mine", "mode": "distance"},
		map[string]any{"path": "pcs/contact/enterPass", "mode": "shock"},
	}}})
	before := e.a.Cfg.GetBasic("dglab3", "channel_a", "strength_limit")
	p := e.postJSON(t, "/api/v1/share/preview", map[string]any{"code": code})
	if p["_status"] != 200 || p["kind"] != "code" {
		t.Fatal(p)
	}
	st := map[string]string{}
	for _, it := range config.AsList(config.Get(p, "channels", "a", "params")) {
		m := config.AsMap(it)
		st[config.Str(m["path"], "")] = config.Str(m["status"], "")
	}
	if st["/avatar/parameters/pcs/contact/enterPass"] != "changed" || st["/avatar/parameters/R"] != "new" {
		t.Fatalf("statuses %v", st)
	}
	if rm := config.AsList(config.Get(p, "channels", "a", "removed_on_replace")); len(rm) != 1 {
		t.Fatalf("removed_on_replace %v", rm)
	}
	if r := config.AsMap(config.AsList(p["overlimit_rules"])[0]); r["high"] != true {
		t.Fatalf("rule not flagged high: %v", r)
	}
	if got := config.AsList(e.a.Cfg.GetBasic("dglab3", "channel_a", "avatar_params")); len(got) != 2 {
		t.Fatal("preview must not change anything")
	}

	res := e.postJSON(t, "/api/v1/share/apply", map[string]any{"share": p["share"], "mode": "merge"})
	if res["success"] != true {
		t.Fatal(res)
	}
	params := config.AsList(e.a.Cfg.GetBasic("dglab3", "channel_a", "avatar_params"))
	paths := []string{}
	for _, x := range params {
		paths = append(paths, config.Str(config.AsMap(x)["path"], ""))
	}
	if len(params) != 3 || paths[0] != "/avatar/parameters/Mine" {
		t.Fatalf("merged params %v", paths)
	}
	if e.a.Cfg.GetBasic("dglab3", "channel_a", "strength_limit") != before {
		t.Fatal("strength limit changed by import")
	}
	e.a.overlimitMu.Lock()
	rule := config.AsMap(e.a.overlimitRules[len(e.a.overlimitRules)-1])
	e.a.overlimitMu.Unlock()
	if rule["enabled"] != false || !strings.HasSuffix(config.Str(rule["name"], ""), "（导入）") {
		t.Fatalf("imported rule %v", rule)
	}
	if pts := e.a.curvePoints("/avatar/parameters/pcs/contact/enterPass"); len(pts) != 2 || pts[1].Y != 0.5 {
		t.Fatalf("curve %v", pts)
	}
	if _, err := os.Stat(filepath.Join(e.a.profilesDir(), config.Str(res["backup"], "")+".yaml")); err != nil {
		t.Fatalf("backup: %v", err)
	}
	// replace: only the shared params remain
	e.postJSON(t, "/api/v1/share/apply", map[string]any{"share": p["share"], "mode": "replace",
		"sections": map[string]any{"overlimit": false}})
	if got := config.AsList(e.a.Cfg.GetBasic("dglab3", "channel_a", "avatar_params")); len(got) != 2 {
		t.Fatalf("replace: %v", got)
	}
	// invalid share content: rejected, nothing changed
	bad := config.AsMap(config.DeepCopy(p["share"]))
	config.AsMap(config.Get(bad, "channels", "a"))["params"] = []any{map[string]any{"path": "x y", "mode": "distance"}}
	if r := e.postJSON(t, "/api/v1/share/apply", map[string]any{"share": bad}); r["_status"] != 400 {
		t.Fatalf("invalid share applied: %v", r)
	}
}
