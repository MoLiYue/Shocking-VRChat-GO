package app

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"shockingvrc/internal/logx"
	"shockingvrc/internal/sps"
	"shockingvrc/internal/wave"
)

// ---------------------------------------------------------------- /ws/live

type liveClient struct {
	conn   *websocket.Conn
	send   chan []byte
	mu     sync.Mutex
	topics map[string]bool
}

// LiveHub pushes real-time topics (wave_A, wave_B, osc, status, log) to the UI.
type LiveHub struct {
	mu      sync.RWMutex
	clients map[*liveClient]struct{}
}

func newLiveHub() *LiveHub { return &LiveHub{clients: map[*liveClient]struct{}{}} }

var liveUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// HasSubscribers reports whether any client subscribed to topic.
func (h *LiveHub) HasSubscribers(topic string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		c.mu.Lock()
		ok := c.topics[topic]
		c.mu.Unlock()
		if ok {
			return true
		}
	}
	return false
}

// Broadcast sends {"topic": topic, ...payload} to subscribers. Non-blocking:
// slow clients drop messages. Must never log (it is the log observer).
func (h *LiveHub) Broadcast(topic string, payload any) {
	h.mu.RLock()
	if len(h.clients) == 0 {
		h.mu.RUnlock()
		return
	}
	targets := make([]*liveClient, 0, len(h.clients))
	for c := range h.clients {
		c.mu.Lock()
		if c.topics[topic] {
			targets = append(targets, c)
		}
		c.mu.Unlock()
	}
	h.mu.RUnlock()
	if len(targets) == 0 {
		return
	}
	msg := map[string]any{}
	if b, err := json.Marshal(payload); err == nil {
		_ = json.Unmarshal(b, &msg)
	}
	msg["topic"] = topic
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	for _, c := range targets {
		select {
		case c.send <- b:
		default:
		}
	}
}

func (h *LiveHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := liveUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &liveClient{conn: conn, send: make(chan []byte, 256), topics: map[string]bool{}}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer logx.Recover("live writer")
		for {
			select {
			case <-done:
				return
			case b := <-c.send:
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	defer func() {
		close(done)
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		_ = conn.Close()
	}()
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg map[string]any
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		if subs, ok := msg["subscribe"].([]any); ok {
			t := map[string]bool{}
			for _, s := range subs {
				if str, ok := s.(string); ok {
					t[str] = true
				}
			}
			c.mu.Lock()
			c.topics = t
			c.mu.Unlock()
		}
		if p, ok := msg["ping"].(bool); ok && p {
			select {
			case c.send <- []byte(`{"pong":true}`):
			default:
			}
		}
	}
}

// ---------------------------------------------------------------- wave history

const (
	waveHistoryMax      = 800
	waveHistorySampleMs = 25
)

type waveSample struct {
	S   int `json:"s"`
	F   int `json:"f"`
	Seq int `json:"seq,omitempty"`
}

type waveHistory struct {
	mu         sync.Mutex
	samples    map[string][]waveSample
	seq        map[string]int
	lastUpdate map[string]time.Time
}

func newWaveHistory() *waveHistory {
	return &waveHistory{
		samples:    map[string][]waveSample{"A": nil, "B": nil},
		seq:        map[string]int{"A": 0, "B": 0},
		lastUpdate: map[string]time.Time{},
	}
}

func (a *App) recordWave(ch, wavestr string) {
	ops, err := wave.DecodeOps(wavestr)
	if err != nil {
		return
	}
	h := a.waves
	h.mu.Lock()
	if _, ok := h.samples[ch]; !ok {
		h.mu.Unlock()
		return
	}
	out := make([]map[string]int, 0, len(ops)*4)
	for _, op := range ops {
		for i := 0; i < 4; i++ {
			f, s := wave.HexByte(op, i*2), wave.HexByte(op, 8+i*2)
			h.seq[ch]++
			h.samples[ch] = append(h.samples[ch], waveSample{S: s, F: f, Seq: h.seq[ch]})
			out = append(out, map[string]int{"s": s, "f": f})
		}
	}
	if n := len(h.samples[ch]); n > waveHistoryMax {
		h.samples[ch] = append([]waveSample(nil), h.samples[ch][n-waveHistoryMax:]...)
	}
	h.lastUpdate[ch] = time.Now()
	seq := h.seq[ch]
	h.mu.Unlock()
	a.Live.Broadcast("wave_"+ch, map[string]any{"samples": out, "seq": seq})
}

func (h *waveHistory) clear(ch string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.samples[ch]; !ok {
		return
	}
	h.samples[ch] = nil
	delete(h.lastUpdate, ch)
}

// snapshot returns the last 200 samples, shifted by elapsed idle time.
func (h *waveHistory) snapshot(ch string) []map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	hist := h.samples[ch]
	conv := func(s []waveSample) []map[string]int {
		out := make([]map[string]int, len(s))
		for i, x := range s {
			out[i] = map[string]int{"s": x.S, "f": x.F, "seq": x.Seq}
		}
		return out
	}
	if len(hist) == 0 {
		return []map[string]int{}
	}
	tail := hist
	if len(tail) > 200 {
		tail = tail[len(tail)-200:]
	}
	last, ok := h.lastUpdate[ch]
	if !ok {
		return conv(tail)
	}
	elapsed := int(time.Since(last).Milliseconds() / waveHistorySampleMs)
	zero := map[string]int{"s": 0, "f": 10}
	if elapsed <= 0 {
		return conv(tail)
	}
	if elapsed >= 200 {
		out := make([]map[string]int, 200)
		for i := range out {
			out[i] = zero
		}
		return out
	}
	if elapsed >= len(tail) {
		out := make([]map[string]int, len(tail))
		for i := range out {
			out[i] = zero
		}
		return out
	}
	out := conv(tail[elapsed:])
	for i := 0; i < elapsed; i++ {
		out = append(out, zero)
	}
	return out
}

