package app

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"

	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
)

// Optional Bluetooth (BLE) heart-rate receiver (off by default). Any device
// using the standard GATT Heart Rate service (0x180D) works: chest straps,
// many watches in "broadcast heart rate" mode, phone/watch apps that
// broadcast. No account or token needed. The value is published like the
// Pulsoid one (HeartRate/* parameters, optional VRCOSC-compatible output).

const (
	bleScanTimeout = 15 * time.Second
	bleStaleAfter  = 15 * time.Second
)

type bleDevice struct {
	Address string  `json:"address"`
	Name    string  `json:"name"`
	RSSI    int16   `json:"rssi"`
	HR      bool    `json:"heart_rate"` // advertises the Heart Rate service
	Seen    float64 `json:"seen"`
}

type bleHRState struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	status    string // off | scanning | connecting | connected | error
	device    string // connected device name/address
	bpm       int
	lastAt    time.Time
	err       string
	found     map[string]bleDevice
	scanUntil time.Time
}

var (
	bleAdapter    = bluetooth.DefaultAdapter
	bleEnableOnce sync.Once
	bleEnableErr  error
	bleEnabled    = make(chan struct{})
	bleScanMu     sync.Mutex // one Scan at a time (adapter limitation)
)

// bleEnable powers up the adapter once. A Bluetooth stack that never
// answers must not hang the receiver or shutdown: wait at most 10 s.
func bleEnable(ctx context.Context) error {
	bleEnableOnce.Do(func() {
		go func() {
			defer close(bleEnabled)
			defer logx.Recover("ble enable")
			bleEnableErr = bleAdapter.Enable()
		}()
	})
	select {
	case <-bleEnabled:
		return bleEnableErr
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Second):
		return errors.New("the Bluetooth adapter does not respond")
	}
}

// parseHRMeasurement decodes a Heart Rate Measurement (0x2A37) value:
// flags bit 0 selects uint8 or uint16 (little endian) BPM.
func parseHRMeasurement(b []byte) (int, bool) {
	if len(b) < 2 {
		return 0, false
	}
	if b[0]&1 == 0 {
		return int(b[1]), b[1] > 0
	}
	if len(b) < 3 {
		return 0, false
	}
	v := int(binary.LittleEndian.Uint16(b[1:3]))
	return v, v > 0
}

func (a *App) bleCfg() config.M { return config.AsMap(a.Cfg.Get("ble_hr")) }

func (a *App) setBLE(f func(s *bleHRState)) {
	a.ble.mu.Lock()
	f(&a.ble)
	a.ble.mu.Unlock()
}

func (a *App) bleRemember(r bluetooth.ScanResult) {
	addr := r.Address.String()
	d := bleDevice{Address: addr, Name: r.LocalName(), RSSI: r.RSSI,
		HR: r.HasServiceUUID(bluetooth.ServiceUUIDHeartRate), Seen: float64(time.Now().UnixMilli()) / 1000}
	a.setBLE(func(s *bleHRState) {
		if s.found == nil {
			s.found = map[string]bleDevice{}
		}
		if old, ok := s.found[addr]; ok {
			if d.Name == "" {
				d.Name = old.Name
			}
			d.HR = d.HR || old.HR
		}
		s.found[addr] = d
	})
}

// StartBLEHRIfEnabled (re)starts the receiver according to the config.
func (a *App) StartBLEHRIfEnabled() {
	a.stopBLEHR()
	if !config.Bool(a.bleCfg()["enabled"], false) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.setBLE(func(s *bleHRState) { s.cancel, s.done, s.err, s.status = cancel, done, "", "scanning" })
	go func() {
		defer close(done)
		defer logx.Recover("ble heart rate")
		a.bleLoop(ctx)
	}()
}

func (a *App) stopBLEHR() {
	var cancel context.CancelFunc
	var done chan struct{}
	a.setBLE(func(s *bleHRState) { cancel, done = s.cancel, s.done; s.cancel, s.done, s.status = nil, nil, "off" })
	if cancel != nil {
		cancel()
		if isBLEEnabled() {
			_ = bleAdapter.StopScan()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second): // a stuck driver must not block shutdown
			logx.Warn("[ble] Receiver did not stop in time")
		}
		a.publishHR("ble", a.bleCfg(), 0, false)
	}
}

