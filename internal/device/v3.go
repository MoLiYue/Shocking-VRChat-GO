package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
)

const writeTimeout = 5 * time.Second

// V3Message is the DG-LAB socket protocol frame.
type V3Message struct {
	Type     string `json:"type"`
	ClientID string `json:"clientId"`
	TargetID string `json:"targetId"`
	Message  string `json:"message"`
}

var v3Heartbeat = V3Message{Type: "heartbeat", Message: "200"}

// V3 is a DG-LAB APP connected through the V3 socket protocol
// (we act as the socket server and the controller at the same time).
type V3 struct {
	*chanState
	hub        *Hub
	conn       *websocket.Conn
	writeMu    sync.Mutex
	uuid       string
	masterUUID string
	closeOnce  sync.Once
	done       chan struct{}
}

func (d *V3) ID() string        { return d.uuid }
func (d *V3) state() *chanState { return d.chanState }
func (d *V3) Status() Status    { return d.snapshot(d.uuid) }
func (d *V3) String() string    { return fmt.Sprintf("<DGConnection (id:%s)>", d.uuid) }

func (d *V3) send(m V3Message) error {
	b, _ := json.Marshal(m)
	logx.Debug("[ws] Device %s sending: %s", d.uuid, b)
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	_ = d.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return d.conn.WriteMessage(websocket.TextMessage, b)
}

func (d *V3) msg(text string) error {
	return d.send(V3Message{Type: "msg", ClientID: d.masterUUID, TargetID: d.uuid, Message: text})
}

func chanNum(ch string) string {
	if ch == "A" {
		return "1"
	}
	return "2"
}

// SetStrength implements Device.
func (d *V3) SetStrength(ch, mode string, value int, force bool) error {
	d.mu.Lock()
	if !force {
		if value < 0 || value > 200 {
			d.mu.Unlock()
			return fmt.Errorf("strength %d out of range", value)
		}
		limit := d.upperLocked(ch)
		if value > limit && mode == "2" {
			logx.Warn("[ws] Device %s set_strength: %d over limit %d, clamping to %d", d.uuid, value, limit, limit)
			value = limit
		}
	}
	switch mode {
	case "2":
		d.strength[ch] = value
	case "1":
		d.strength[ch] = min(200, d.strength[ch]+value)
	case "0":
		d.strength[ch] = max(0, d.strength[ch]-value)
	}
	d.mu.Unlock()
	logx.Debug("[ws] Set strength: ch=%s mode=%s val=%d", ch, mode, value)
	return d.msg(fmt.Sprintf("strength-%s+%s+%d", chanNum(ch), mode, value))
}

// SendWave implements Device.
func (d *V3) SendWave(ch, wavestr string) error {
	if wavestr == "" {
		wavestr = defaultWave
	}
	return d.msg(fmt.Sprintf("pulse-%s:%s", ch, wavestr))
}

// ClearWave implements Device.
func (d *V3) ClearWave(ch string) error { return d.msg("clear-" + chanNum(ch)) }

// Close implements Device.
func (d *V3) Close() {
	d.closeOnce.Do(func() {
		close(d.done)
		d.writeMu.Lock()
		_ = d.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, ""), time.Now().Add(time.Second))
		d.writeMu.Unlock()
		_ = d.conn.Close()
	})
}

func (d *V3) handle(m V3Message) error {
	switch m.Type {
	case "bind":
		if m.TargetID != d.uuid {
			logx.Warn("[ws] Device %s bind rejected: UUID mismatch (%s)", d.uuid, m.TargetID)
			return nil
		}
		if m.ClientID != d.masterUUID {
			logx.Warn("[ws] Device %s bind rejected: unknown controller %s", d.uuid, m.ClientID)
			return nil
		}
		return d.send(V3Message{Type: "bind", ClientID: m.ClientID, TargetID: m.TargetID, Message: "200"})
	case "msg":
		switch {
		case strings.HasPrefix(m.Message, "strength-"):
			parts := strings.Split(strings.TrimPrefix(m.Message, "strength-"), "+")
			if len(parts) != 4 {
				logx.Warn("[ws] Device %s malformed strength msg: %s", d.uuid, m.Message)
				return nil
			}
			vals := make([]int, 4)
			for i, p := range parts {
				v, err := strconv.Atoi(strings.TrimSpace(p))
				if err != nil {
					logx.Warn("[ws] Device %s malformed strength msg: %s", d.uuid, m.Message)
					return nil
				}
				vals[i] = v
			}
			d.mu.Lock()
			d.strength["A"], d.strength["B"], d.strengthMax["A"], d.strengthMax["B"] = vals[0], vals[1], vals[2], vals[3]
			type fix struct {
				ch    string
				limit int
			}
			var fixes []fix
			for _, ch := range []string{"A", "B"} {
				limit := d.upperLocked(ch)
				if d.strength[ch] != 0 && d.strength[ch] != limit {
					fixes = append(fixes, fix{ch, limit})
				}
			}
			d.mu.Unlock()
			for _, f := range fixes {
				if err := d.SetStrength(f.ch, "2", f.limit, false); err != nil {
					return err
				}
			}
		case strings.HasPrefix(m.Message, "feedback-"):
			logx.Info("[ws] Device %s feedback: %s", d.uuid, m.Message)
		default:
			logx.Warn("[ws] Device %s unknown msg: %s", d.uuid, m.Message)
		}
	case "heartbeat":
		logx.Debug("[ws] Device %s heartbeat", d.uuid)
	}
	return nil
}

