package app

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
	"shockingvrc/internal/osc"
)

// ---------------------------------------------------------------- recorder

type recMsg struct {
	T    float64 `json:"t"`
	Addr string  `json:"addr"`
	Args []any   `json:"args"`
}

type recorder struct {
	mu       sync.Mutex
	active   bool
	start    time.Time
	messages []recMsg
	filename string
}

func (r *recorder) record(addr string, args []any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return
	}
	elapsed := float64(time.Since(r.start).Nanoseconds()) / 1e6
	cp := make([]any, 0, len(args))
	for _, a := range args {
		if b, ok := a.([]byte); ok {
			cp = append(cp, string(b))
			continue
		}
		cp = append(cp, a)
	}
	r.messages = append(r.messages, recMsg{T: float64(int64(elapsed*1000+0.5)) / 1000, Addr: addr, Args: cp})
}

func (a *App) recordingsDir() string { return a.Path("recordings") }

// safeFile rejects path traversal in user-provided file names.
func safeFile(name string) (string, bool) {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", false
	}
	return name, true
}

func (a *App) apiRecorderStart(*http.Request) (any, error) {
	r := a.rec
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active {
		return map[string]any{"success": false, "message": "Already recording"}, nil
	}
	_ = os.MkdirAll(a.recordingsDir(), 0o755)
	r.active, r.start, r.messages = true, time.Now(), nil
	r.filename = "rec_" + time.Now().Format("20060102_150405") + ".json"
	logx.Success("[recorder] Started recording: %s", r.filename)
	return map[string]any{"success": true, "filename": r.filename}, nil
}

func (a *App) apiRecorderStop(*http.Request) (any, error) {
	r := a.rec
	r.mu.Lock()
	if !r.active {
		r.mu.Unlock()
		return map[string]any{"success": false, "message": "Not recording"}, nil
	}
	r.active = false
	msgs, fn := r.messages, r.filename
	r.messages = nil
	r.mu.Unlock()
	if msgs == nil {
		msgs = []recMsg{}
	}
	dur := 0.0
	if len(msgs) > 0 {
		dur = msgs[len(msgs)-1].T
	}
	data := map[string]any{
		"version":       1,
		"recorded_at":   time.Now().Format("2006-01-02T15:04:05"),
		"duration_ms":   dur,
		"message_count": len(msgs),
		"messages":      msgs,
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(a.recordingsDir(), fn), b, 0o644); err != nil {
		return nil, err
	}
	logx.Success("[recorder] Stopped. Saved %d messages (%.1fs) to %s", len(msgs), dur/1000, fn)
	return map[string]any{"success": true, "filename": fn, "message_count": len(msgs), "duration_ms": dur}, nil
}

func (a *App) apiRecorderStatus(*http.Request) (any, error) {
	r := a.rec
	r.mu.Lock()
	defer r.mu.Unlock()
	elapsed := 0.0
	if r.active {
		elapsed = float64(time.Since(r.start).Milliseconds())
	}
	var fn any
	if r.filename != "" {
		fn = r.filename
	}
	return map[string]any{"recording": r.active, "filename": fn, "message_count": len(r.messages), "elapsed_ms": elapsed}, nil
}

func (a *App) apiRecordingsList(*http.Request) (any, error) {
	_ = os.MkdirAll(a.recordingsDir(), 0o755)
	entries, _ := os.ReadDir(a.recordingsDir())
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	files := []any{}
	for _, n := range names {
		p := filepath.Join(a.recordingsDir(), n)
		item := map[string]any{"name": n, "size_kb": 0, "duration_ms": 0, "message_count": 0, "recorded_at": ""}
		if st, err := os.Stat(p); err == nil {
			if b, err := os.ReadFile(p); err == nil {
				var hdr map[string]any
				if json.Unmarshal(b, &hdr) == nil {
					item["size_kb"] = float64(int(float64(st.Size())/1024*10+0.5)) / 10
					item["duration_ms"] = orZero(hdr["duration_ms"])
					item["message_count"] = orZero(hdr["message_count"])
					item["recorded_at"] = config.Str(hdr["recorded_at"], "")
				}
			}
		}
		files = append(files, item)
	}
	return map[string]any{"recordings": files}, nil
}

func (a *App) apiRecordingDelete(r *http.Request) (any, error) {
	name, ok := safeFile(r.PathValue("filename"))
	if !ok {
		return nil, httpErr(404, "Not found")
	}
	if err := os.Remove(filepath.Join(a.recordingsDir(), name)); err != nil {
		return nil, httpErr(404, "Not found")
	}
	return map[string]any{"success": true}, nil
}

// ---------------------------------------------------------------- playback

