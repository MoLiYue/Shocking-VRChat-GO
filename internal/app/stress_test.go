package app

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"shockingvrc/internal/config"
	"shockingvrc/internal/osc"
)

// TestStressDevicesAndOSC runs devices connecting/disconnecting (V3 + V4),
// an OSC flood on every mode, API churn and live sockets concurrently;
// meant for `go test -race` to catch data races and panics.
func TestStressDevicesAndOSC(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}
	e := newEnv(t)
	e.a.Cfg.Write(func(adv, basic config.M) {
		for i, ch := range []string{"channel_a", "channel_b"} {
			ps := []any{}
			for j, m := range []string{"distance", "shock", "touch", "combo", "boost"} {
				ps = append(ps, map[string]any{"path": fmt.Sprintf("/avatar/parameters/s%d_%d", i, j), "mode": m})
			}
			ps = append(ps, map[string]any{"path": fmt.Sprintf("/avatar/parameters/s%d_5", i), "mode": "ranges", "crossfade": 0.4,
				"ranges": []any{map[string]any{"from": 0, "to": 0.4, "mode": "distance"}, map[string]any{"from": 0.4, "to": 0.7, "mode": "touch"},
					map[string]any{"from": 0.7, "to": 1, "mode": "shock"}}})
			config.Sub(basic, true, "dglab3", ch)["avatar_params"] = ps
			sc := config.Sub(adv, true, "dglab3", ch, "mode_config", "shock")
			sc["follow_value"] = true
		}
		config.SyncBasicIntoAdvanced(adv, basic)
	})
	_ = e.a.Cfg.Save()
	e.a.Engine.Reconfigure()
	e.postJSON(t, "/api/v1/overlimit_rules", map[string]any{"rules": []any{map[string]any{
		"name": "r", "channel": "both", "limit_boost": 20, "decay_seconds": 2,
		"condition": map[string]any{"param": "/avatar/parameters/s0_0", "operator": ">", "value": 0.5}}}})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					f()
				}
			}
		}()
	}
	// OSC flood
	conn, _ := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(e.oscPort))
	run(func() {
		addr := fmt.Sprintf("/avatar/parameters/s%d_%d", rand.Intn(2), rand.Intn(6))
		b, _ := osc.Encode(addr, float32(rand.Float64()))
		_, _ = conn.Write(b)
		time.Sleep(200 * time.Microsecond)
	})
	// V3 app connect / bind / report / disconnect
	master := config.Str(e.a.Cfg.Get("ws", "master_uuid"), "")
	run(func() {
		c, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+strconv.Itoa(e.wsPort)+"/"+master, nil)
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			return
		}
		_, b, _ := c.ReadMessage()
		var m map[string]string
		_ = json.Unmarshal(b, &m)
		id := m["clientId"]
		send := func(v map[string]string) { bb, _ := json.Marshal(v); _ = c.WriteMessage(websocket.TextMessage, bb) }
		send(map[string]string{"type": "bind", "clientId": master, "targetId": id, "message": "DGLAB"})
		deadline := time.Now().Add(time.Duration(200+rand.Intn(800)) * time.Millisecond)
		go func() {
			for {
				if _, _, err := c.ReadMessage(); err != nil {
					return
				}
			}
		}()
		for time.Now().Before(deadline) {
			send(map[string]string{"type": "msg", "clientId": master, "targetId": id,
				"message": fmt.Sprintf("strength-%d+%d+200+200", rand.Intn(80), rand.Intn(80))})
			time.Sleep(20 * time.Millisecond)
		}
		c.Close()
	})
	// V4 app
	run(func() {
		u := "ws" + strings.TrimPrefix(e.web.URL, "http") + "/ws/dglab-v4?tid=" + e.a.V4.ControllerID
		c, _, err := websocket.DefaultDialer.Dial(u, nil)
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			return
		}
		go func() {
			for {
				if _, _, err := c.ReadMessage(); err != nil {
					return
				}
			}
		}()
		deadline := time.Now().Add(time.Duration(200+rand.Intn(800)) * time.Millisecond)
		for time.Now().Before(deadline) {
			ev := map[string]any{"type": "message", "data": map[string]any{"t": "ev", "ev": "devices.snapshot", "devices": []any{
				map[string]any{"slotId": "slot-x", "props": map[string]any{"intensityA": rand.Intn(80), "intensityB": 3},
					"slotState": map[string]any{"channelA": map[string]any{"intensityMax": 80}, "channelB": map[string]any{"intensityMax": 80}}}}}}
			if rand.Intn(2) == 0 {
				ev = map[string]any{"type": "message", "data": map[string]any{"t": "ev", "ev": "slots.patch", "slots": []any{
					map[string]any{"props": map[string]any{"intensityA": rand.Intn(80)}}}}}
			}
			b, _ := json.Marshal(ev)
			_ = c.WriteMessage(websocket.TextMessage, b)
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`))
			time.Sleep(20 * time.Millisecond)
		}
		c.Close()
	})
	// API churn
	paths := []string{"/api/v1/status", "/api/v1/strength/A/set/30", "/api/v1/strength/all/add/5", "/api/v1/strength/B/sub/3",
		"/api/v1/overlimit_rules", "/api/v1/sendwave/A/2/0A0A0A0A64646464", "/api/v1/shock/all/1", "/api/v1/strength_boost/all/10/1",
		"/api/v1/mode_config/a/shock", "/api/v1/logs"}
	run(func() {
		p := paths[rand.Intn(len(paths))]
		resp, err := httpClient.Get(e.web.URL + p)
		if err == nil {
			resp.Body.Close()
		}
	})
	run(func() {
		e.postJSON(t, "/api/v1/strength_limit", map[string]any{"channel_a": 40 + rand.Intn(20)})
		e.postJSON(t, "/api/v1/ui/theme", map[string]any{"theme": []string{"dark", "light"}[rand.Intn(2)]})
		time.Sleep(30 * time.Millisecond)
	})
	run(func() {
		time.Sleep(700 * time.Millisecond)
		e.a.Engine.Reconfigure()
	})
	// live ws
	run(func() {
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(e.web.URL, "http")+"/ws/live", nil)
		if err != nil {
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"subscribe":["wave_A","wave_B","osc","status","log"]}`))
		end := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(end) {
			_ = c.SetReadDeadline(end)
			if _, _, err := c.ReadMessage(); err != nil {
				break
			}
		}
		c.Close()
	})
	time.Sleep(4 * time.Second)
	close(stop)
	wg.Wait()
}
