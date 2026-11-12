package device

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/wave"
)

var defaultWave = wave.DefaultWave

// V4 action types.
const (
	ActionAppendPulseData  = 0
	ActionAddIntensity     = 3
	ActionSetTempIntensity = 4
	ActionSetMute          = 5
	ActionSetIntensity     = 7
)

// V4 is a DG-LAB 4 APP connected through our self-hosted V4 relay.
type V4 struct {
	*chanState
	hub         *Hub
	conn        *websocket.Conn
	writeMu     sync.Mutex
	clientID    string
	reqMu       sync.Mutex
	reqCounter  int
	closeOnce   sync.Once
	closed      chan struct{}
	devMu       sync.Mutex
	devices     []map[string]any
	defaultSlot string
}

func (d *V4) ID() string        { return d.clientID }
func (d *V4) state() *chanState { return d.chanState }
func (d *V4) Status() Status    { return d.snapshot(d.clientID) }

func (d *V4) sendJSON(v any) error {
	select {
	case <-d.closed:
		return nil
	default:
	}
	b, _ := json.Marshal(v)
	logx.Debug("[v4] Device %s sending: %s", d.clientID, b)
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_ = d.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := d.conn.WriteMessage(websocket.TextMessage, b); err != nil {
		logx.Warn("[v4] Device %s send error: %v", d.clientID, err)
	}
	return nil
}

func (d *V4) rpc(method string, data map[string]any) string {
	d.reqMu.Lock()
	d.reqCounter++
	id := fmt.Sprintf("req-%d", d.reqCounter)
	d.reqMu.Unlock()
	payload := map[string]any{"t": "req", "reqId": id, "m": method}
	if data != nil {
		payload["data"] = data
	}
	_ = d.sendJSON(map[string]any{"type": "message", "data": payload})
	return id
}

func (d *V4) slot() string {
	d.devMu.Lock()
	defer d.devMu.Unlock()
	if d.defaultSlot == "" {
		return "slot-a"
	}
	return d.defaultSlot
}

func v4Chan(ch string) int {
	if ch == "A" {
		return 0
	}
	return 1
}

// SetStrength implements Device. V4 has no absolute set except to 0, so
// "set" is translated into a relative AddIntensity.
func (d *V4) SetStrength(ch, mode string, value int, force bool) error {
	d.mu.Lock()
	if !force {
		if value < 0 || value > 200 {
			d.mu.Unlock()
			return fmt.Errorf("invalid strength value: %d", value)
		}
		limit := d.upperLocked(ch)
		if value > limit && mode == "2" {
			logx.Warn("[v4] Device %s set_strength: %d over limit %d, clamping", d.clientID, value, limit)
			value = limit
		}
	}
	slot, c := d.slot(), v4Chan(ch)
	d.pushedAt[ch] = time.Now()
	var op map[string]any
	switch mode {
	case "2":
		if value == 0 {
			op = map[string]any{"s": slot, "t": ActionSetIntensity, "c": c, "p": 1, "v": 0}
		} else if delta := value - d.strength[ch]; delta != 0 {
			op = map[string]any{"s": slot, "t": ActionAddIntensity, "c": c, "p": 1, "v": delta}
		}
		d.strength[ch] = value
	case "1":
		op = map[string]any{"s": slot, "t": ActionAddIntensity, "c": c, "p": 1, "v": value}
		d.strength[ch] = min(200, d.strength[ch]+value)
	case "0":
		op = map[string]any{"s": slot, "t": ActionAddIntensity, "c": c, "p": 1, "v": -value}
		d.strength[ch] = max(0, d.strength[ch]-value)
	}
	d.mu.Unlock()
	if op != nil {
		d.rpc("device.op", op)
	}
	logx.Debug("[v4] Set strength: ch=%s mode=%s val=%d", ch, mode, value)
	return nil
}

// SendWave implements Device.
func (d *V4) SendWave(ch, wavestr string) error {
	if wavestr == "" {
		return nil
	}
	ops, err := wave.DecodeOps(wavestr)
	if err != nil {
		logx.Warn("[v4] Invalid wavestr for device %s", d.clientID)
		return nil
	}
	d.rpc("device.op", map[string]any{
		"s": d.slot(), "t": ActionAppendPulseData, "c": v4Chan(ch), "p": 1,
		"d": len(ops) * 100, "v": ops,
	})
	return nil
}

// ClearWave implements Device.
func (d *V4) ClearWave(ch string) error {
	d.rpc("device.op.clear", map[string]any{"s": d.slot(), "c": v4Chan(ch)})
	return nil
}