type player struct {
	mu       sync.Mutex
	active   bool
	filename string
	progress int
	total    int
	speed    float64
	loop     bool
	stop     bool
}

func (p *player) requestStop() {
	p.mu.Lock()
	p.stop = true
	p.mu.Unlock()
}

func (p *player) status() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var fn any
	if p.filename != "" {
		fn = p.filename
	}
	speed := p.speed
	if speed == 0 {
		speed = 1.0
	}
	return map[string]any{"active": p.active, "filename": fn, "progress": p.progress, "total": p.total, "speed": speed, "loop": p.loop}
}

func (a *App) apiPlaybackStart(r *http.Request) (any, error) {
	p := a.play
	p.mu.Lock()
	busy := p.active
	p.mu.Unlock()
	if busy {
		return map[string]any{"success": false, "message": "Already playing"}, nil
	}
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	name, ok := safeFile(config.Str(data["filename"], ""))
	if !ok {
		return nil, httpErr(404, "File not found")
	}
	path := filepath.Join(a.recordingsDir(), name)
	if _, err := os.Stat(path); err != nil {
		return nil, httpErr(404, "File not found")
	}
	speed := config.Float(data["speed"], 1.0)
	if speed <= 0 {
		speed = 1.0
	}
	loop := config.Bool(data["loop"], false)
	p.mu.Lock()
	p.stop, p.filename, p.speed, p.loop, p.progress, p.active = false, name, speed, loop, 0, true
	p.mu.Unlock()
	go a.playbackWorker(path, speed, loop)
	return map[string]any{"success": true, "filename": name}, nil
}

func (a *App) playbackWorker(path string, speed float64, loop bool) {
	defer logx.Recover("playback")
	p := a.play
	defer func() {
		p.mu.Lock()
		p.active, p.progress = false, 0
		p.mu.Unlock()
	}()
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var data struct {
		Messages []recMsg `json:"messages"`
	}
	if json.Unmarshal(b, &data) != nil {
		return
	}
	p.mu.Lock()
	p.total = len(data.Messages)
	p.mu.Unlock()
	if len(data.Messages) == 0 {
		return
	}
	client, err := osc.NewClient("127.0.0.1", config.Int(a.Cfg.Get("osc", "listen_port"), 9001))
	if err != nil {
		logx.Warn("[playback] %v", err)
		return
	}
	defer client.Close()
	stopped := func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.stop
	}
	for {
		start := time.Now()
		for i, m := range data.Messages {
			if stopped() {
				return
			}
			target := time.Duration(m.T / speed * float64(time.Millisecond))
			if d := target - time.Since(start); d > 0 {
				time.Sleep(d)
			}
			args := make([]any, 0, len(m.Args))
			for _, x := range m.Args {
				// JSON numbers are float64; python-osc playback sends ints as floats too.
				args = append(args, x)
			}
			_ = client.Send(m.Addr, args...)
			p.mu.Lock()
			p.progress = i + 1
			p.mu.Unlock()
		}
		if !loop {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ---------------------------------------------------------------- chatbox

type chatboxSender struct {
	a      *App
	mu     sync.Mutex
	cancel chan struct{}
}

func (c *chatboxSender) start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return
	}
	ch := make(chan struct{})
	c.cancel = ch
	go c.loop(ch)
}

func (c *chatboxSender) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		close(c.cancel)
		c.cancel = nil
	}
}

// formatTemplate implements the subset of str.format used by templates:
// {name} placeholders and {{ / }} escapes.
func formatTemplate(tpl string, vars map[string]any) string {
	var sb strings.Builder
	for i := 0; i < len(tpl); i++ {
		ch := tpl[i]
		if ch == '{' {
			if i+1 < len(tpl) && tpl[i+1] == '{' {
				sb.WriteByte('{')
				i++
				continue
			}
			end := strings.IndexByte(tpl[i:], '}')
			if end < 0 {
				return "[template error: Single '{' encountered in format string]"
			}
			key := tpl[i+1 : i+end]
			if j := strings.IndexAny(key, ":!"); j >= 0 {
				key = key[:j]
			}
			v, ok := vars[key]
			if !ok {
				return "[template error: '" + key + "']"
			}
			sb.WriteString(config.Str(v, ""))
			i += end
			continue
		}
		if ch == '}' {
			if i+1 < len(tpl) && tpl[i+1] == '}' {
				i++
			} else {
				return "[template error: Single '}' encountered in format string]"
			}
		}
		sb.WriteByte(ch)
	}
	return sb.String()
}