func (h *waveHistory) since(ch string, since int) ([]map[string]int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []map[string]int{}
	for _, s := range h.samples[ch] {
		if s.Seq > since {
			out = append(out, map[string]int{"s": s.S, "f": s.F})
		}
	}
	return out, h.seq[ch]
}

// ---------------------------------------------------------------- OSC activity

type oscEvent struct {
	Time    float64 `json:"time"`
	Address string  `json:"address"`
	Value   float64 `json:"value"`
	Channel string  `json:"channel"`
	Mode    string  `json:"mode"`
}

type oscActivity struct {
	mu     sync.Mutex
	events []oscEvent // newest first, max 50
}

func round4(f float64) float64 { return float64(wave.Round(f*1e4)) / 1e4 }

func (a *App) recordOSCActivity(addr string, value float64, ch, mode string) {
	if sps.IsOGBAddress(addr) {
		a.trackOGBZone(addr)
	}
	ev := oscEvent{Time: nowSec(), Address: addr, Value: round4(value), Channel: ch, Mode: mode}
	a.activity.mu.Lock()
	a.activity.events = append([]oscEvent{ev}, a.activity.events...)
	if len(a.activity.events) > 50 {
		a.activity.events = a.activity.events[:50]
	}
	a.activity.mu.Unlock()
	a.Live.Broadcast("osc", map[string]any{"event": ev})
}

func (o *oscActivity) list() []oscEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]oscEvent{}, o.events...)
}

func (o *oscActivity) lastTime() any {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.events) == 0 {
		return nil
	}
	return o.events[0].Time
}

func nowSec() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// ---------------------------------------------------------------- OGB zones

const ogbSeenMax = 64

func (a *App) trackOGBZone(addr string) {
	tt, zone, _, ok := sps.ParseOGBAddress(addr)
	if !ok {
		return
	}
	a.ogbMu.Lock()
	defer a.ogbMu.Unlock()
	a.ogbSeen[[2]string{tt, zone}] = nowSec()
	for len(a.ogbSeen) > ogbSeenMax {
		var oldest [2]string
		first := true
		for k, v := range a.ogbSeen {
			if first || v < a.ogbSeen[oldest] {
				oldest, first = k, false
			}
		}
		delete(a.ogbSeen, oldest)
	}
}
