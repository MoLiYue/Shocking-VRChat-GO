package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/osc"
)

// Optional Pulsoid heart-rate integration (off by default), modelled on the
// VRCOSC heart-rate module:
//   - the BPM is published inside the program as virtual parameters
//     (Pulsoid/HeartRate 0..1, Pulsoid/BPM, Pulsoid/Connected) that can be
//     mapped to modes or used in overlimit rules like any avatar parameter,
//   - optionally sent to VRChat with VRCOSC-compatible names
//     (VRCOSC/Heartrate/*) so existing heart-rate avatar prefabs work.
// The access token is a secret: it is stored in the config but never
// returned by the API or logged.

var pulsoidURL = "wss://dev.pulsoid.net/api/v1/data/real_time" // var for tests

const (
	pulsoidParamPrefix = "/avatar/parameters/Pulsoid/"
	vrcoscHRPrefix     = "/avatar/parameters/VRCOSC/Heartrate/"
	pulsoidStaleAfter  = 10 * time.Second
)

type pulsoidState struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	connected bool
	bpm       int
	lastAt    time.Time
	err       string
}

func (a *App) pulsoidCfg() config.M { return config.AsMap(a.Cfg.Get("pulsoid")) }

// StartPulsoidIfEnabled (re)starts the client according to the config.
func (a *App) StartPulsoidIfEnabled() {
	a.stopPulsoid()
	c := a.pulsoidCfg()
	if !config.Bool(c["enabled"], false) || strings.TrimSpace(config.Str(c["token"], "")) == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &a.pulsoid
	p.mu.Lock()
	p.cancel, p.done, p.err = cancel, make(chan struct{}), ""
	done := p.done
	p.mu.Unlock()
	go func() {
		defer close(done)
		defer logx.Recover("pulsoid")
		a.pulsoidLoop(ctx)
	}()
}

func (a *App) stopPulsoid() {
	p := &a.pulsoid
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.cancel, p.done = nil, nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
		a.publishHeartRate(0, false)
	}
}

func (a *App) setPulsoid(f func(p *pulsoidState)) {
	a.pulsoid.mu.Lock()
	f(&a.pulsoid)
	a.pulsoid.mu.Unlock()
}

func (a *App) pulsoidLoop(ctx context.Context) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		token := strings.TrimSpace(config.Str(a.pulsoidCfg()["token"], ""))
		started := time.Now()
		err := a.pulsoidSession(ctx, token)
		if time.Since(started) > time.Minute {
			backoff = 2 * time.Second // it was working: retry quickly
		}
		if ctx.Err() != nil {
			return
		}
		a.setPulsoid(func(p *pulsoidState) { p.connected = false; p.err = err.Error() })
		a.publishHeartRate(0, false)
		var authErr *pulsoidAuthError
		if errors.As(err, &authErr) {
			logx.Error("[pulsoid] %v - check the access token in Settings", err)
			return // retrying cannot fix a bad token
		}
		logx.Warn("[pulsoid] %v; reconnecting in %s", err, backoff)
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

type pulsoidAuthError struct{ status int }

func (e *pulsoidAuthError) Error() string {
	return fmt.Sprintf("Pulsoid rejected the access token (HTTP %d)", e.status)
}

// pulsoidSession runs one websocket connection until it fails.
func (a *App) pulsoidSession(ctx context.Context, token string) error {
	u := pulsoidURL + "?access_token=" + url.QueryEscape(token)
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	c, resp, err := d.DialContext(ctx, u, nil)
	if err != nil {
		if resp != nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
			return &pulsoidAuthError{resp.StatusCode}
		}
		// never include the URL (it carries the token)
		if resp != nil {
			return fmt.Errorf("connect failed (HTTP %d)", resp.StatusCode)
		}
		return errors.New("connect failed: " + strings.ReplaceAll(err.Error(), token, "***"))
	}
	defer c.Close()
	go func() { <-ctx.Done(); _ = c.Close() }()
	logx.Success("[pulsoid] Connected")
	a.setPulsoid(func(p *pulsoidState) { p.connected, p.err = true, "" })
	for {
		_ = c.SetReadDeadline(time.Now().Add(pulsoidStaleAfter))
		_, msg, err := c.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var ne interface{ Timeout() bool }
			if errors.As(err, &ne) && ne.Timeout() {
				return errors.New("no heart rate for 10 s")
			}
			return errors.New("connection lost")
		}
		var m struct {
			Data struct {
				HeartRate float64 `json:"heart_rate"`
			} `json:"data"`
		}
		if json.Unmarshal(msg, &m) != nil || m.Data.HeartRate <= 0 {
			continue
		}
		bpm := int(math.Round(m.Data.HeartRate))
		a.setPulsoid(func(p *pulsoidState) { p.bpm, p.lastAt, p.connected = bpm, time.Now(), true })
		a.publishHeartRate(bpm, true)
	}
}