func (c *chatboxSender) loop(done chan struct{}) {
	defer logx.Recover("chatbox")
	a := c.a
	logx.Info("[chatbox] Sender started")
	defer logx.Info("[chatbox] Sender stopped")
	var client *osc.Client
	last := ""
	defer func() {
		if client != nil {
			client.Close()
		}
	}()
	wait := func(d time.Duration) bool {
		select {
		case <-done:
			return false
		case <-time.After(d):
			return true
		}
	}
	for {
		cfg := config.AsMap(a.Cfg.Get("chatbox"))
		if !config.Bool(cfg["enabled"], false) {
			if !wait(time.Second) {
				return
			}
			continue
		}
		host := config.Str(cfg["target_host"], "127.0.0.1")
		port := config.Int(cfg["target_port"], 9000)
		interval := config.Float(cfg["interval_seconds"], 3.0)
		if interval < 0.5 {
			interval = 0.5
		}
		target := host + ":" + config.Str(port, "")
		if client == nil || target != last {
			if client != nil {
				client.Close()
			}
			var err error
			client, err = osc.NewClient(host, port)
			if err != nil {
				logx.Error("[chatbox] Sender error: %v", err)
				client = nil
				if !wait(3 * time.Second) {
					return
				}
				continue
			}
			last = target
		}
		var vars map[string]any
		a.Cfg.Read(func(_, basic config.M) {
			sa := orZero(config.Get(basic, "dglab3", "channel_a", "strength_limit"))
			sb := orZero(config.Get(basic, "dglab3", "channel_b", "strength_limit"))
			vars = map[string]any{
				"strength_a": sa, "strength_b": sb,
				"strength_limit_a": sa, "strength_limit_b": sb,
				"mode_a":       config.Str(config.Get(basic, "dglab3", "channel_a", "mode"), "distance"),
				"mode_b":       config.Str(config.Get(basic, "dglab3", "channel_b", "mode"), "distance"),
				"device_count": a.Hub.Count(),
				"app_version":  Version,
			}
		})
		msg := formatTemplate(config.Str(cfg["message_template"], ""), vars)
		if strings.TrimSpace(msg) != "" {
			if err := client.Send("/chatbox/input", msg, config.Bool(cfg["send_notification"], true), config.Bool(cfg["trigger_sfx"], false)); err != nil {
				logx.Warn("[chatbox] OSC send failed: %v", err)
			}
		}
		if !wait(time.Duration(interval * float64(time.Second))) {
			return
		}
	}
}

// StartChatboxIfEnabled is called at startup.
func (a *App) StartChatboxIfEnabled() {
	if config.Bool(a.Cfg.Get("chatbox", "enabled"), false) {
		a.chatbox.start()
	}
}

func (a *App) apiChatboxGet(*http.Request) (any, error) {
	if m := config.AsMap(a.Cfg.Get("chatbox")); m != nil {
		return m, nil
	}
	return config.DefaultAdvanced()["chatbox"], nil
}

func (a *App) apiChatboxSet(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	var out config.M
	a.Cfg.Write(func(adv, _ config.M) {
		c := config.Sub(adv, true, "chatbox")
		if v, ok := data["enabled"]; ok {
			c["enabled"] = config.Bool(v, false)
		}
		if v, ok := data["target_host"]; ok {
			h := strings.TrimSpace(config.Str(v, ""))
			if h == "" {
				h = "127.0.0.1"
			}
			c["target_host"] = h
		}
		if v, ok := data["target_port"]; ok {
			c["target_port"] = config.Int(v, 9000)
		}
		if v, ok := data["interval_seconds"]; ok {
			f := config.Float(v, 3.0)
			if f < 0.5 {
				f = 0.5
			}
			c["interval_seconds"] = f
		}
		if v, ok := data["send_notification"]; ok {
			c["send_notification"] = config.Bool(v, true)
		}
		if v, ok := data["trigger_sfx"]; ok {
			c["trigger_sfx"] = config.Bool(v, false)
		}
		if v, ok := data["message_template"]; ok {
			c["message_template"] = config.Str(v, "")
		}
		out = config.CopyMap(c)
	})
	if err := a.Cfg.Save(); err != nil {
		return nil, err
	}
	a.chatbox.stop()
	if config.Bool(out["enabled"], false) {
		a.chatbox.start()
	}
	return map[string]any{"success": true, "chatbox": out}, nil
}

func (a *App) apiChatboxTest(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	msg := config.Str(data["message"], "Shocking VRChat test ⚡")
	host := config.Str(data["target_host"], config.Str(a.Cfg.Get("chatbox", "target_host"), "127.0.0.1"))
	port := config.Int(data["target_port"], config.Int(a.Cfg.Get("chatbox", "target_port"), 9000))
	if err := osc.SendOnce(host, port, "/chatbox/input", msg, true, false); err != nil {
		return map[string]any{"success": false, "error": err.Error()}, nil
	}
	return map[string]any{"success": true, "message": msg}, nil
}