func (d *V3) serve() {
	logx.Success("[ws] Device connected: %s", d.uuid)
	defer func() {
		logx.Warn("[ws] Device disconnected: %s", d.uuid)
		d.Close()
		d.hub.remove(d)
	}()
	if err := d.send(V3Message{Type: "bind", ClientID: d.uuid, Message: "targetId"}); err != nil {
		return
	}
	// connection_init: after 2s push strength 1 on both channels
	go func() {
		select {
		case <-d.done:
			return
		case <-time.After(2 * time.Second):
		}
		_ = d.SetStrength("A", "2", 1, true)
		_ = d.SetStrength("B", "2", 1, true)
	}()
	// heartbeat every 60s
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-d.done:
				return
			case <-t.C:
				logx.Debug("[ws] Device %s sending heartbeat", d.uuid)
				if err := d.send(v3Heartbeat); err != nil {
					return
				}
			}
		}
	}()
	for {
		_, data, err := d.conn.ReadMessage()
		if err != nil {
			return
		}
		logx.Debug("[ws] Device %s recv: %s", d.uuid, data)
		var m V3Message
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m); err != nil {
			logx.Warn("[ws] Device %s malformed message: %v", d.uuid, err)
			continue
		}
		if err := d.handle(m); err != nil {
			logx.Error("[ws] %v", err)
		}
	}
}

// V3Server is the DG-LAB socket server (ws://ip:port/<master_uuid>).
type V3Server struct {
	hub        *Hub
	masterUUID func() string
	srv        *http.Server
	ln         net.Listener
	// Middleware wraps the handler (host / origin checks).
	Middleware func(http.Handler) http.Handler
	mu         sync.Mutex
	conns      map[*V3]struct{}
}

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(*http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// NewV3Server creates (but does not start) a V3 server.
func NewV3Server(hub *Hub, masterUUID func() string) *V3Server {
	return &V3Server{hub: hub, masterUUID: masterUUID, conns: map[*V3]struct{}{}}
}

func (s *V3Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only the QR code path ws://ip:port/<master_uuid> is accepted. The
	// random uuid is the access token (instead of browser Origin/Host
	// checks, which the DG-LAB APP may not pass).
	master := s.masterUUID()
	if master == "" || strings.Trim(r.URL.Path, "/") != master {
		logx.Warn("[ws] Rejected connection from %s: path %q does not match master uuid", r.RemoteAddr, r.URL.Path)
		http.NotFound(w, r)
		return
	}
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	d := &V3{
		chanState:  newChanState(s.hub.Limits(), 0),
		hub:        s.hub,
		conn:       c,
		uuid:       config.NewUUID(),
		masterUUID: master,
		done:       make(chan struct{}),
	}
	s.mu.Lock()
	s.conns[d] = struct{}{}
	s.mu.Unlock()
	s.hub.add(d)
	d.serve()
	s.mu.Lock()
	delete(s.conns, d)
	s.mu.Unlock()
}

// Start listens on host:port.
func (s *V3Server) Start(host string, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	s.ln = ln
	var h http.Handler = s
	if s.Middleware != nil {
		h = s.Middleware(h)
	}
	s.srv = &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logx.Error("[engine] WS server error: %v", err)
		}
	}()
	return nil
}

// Stop closes the listener and all device connections.
func (s *V3Server) Stop() {
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = s.srv.Shutdown(ctx)
		cancel()
	}
	s.mu.Lock()
	conns := make([]*V3, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}