// heartRateNormalised maps bpm to 0..1 between the configured bounds.
func (a *App) heartRateNormalised(bpm int) float64 {
	c := a.pulsoidCfg()
	lo := config.Float(c["min_bpm"], 40)
	hi := config.Float(c["max_bpm"], 200)
	if hi <= lo {
		return 0
	}
	return config.Clamp((float64(bpm)-lo)/(hi-lo), 0, 1)
}

// publishHeartRate injects the virtual parameters and optionally sends the
// VRCOSC-compatible ones to VRChat.
func (a *App) publishHeartRate(bpm int, connected bool) {
	norm := 0.0
	if connected {
		norm = a.heartRateNormalised(bpm)
	}
	a.Engine.stateMu.Lock()
	srv := a.Engine.oscSrv
	a.Engine.stateMu.Unlock()
	if srv != nil {
		srv.Inject(pulsoidParamPrefix+"HeartRate", norm)
		srv.Inject(pulsoidParamPrefix+"BPM", bpm)
		srv.Inject(pulsoidParamPrefix+"Connected", connected)
	}
	if !config.Bool(a.pulsoidCfg()["send_to_vrchat"], true) {
		return
	}
	host, port := a.oscSendTarget()
	cl, err := osc.NewClient(host, port)
	if err != nil {
		return
	}
	defer cl.Close()
	digit := func(n int) float64 { return float64(n%10) / 10 }
	_ = cl.Send(vrcoscHRPrefix+"Connected", connected)
	_ = cl.Send(vrcoscHRPrefix+"Enabled", connected)
	_ = cl.Send(vrcoscHRPrefix+"Value", bpm)
	_ = cl.Send(vrcoscHRPrefix+"Normalised", float32(norm))
	_ = cl.Send(vrcoscHRPrefix+"Units", float32(digit(bpm)))
	_ = cl.Send(vrcoscHRPrefix+"Tens", float32(digit(bpm/10)))
	_ = cl.Send(vrcoscHRPrefix+"Hundreds", float32(digit(bpm/100)))
}

// ---------------------------------------------------------------- API

func (a *App) apiPulsoidGet(*http.Request) (any, error) {
	c := a.pulsoidCfg()
	tok := strings.TrimSpace(config.Str(c["token"], ""))
	hint := ""
	if len(tok) > 4 {
		hint = "…" + tok[len(tok)-4:]
	}
	a.pulsoid.mu.Lock()
	defer a.pulsoid.mu.Unlock()
	var last any
	if !a.pulsoid.lastAt.IsZero() {
		last = float64(a.pulsoid.lastAt.UnixMilli()) / 1000
	}
	return map[string]any{
		"enabled": config.Bool(c["enabled"], false), "has_token": tok != "", "token_hint": hint,
		"min_bpm": config.Float(c["min_bpm"], 40), "max_bpm": config.Float(c["max_bpm"], 200),
		"send_to_vrchat": config.Bool(c["send_to_vrchat"], true),
		"running":        a.pulsoid.cancel != nil, "connected": a.pulsoid.connected,
		"bpm": a.pulsoid.bpm, "last_at": last, "error": a.pulsoid.err,
		"params": []string{pulsoidParamPrefix + "HeartRate", pulsoidParamPrefix + "BPM", pulsoidParamPrefix + "Connected"},
	}, nil
}

func (a *App) apiPulsoidSet(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	var bad error
	a.Cfg.Write(func(adv, _ config.M) {
		c := config.Sub(adv, true, "pulsoid")
		if v, ok := data["enabled"]; ok {
			c["enabled"] = config.Bool(v, false)
		}
		if v, ok := data["token"]; ok { // "" keeps the stored token
			if t := strings.TrimSpace(config.Str(v, "")); t != "" {
				c["token"] = t
			}
		}
		if config.Bool(data["clear_token"], false) {
			c["token"] = ""
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
	a.StartPulsoidIfEnabled()
	return a.apiPulsoidGet(r)
}