// Close implements Device.
func (d *V4) Close() {
	d.closeOnce.Do(func() {
		close(d.closed)
		d.hub.remove(d)
		_ = d.conn.Close()
	})
}

func num(v any, def int) int { return config.Int(v, def) }

// logSlotState records the APP's slot state (output switches etc.) so
// APP-side behaviour can be diagnosed from the log.
func (d *V4) logSlotState(what string, v any) {
	b, _ := json.Marshal(v)
	if len(b) > 600 {
		b = append(b[:600], "…"...)
	}
	logx.Info("[v4] Device %s %s: %s", d.clientID, what, b)
}

func (d *V4) handleEvent(data map[string]any) {
	ev, _ := data["ev"].(string)
	switch ev {
	case "devices.snapshot":
		devs := []map[string]any{}
		for _, x := range config.AsList(data["devices"]) {
			if m, ok := x.(map[string]any); ok {
				devs = append(devs, m)
			}
		}
		d.devMu.Lock()
		d.devices = devs
		if len(devs) > 0 {
			d.defaultSlot = config.Str(devs[0]["slotId"], "slot-a")
		}
		d.devMu.Unlock()
		if len(devs) == 0 {
			logx.Info("[v4] Device %s snapshot: no devices", d.clientID)
			return
		}
		props := config.AsMap(devs[0]["props"])
		st := config.AsMap(devs[0]["slotState"])
		d.logSlotState("snapshot", devs[0])
		d.mu.Lock()
		prev := cp(d.strength)
		// A snapshot is a fresh device state: its 0 is "not set yet".
		d.synced["A"], d.synced["B"] = false, false
		d.strength["A"] = num(props["intensityA"], 0)
		d.strength["B"] = num(props["intensityB"], 0)
		d.strengthMax["A"] = num(config.Get(st, "channelA", "intensityMax"), 200)
		d.strengthMax["B"] = num(config.Get(st, "channelB", "intensityMax"), 200)
		logx.Info("[v4] Device %s snapshot: %d devices, slot=%s str=%v max=%v",
			d.clientID, len(devs), d.slot(), d.strength, d.strengthMax)
		d.mu.Unlock()
		d.hub.reconcile(d, prev)
	case "devices.patch":
		added := false
		d.devMu.Lock()
		for _, x := range config.AsList(data["added"]) {
			if m, ok := x.(map[string]any); ok {
				d.devices = append(d.devices, m)
				logx.Info("[v4] Device %s added slot: %v", d.clientID, m["slotId"])
				added = true
			}
		}
		for _, x := range config.AsList(data["removed"]) {
			sid := config.Str(x, "")
			kept := d.devices[:0]
			for _, dv := range d.devices {
				if config.Str(dv["slotId"], "") != sid {
					kept = append(kept, dv)
				}
			}
			d.devices = kept
			logx.Info("[v4] Device %s removed slot: %s", d.clientID, sid)
		}
		if len(d.devices) > 0 && d.defaultSlot == "" {
			d.defaultSlot = config.Str(d.devices[0]["slotId"], "slot-a")
		}
		d.devMu.Unlock()
		if added { // a (re)connected Coyote starts from the APP's defaults
			d.mu.Lock()
			d.synced["A"], d.synced["B"] = false, false
			d.mu.Unlock()
			d.rpc("devices.get", nil)
		}
	case "slots.patch":
		d.mu.Lock()
		prev := cp(d.strength)
		for _, x := range config.AsList(data["slots"]) {
			sd := config.AsMap(x)
			if st, ok := sd["slotState"]; ok {
				d.mu.Unlock()
				d.logSlotState("slot state", st)
				d.mu.Lock()
			}
			props := config.AsMap(sd["props"])
			if v, ok := props["intensityA"]; ok {
				d.strength["A"] = num(v, d.strength["A"])
			}
			if v, ok := props["intensityB"]; ok {
				d.strength["B"] = num(v, d.strength["B"])
			}
			st := config.AsMap(sd["slotState"])
			if v := config.Get(st, "channelA", "intensityMax"); v != nil {
				d.strengthMax["A"] = num(v, d.strengthMax["A"])
			}
			if v := config.Get(st, "channelB", "intensityMax"); v != nil {
				d.strengthMax["B"] = num(v, d.strengthMax["B"])
			}
		}
		d.mu.Unlock()
		d.hub.reconcile(d, prev)
	case "custom.action":
		logx.Info("[v4] Device %s custom action: %v", d.clientID, data["action"])
	default:
		logx.Debug("[v4] Device %s unknown event: %s", d.clientID, ev)
	}
}

