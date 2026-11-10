package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
)

// Optional HypeRate heart rate (off by default). The user only enters the
// session ID shown in the HypeRate app (or pastes the share link); the
// endpoint and channel names are filled in. HypeRate's realtime API is a
// Phoenix channel: connect with an API key, join "hr:<session>", send a
// heartbeat every 10 s, receive "hr_update" {hr}.
// The API key is a secret: stored in the config, never returned/exported.

var hyperateURL = "wss://app.hyperate.io/socket/websocket" // var for tests

type hyperateState struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	connected bool
	bpm       int
	lastAt    time.Time
	err       string
}

var hyperateIDRe = regexp.MustCompile(`^[A-Za-z0-9]{2,32}$`)

// normaliseHypeRateID accepts "ABCD", "https://app.hyperate.io/ABCD",
// "app.hyperate.io/ABCD?x" and returns "ABCD" ("" if invalid).
func normaliseHypeRateID(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if !hyperateIDRe.MatchString(s) {
		return ""
	}
	return s
}

func (a *App) hyperateCfg() config.M { return config.AsMap(a.Cfg.Get("hyperate")) }

func (a *App) setHypeRate(f func(s *hyperateState)) {
	a.hyperate.mu.Lock()
	f(&a.hyperate)
	a.hyperate.mu.Unlock()
}

// StartHypeRateIfEnabled (re)starts the client according to the config.
func (a *App) StartHypeRateIfEnabled() {
	a.stopHypeRate()
	c := a.hyperateCfg()
	if !config.Bool(c["enabled"], false) || config.Str(c["session_id"], "") == "" ||
		strings.TrimSpace(config.Str(c["api_key"], "")) == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.setHypeRate(func(s *hyperateState) { s.cancel, s.done, s.err = cancel, done, "" })
	go func() {
		defer close(done)
		defer logx.Recover("hyperate")
		a.hyperateLoop(ctx)
	}()
}

func (a *App) stopHypeRate() {
	var cancel context.CancelFunc
	var done chan struct{}
	a.setHypeRate(func(s *hyperateState) {
		cancel, done = s.cancel, s.done
		s.cancel, s.done, s.connected = nil, nil, false
	})
	if cancel != nil {
		cancel()
		<-done
		a.publishHR("hyperate", a.hyperateCfg(), 0, false)
	}
}