func isBLEEnabled() bool {
	select {
	case <-bleEnabled:
		return bleEnableErr == nil
	default:
		return false
	}
}

func (a *App) bleLoop(ctx context.Context) {
	if err := bleEnable(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		a.setBLE(func(s *bleHRState) { s.status, s.err = "error", "Bluetooth adapter not available: "+err.Error() })
		logx.Error("[ble] Bluetooth adapter not available: %v", err)
		return
	}
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := a.bleSession(ctx)
		if ctx.Err() != nil {
			return
		}
		a.publishHR("ble", a.bleCfg(), 0, false)
		a.setBLE(func(s *bleHRState) { s.status, s.err = "scanning", err.Error() })
		if time.Since(started) > time.Minute {
			backoff = 2 * time.Second
		}
		logx.Warn("[ble] %v; retrying in %s", err, backoff)
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

// bleFind scans until the configured device (or, with none configured,
// the first heart-rate device) is seen.
func (a *App) bleFind(ctx context.Context) (bluetooth.ScanResult, error) {
	want := strings.ToUpper(strings.TrimSpace(config.Str(a.bleCfg()["device"], "")))
	var hit bluetooth.ScanResult
	found := false
	bleScanMu.Lock()
	defer bleScanMu.Unlock()
	a.setBLE(func(s *bleHRState) { s.status = "scanning" })
	stop := time.AfterFunc(bleScanTimeout, func() { _ = bleAdapter.StopScan() })
	defer stop.Stop()
	go func() { <-ctx.Done(); _ = bleAdapter.StopScan() }()
	err := bleAdapter.Scan(func(ad *bluetooth.Adapter, r bluetooth.ScanResult) {
		a.bleRemember(r)
		addr := strings.ToUpper(r.Address.String())
		if (want != "" && addr == want) || (want == "" && r.HasServiceUUID(bluetooth.ServiceUUIDHeartRate)) {
			hit, found = r, true
			_ = ad.StopScan()
		}
	})
	if ctx.Err() != nil {
		return hit, ctx.Err()
	}
	if err != nil {
		return hit, errors.New("scan failed: " + err.Error())
	}
	if !found {
		if want != "" {
			return hit, errors.New("device " + want + " not found (is it broadcasting heart rate?)")
		}
		return hit, errors.New("no heart-rate device found")
	}
	return hit, nil
}

func (a *App) bleSession(ctx context.Context) error {
	r, err := a.bleFind(ctx)
	if err != nil {
		return err
	}
	name := r.LocalName()
	if name == "" {
		name = r.Address.String()
	}
	a.setBLE(func(s *bleHRState) { s.status, s.device = "connecting", name })
	lost := make(chan struct{}, 1)
	bleAdapter.SetConnectionStateHandler(func(_ bluetooth.Device, st bluetooth.ConnectionState) {
		if !st.Connected {
			select {
			case lost <- struct{}{}:
			default:
			}
		}
	})
	dev, err := bleAdapter.Connect(r.Address, bluetooth.ConnectionParams{})
	if err != nil {
		return errors.New("connect failed: " + err.Error())
	}
	defer func() { _ = dev.Disconnect() }()
	svcs, err := dev.DiscoverServices([]bluetooth.UUID{bluetooth.ServiceUUIDHeartRate})
	if err != nil || len(svcs) == 0 {
		return errors.New(name + " has no heart-rate service")
	}
	chars, err := svcs[0].DiscoverCharacteristics([]bluetooth.UUID{bluetooth.CharacteristicUUIDHeartRateMeasurement})
	if err != nil || len(chars) == 0 {
		return errors.New(name + " has no heart-rate measurement")
	}
	beat := make(chan struct{}, 1)
	err = chars[0].EnableNotifications(func(buf []byte) {
		bpm, ok := parseHRMeasurement(buf)
		if !ok {
			return
		}
		a.setBLE(func(s *bleHRState) { s.bpm, s.lastAt, s.status, s.err = bpm, time.Now(), "connected", "" })
		a.publishHR("ble", a.bleCfg(), bpm, true)
		select {
		case beat <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return errors.New("subscribe failed: " + err.Error())
	}
	logx.Success("[ble] Connected to %s", name)
	a.setBLE(func(s *bleHRState) { s.status = "connected" })
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-lost:
			return errors.New(name + " disconnected")
		case <-beat:
		case <-time.After(bleStaleAfter):
			return errors.New("no heart rate from " + name + " for 15 s")
		}
	}
}

// ---------------------------------------------------------------- API

func (a *App) apiBLEHRGet(*http.Request) (any, error) {
	c := a.bleCfg()
	a.ble.mu.Lock()
	defer a.ble.mu.Unlock()
	devs := make([]bleDevice, 0, len(a.ble.found))
	for _, d := range a.ble.found {
		devs = append(devs, d)
	}
	sort.Slice(devs, func(i, j int) bool {
		if devs[i].HR != devs[j].HR {
			return devs[i].HR
		}
		return devs[i].RSSI > devs[j].RSSI
	})
	var last any
	if !a.ble.lastAt.IsZero() {
		last = float64(a.ble.lastAt.UnixMilli()) / 1000
	}
	status := a.ble.status
	if status == "" {
		status = "off"
	}
	return map[string]any{
		"enabled": config.Bool(c["enabled"], false), "device": config.Str(c["device"], ""),
		"device_name": config.Str(c["device_name"], ""),
		"min_bpm":     config.Float(c["min_bpm"], 40), "max_bpm": config.Float(c["max_bpm"], 200),
		"send_to_vrchat": config.Bool(c["send_to_vrchat"], true),
		"status":         status, "connected_to": a.ble.device, "bpm": a.ble.bpm, "last_at": last, "error": a.ble.err,
		"scanning": time.Now().Before(a.ble.scanUntil), "devices": devs,
		"params": []string{hrParamPrefix + "HeartRate", hrParamPrefix + "BPM", hrParamPrefix + "Connected"},
	}, nil
}

func (a *App) apiBLEHRSet(r *http.Request) (any, error) {
	data, err := readBody(r)
	if err != nil {
		return nil, err
	}
	var bad error
	a.Cfg.Write(func(adv, _ config.M) {
		c := config.Sub(adv, true, "ble_hr")
		if v, ok := data["enabled"]; ok {
			c["enabled"] = config.Bool(v, false)
		}
		if v, ok := data["device"]; ok {
			c["device"] = strings.ToUpper(strings.TrimSpace(config.Str(v, "")))
		}
		if v, ok := data["device_name"]; ok {
			c["device_name"] = truncRunes(config.Str(v, ""), 60)
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
	a.StartBLEHRIfEnabled()
	return a.apiBLEHRGet(r)
}

// apiBLEHRScan lists nearby devices for picking one (runs in background;
// results appear in GET). While the receiver itself is scanning or
// connected, its own scan results are shown instead.
func (a *App) apiBLEHRScan(r *http.Request) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := bleEnable(ctx); err != nil {
		return nil, httpErr(400, "Bluetooth adapter not available: %v", err)
	}
	a.setBLE(func(s *bleHRState) { s.scanUntil = time.Now().Add(10 * time.Second) })
	go func() {
		defer logx.Recover("ble scan")
		if !bleScanMu.TryLock() {
			return // the receiver is scanning; it records devices too
		}
		defer bleScanMu.Unlock()
		t := time.AfterFunc(10*time.Second, func() { _ = bleAdapter.StopScan() })
		defer t.Stop()
		if err := bleAdapter.Scan(func(_ *bluetooth.Adapter, r bluetooth.ScanResult) { a.bleRemember(r) }); err != nil {
			logx.Warn("[ble] Scan failed: %v", err)
		}
	}()
	return a.apiBLEHRGet(r)
}
