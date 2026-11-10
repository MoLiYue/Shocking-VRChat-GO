// Package device implements DG-LAB Coyote connections (V3 socket protocol
// and V4 relay protocol) behind a common interface plus broadcast helpers.
package device

import (
	"fmt"
	"sync"
	"sync/atomic"

	"shockingvrc/internal/logx"
)

// Status is a snapshot of a device's per-channel state.
type Status struct {
	Strength        map[string]int  `json:"strength"`
	StrengthMax     map[string]int  `json:"strength_max"`
	StrengthLimit   map[string]int  `json:"strength_limit"`
	LimitBoost      map[string]int  `json:"limit_boost"`
	OverlimitActive map[string]bool `json:"overlimit_active"`
	UUID            string          `json:"uuid"`
}

// Device is a connected Coyote (via the DG-LAB APP).
type Device interface {
	ID() string
	Status() Status
	// SetStrength: mode "0"=sub, "1"=add, "2"=set. Without force the value
	// is range-checked and "set" is clamped to the effective upper limit.
	SetStrength(ch, mode string, value int, force bool) error
	SendWave(ch, wavestr string) error
	ClearWave(ch string) error
	Close()
	state() *chanState
}

// Limits is the per-channel strength cap configuration.
type Limits struct {
	LimitA, LimitB int
}

// chanState is the strength bookkeeping shared by V3 and V4.
type chanState struct {
	mu              sync.Mutex
	strength        map[string]int
	strengthMax     map[string]int
	strengthLimit   map[string]int
	overlimitActive map[string]bool
	// limitBoost is the temporary cap increase from overlimit rules
	// (added on top of strengthLimit, decays back to 0 over time).
	limitBoost map[string]int
	// boosted is the strength actually added by an active boost-mode
	// handler (after capping), so reverting subtracts exactly that.
	boosted map[string]int
}

func newChanState(l Limits, initialMax int) *chanState {
	return &chanState{
		strength:        map[string]int{"A": 0, "B": 0},
		strengthMax:     map[string]int{"A": initialMax, "B": initialMax},
		strengthLimit:   map[string]int{"A": l.LimitA, "B": l.LimitB},
		overlimitActive: map[string]bool{"A": false, "B": false},
		limitBoost:      map[string]int{"A": 0, "B": 0},
		boosted:         map[string]int{"A": 0, "B": 0},
	}
}

// upperLocked returns min(strength_max, limit + overlimit boost, 200).
func (s *chanState) upperLocked(ch string) int {
	limit := min(200, s.strengthLimit[ch]+max(0, s.limitBoost[ch]))
	if s.strengthMax[ch] < limit {
		return s.strengthMax[ch]
	}
	return limit
}

func (s *chanState) Upper(ch string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upperLocked(ch)
}

func cp[K comparable, V any](m map[K]V) map[K]V {
	o := make(map[K]V, len(m))
	for k, v := range m {
		o[k] = v
	}
	return o
}

func (s *chanState) snapshot(id string) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		Strength: cp(s.strength), StrengthMax: cp(s.strengthMax), StrengthLimit: cp(s.strengthLimit),
		LimitBoost: cp(s.limitBoost), OverlimitActive: cp(s.overlimitActive), UUID: id,
	}
}

// Hub tracks all connected devices (srv.WS_CONNECTIONS) and fans out commands.
type Hub struct {
	mu            sync.RWMutex
	devices       map[Device]struct{}
	order         []Device
	suppressClear atomic.Bool
	WaveObserver  func(ch, wavestr string)
	ClearObserver func(ch string)
	limits        atomic.Pointer[Limits]
	limitBoost    atomic.Pointer[[2]int]
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	h := &Hub{devices: map[Device]struct{}{}}
	h.limits.Store(&Limits{LimitA: 5, LimitB: 5})
	return h
}

// Limits returns the current configured limits.
func (h *Hub) Limits() Limits { return *h.limits.Load() }

// SetSuppressClear toggles clear suppression (used during wave tests).
func (h *Hub) SetSuppressClear(v bool) { h.suppressClear.Store(v) }

// SuppressClear reports the clear suppression flag.
func (h *Hub) SuppressClear() bool { return h.suppressClear.Load() }

func (h *Hub) add(d Device) {
	if b := h.limitBoost.Load(); b != nil {
		s := d.state()
		s.mu.Lock()
		s.limitBoost["A"], s.limitBoost["B"] = b[0], b[1]
		s.overlimitActive["A"], s.overlimitActive["B"] = b[0] > 0, b[1] > 0
		s.mu.Unlock()
	}
	h.mu.Lock()
	if _, ok := h.devices[d]; !ok {
		h.devices[d] = struct{}{}
		h.order = append(h.order, d)
	}
	h.mu.Unlock()
}

func (h *Hub) remove(d Device) {
	h.mu.Lock()
	if _, ok := h.devices[d]; ok {
		delete(h.devices, d)
		for i, x := range h.order {
			if x == d {
				h.order = append(h.order[:i:i], h.order[i+1:]...)
				break
			}
		}
	}
	h.mu.Unlock()
}

// Devices returns a snapshot of connected devices in connection order.
func (h *Hub) Devices() []Device {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]Device{}, h.order...)
}

// Count returns the number of connected devices.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.order)
}