func (a *App) hyperateLoop(ctx context.Context) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		c := a.hyperateCfg()
		started := time.Now()
		err := a.hyperateSession(ctx, strings.TrimSpace(config.Str(c["api_key"], "")), config.Str(c["session_id"], ""))
		if ctx.Err() != nil {
			return
		}
		a.setHypeRate(func(s *hyperateState) { s.connected, s.err = false, err.Error() })
		a.publishHR("hyperate", a.hyperateCfg(), 0, false)
		var authErr *pulsoidAuthError
		if errors.As(err, &authErr) {
			logx.Error("[hyperate] API key rejected (HTTP %d)", authErr.status)
			return
		}
		if time.Since(started) > time.Minute {
			backoff = 2 * time.Second
		}
		logx.Warn("[hyperate] %v; reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

type phxMsg struct {
	Topic   string          `json:"topic"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
	Ref     any             `json:"ref"`
}

func (a *App) hyperateSession(ctx context.Context, key, id string) error {
	u := hyperateURL + "?token=" + url.QueryEscape(key)
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	c, resp, err := d.DialContext(ctx, u, nil)
	if err != nil {
		if resp != nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
			return &pulsoidAuthError{resp.StatusCode}
		}
		if resp != nil {
			return fmt.Errorf("connect failed (HTTP %d)", resp.StatusCode)
		}
		return errors.New("connect failed: " + strings.ReplaceAll(err.Error(), key, "***"))
	}
	defer c.Close()
	var wmu sync.Mutex
	send := func(m phxMsg) error {
		b, _ := json.Marshal(m)
		wmu.Lock()
		defer wmu.Unlock()
		_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return c.WriteMessage(websocket.TextMessage, b)
	}
	topic := "hr:" + id
	if err := send(phxMsg{Topic: topic, Event: "phx_join", Payload: json.RawMessage(`{}`), Ref: 1}); err != nil {
		return errors.New("join failed")
	}
	hbCtx, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for ref := 2; ; ref++ {
			select {
			case <-hbCtx.Done():
				_ = c.Close()
				return
			case <-t.C:
				if send(phxMsg{Topic: "phoenix", Event: "heartbeat", Payload: json.RawMessage(`{}`), Ref: ref}) != nil {
					return
				}
			}
		}
	}()
	logx.Success("[hyperate] Connected, session %s", id)
	for {
		// HypeRate only sends while the watch measures; the heartbeat
		// replies keep the read alive, so a long silence means trouble.
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, raw, err := c.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("connection lost")
		}
		var m phxMsg
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		switch m.Event {
		case "phx_reply":
			var r struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal(m.Payload, &r)
			if m.Topic == topic && r.Status != "ok" && r.Status != "" {
				return errors.New("session " + id + " could not be joined (" + r.Status + ")")
			}
			if m.Topic == topic {
				a.setHypeRate(func(s *hyperateState) { s.connected, s.err = true, "" })
			}
		case "hr_update":
			var p struct {
				HR float64 `json:"hr"`
			}
			if json.Unmarshal(m.Payload, &p) != nil || p.HR <= 0 {
				continue
			}
			bpm := int(p.HR + 0.5)
			a.setHypeRate(func(s *hyperateState) { s.bpm, s.lastAt, s.connected = bpm, time.Now(), true })
			a.publishHR("hyperate", a.hyperateCfg(), bpm, true)
		case "phx_error", "phx_close":
			return errors.New("session closed by HypeRate")
		}
	}
}

// ---------------------------------------------------------------- API

func (a *App) apiHypeRateGet(*http.Request) (any, error) {
	c := a.hyperateCfg()
	key := strings.TrimSpace(config.Str(c["api_key"], ""))
	hint := ""
	if len(key) > 4 {
		hint = "…" + key[len(key)-4:]
	}
	a.hyperate.mu.Lock()
	defer a.hyperate.mu.Unlock()
	var last any
	if !a.hyperate.lastAt.IsZero() {
		last = float64(a.hyperate.lastAt.UnixMilli()) / 1000
	}
	return map[string]any{
		"enabled": config.Bool(c["enabled"], false), "session_id": config.Str(c["session_id"], ""),
		"has_key": key != "", "key_hint": hint,
		"min_bpm": config.Float(c["min_bpm"], 40), "max_bpm": config.Float(c["max_bpm"], 200),
		"send_to_vrchat": config.Bool(c["send_to_vrchat"], true),
		"running":        a.hyperate.cancel != nil, "connected": a.hyperate.connected,
		"bpm": a.hyperate.bpm, "last_at": last, "error": a.hyperate.err,
		"params": []string{hrParamPrefix + "HeartRate", hrParamPrefix + "BPM", hrParamPrefix + "Connected"},
	}, nil
}

func (a *App) apiHypeRateSet(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	var bad error
	a.Cfg.Write(func(adv, _ config.M) {
		c := config.Sub(adv, true, "hyperate")
		if v, ok := data["enabled"]; ok {
			c["enabled"] = config.Bool(v, false)
		}
		if v, ok := data["session_id"]; ok {
			raw := strings.TrimSpace(config.Str(v, ""))
			id := normaliseHypeRateID(raw)
			if raw != "" && id == "" {
				bad = httpErr(400, "invalid HypeRate session ID")
				return
			}
			c["session_id"] = id
		}
		if v, ok := data["api_key"]; ok { // "" keeps the stored key
			if k := strings.TrimSpace(config.Str(v, "")); k != "" {
				c["api_key"] = k
			}
		}
		if config.Bool(data["clear_key"], false) {
			c["api_key"] = ""
		}
		lo, hi := config.Float(c["min_bpm"], 40), config.Float(c["max_bpm"], 200)
		if v, ok := data["min_bpm"]; ok {
			lo = config.Clamp(config.Float(v, lo), 0, 300)
		}
		if v, ok := data["max_bpm"]; ok {
			hi = config.Clamp(config.Float(v, hi), 0, 300)
		}
		if hi <= lo {
			bad = httpErr(400, "max_bpm must be greater than min_bpm")
			return
		}
		c["min_bpm"], c["max_bpm"] = lo, hi
		if v, ok := data["send_to_vrchat"]; ok {
			c["send_to_vrchat"] = config.Bool(v, true)
		}
	})
	if bad != nil {
		return nil, bad
	}
	if err := a.Cfg.Save(); err != nil {
		return nil, err
	}
	a.StartHypeRateIfEnabled()
	return a.apiHypeRateGet(r)
}