func (d *V4) handleMessage(data map[string]any) {
	switch t, _ := data["t"].(string); t {
	case "ev":
		d.handleEvent(data)
	case "resp":
		if e, ok := data["error"]; ok {
			logx.Warn("[v4] Device %s RPC error: reqId=%v error=%v", d.clientID, data["reqId"], e)
		} else {
			logx.Debug("[v4] Device %s RPC response: reqId=%v result=%v", d.clientID, data["reqId"], data["result"])
		}
	case "req":
		if m, _ := data["m"].(string); m == "ping" {
			_ = d.sendJSON(map[string]any{"type": "message", "data": map[string]any{
				"t": "resp", "reqId": config.Str(data["reqId"], ""), "result": map[string]any{}}})
		}
	default:
		logx.Debug("[v4] Device %s unknown message type: %v", d.clientID, t)
	}
}

// V4Relay serves /ws/dglab-v4 on the web server.
type V4Relay struct {
	hub          *Hub
	ControllerID string
	Enabled      func() bool
	mu           sync.Mutex
	clients      map[string]*V4
}

// NewV4Relay creates a relay with a fresh controller id.
func NewV4Relay(hub *Hub, enabled func() bool) *V4Relay {
	return &V4Relay{hub: hub, ControllerID: config.ShortID(), Enabled: enabled, clients: map[string]*V4{}}
}

func writeJSON(c *websocket.Conn, v any) {
	b, _ := json.Marshal(v)
	_ = c.SetWriteDeadline(time.Now().Add(writeTimeout))
	_ = c.WriteMessage(websocket.TextMessage, b)
}

func closeWith(c *websocket.Conn, code int, reason string) {
	_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = c.Close()
}

func (r *V4Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.Enabled != nil && !r.Enabled() {
		http.Error(w, "v4_disabled", http.StatusForbidden)
		return
	}
	c, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	tid := req.URL.Query().Get("tid")
	if tid == "" {
		tid = req.URL.Query().Get("targetId")
	}
	clientID := config.ShortID()
	if tid == "" {
		writeJSON(c, map[string]any{"type": "hello", "clientId": clientID})
		writeJSON(c, map[string]any{"type": "error", "code": "not_supported",
			"message": "This server only accepts controlled clients with ?tid parameter"})
		closeWith(c, 4001, "controller_not_supported")
		return
	}
	if tid != r.ControllerID {
		writeJSON(c, map[string]any{"type": "hello", "clientId": clientID})
		writeJSON(c, map[string]any{"type": "error", "code": "controller_not_found"})
		closeWith(c, 4001, "controller_not_found")
		logx.Warn("[v4] Rejected client %s: tid=%s does not match %s", clientID, tid, r.ControllerID)
		return
	}
	writeJSON(c, map[string]any{"type": "hello", "clientId": clientID})
	writeJSON(c, map[string]any{"type": "controller_attached", "clientId": r.ControllerID})

	d := &V4{
		chanState: newChanState(r.hub.Limits(), 200),
		hub:       r.hub,
		conn:      c,
		clientID:  clientID,
		closed:    make(chan struct{}),
	}
	r.hub.add(d)
	r.mu.Lock()
	r.clients[clientID] = d
	r.mu.Unlock()
	logx.Success("[v4] APP connected: %s (slot target: %s)", clientID, tid)

	go func() {
		defer logx.Recover("v4 devices.get")
		select {
		case <-d.closed:
		case <-time.After(time.Second):
			d.rpc("devices.get", nil)
		}
	}()

	defer func() {
		d.Close()
		r.mu.Lock()
		delete(r.clients, clientID)
		remaining := len(r.clients)
		r.mu.Unlock()
		logx.Info("[v4] Cleaned up: %s, remaining: %d", clientID, remaining)
	}()
	for {
		_, raw, err := c.ReadMessage()
		if err != nil {
			logx.Warn("[v4] APP disconnected: %s (%v)", clientID, err)
			return
		}
		logx.Debug("[v4] Device %s recv: %s", clientID, raw)
		var msg map[string]any
		if err := json.Unmarshal(raw, &msg); err != nil {
			logx.Warn("[v4] Device %s invalid JSON", clientID)
			continue
		}
		switch t, _ := msg["type"].(string); t {
		case "ping":
			_ = d.sendJSON(map[string]any{"type": "pong", "ts": time.Now().UnixMilli()})
		case "pong", "heartbeat":
		case "message":
			if data, ok := msg["data"].(map[string]any); ok && len(data) > 0 {
				d.handleMessage(data)
			}
		default:
			logx.Debug("[v4] Device %s unknown frame type: %v", clientID, t)
		}
	}
}