// fanout runs fn against every device concurrently, logging failures.
func (h *Hub) fanout(what string, fn func(Device) error) {
	devs := h.Devices()
	if len(devs) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, d := range devs {
		wg.Add(1)
		go func(d Device) {
			defer wg.Done()
			defer logx.Recover("device " + what)
			if err := fn(d); err != nil {
				logx.Warn("[ws] Device %s %s failed: %v", d.ID(), what, err)
			}
		}(d)
	}
	wg.Wait()
}

// BroadcastWave sends a wavestr to all devices on ch.
func (h *Hub) BroadcastWave(ch, wavestr string) {
	if obs := h.WaveObserver; obs != nil {
		obs(ch, wavestr)
	}
	h.fanout("send_wave", func(d Device) error { return d.SendWave(ch, wavestr) })
}

// BroadcastClear clears ch on all devices (no-op while suppressed).
func (h *Hub) BroadcastClear(ch string) {
	if h.suppressClear.Load() {
		return
	}
	if obs := h.ClearObserver; obs != nil {
		obs(ch)
	}
	h.fanout("clear_wave", func(d Device) error { return d.ClearWave(ch) })
}

// BroadcastSetStrength calls SetStrength on all devices.
func (h *Hub) BroadcastSetStrength(ch, mode string, value int, force bool) {
	h.fanout("set_strength", func(d Device) error { return d.SetStrength(ch, mode, value, force) })
}

// BroadcastStrengthAdjust adds/subtracts strength, keeping additions inside
// the configured cap. mode: "0"=sub, "1"=add.
func (h *Hub) BroadcastStrengthAdjust(ch, mode string, value int) {
	h.fanout("strength_adjust(mode="+mode+")", func(d Device) error {
		s := d.state()
		s.mu.Lock()
		effective := value
		switch mode {
		case "1":
			effective = s.capAddLocked(d, ch, value)
		case "0":
			if effective > s.strength[ch] {
				effective = s.strength[ch]
			}
		}
		s.mu.Unlock()
		if effective <= 0 {
			return nil
		}
		return d.SetStrength(ch, mode, effective, true)
	})
}

// capAddLocked limits an addition to the headroom below the effective cap.
func (s *chanState) capAddLocked(d Device, ch string, value int) int {
	limit := s.upperLocked(ch)
	headroom := max(0, limit-s.strength[ch])
	if value > headroom {
		logx.Warn("[ws] Device %s ch=%s +%d exceeds limit %d (current %d), capped to +%d",
			d.ID(), ch, value, limit, s.strength[ch], headroom)
		return headroom
	}
	return value
}

// BroadcastBoost applies (on) or reverts (!on) a boost-mode increase.
// The amount actually added per device is remembered, so reverting never
// subtracts more than the boost added (e.g. when it was capped to 0).
func (h *Hub) BroadcastBoost(ch string, on bool, value int) {
	h.fanout(fmt.Sprintf("boost(on=%v)", on), func(d Device) error {
		s := d.state()
		s.mu.Lock()
		var mode string
		var effective int
		if on {
			mode, effective = "1", s.capAddLocked(d, ch, value)
			s.boosted[ch] += effective
		} else {
			mode, effective = "0", min(s.boosted[ch], s.strength[ch])
			s.boosted[ch] = 0
		}
		s.mu.Unlock()
		if effective <= 0 {
			return nil
		}
		return d.SetStrength(ch, mode, effective, true)
	})
}

// RefreshLimits updates limits on every device and pushes them asynchronously.
func (h *Hub) RefreshLimits(l Limits) {
	h.limits.Store(&l)
	devs := h.Devices()
	for _, d := range devs {
		s := d.state()
		s.mu.Lock()
		s.strengthLimit["A"], s.strengthLimit["B"] = l.LimitA, l.LimitB
		s.mu.Unlock()
	}
	go h.applyLimits(devs)
}

func (h *Hub) applyLimits(devs []Device) {
	defer logx.Recover("apply limits")
	for _, d := range devs {
		for _, ch := range []string{"A", "B"} {
			s := d.state()
			s.mu.Lock()
			limit := s.upperLocked(ch)
			push := s.strengthMax[ch] > 0 && s.strength[ch] != 0
			s.mu.Unlock()
			if push {
				if err := d.SetStrength(ch, "2", limit, true); err != nil {
					logx.Warn("[ws] Device %s limit push failed: %v", d.ID(), err)
				}
			}
		}
	}
}

// SetLimitBoost sets the overlimit cap increase per channel on every
// device and pushes the resulting caps synchronously (callers serialize,
// so pushes are never reordered).
func (h *Hub) SetLimitBoost(boostA, boostB int) {
	h.limitBoost.Store(&[2]int{boostA, boostB})
	devs := h.Devices()
	for _, d := range devs {
		s := d.state()
		s.mu.Lock()
		s.limitBoost["A"], s.limitBoost["B"] = boostA, boostB
		s.overlimitActive["A"], s.overlimitActive["B"] = boostA > 0, boostB > 0
		s.mu.Unlock()
	}
	h.applyLimits(devs)
}

// CloseAll disconnects every device.
func (h *Hub) CloseAll() {
	for _, d := range h.Devices() {
		d.Close()
	}
}
