// Package queue is a bounded priority command queue with per-source cooldown.
package queue

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"
)

// Priority: lower value = higher priority.
type Priority int

const (
	API Priority = iota
	OSCPanel
	OSCInteraction
	Game
)

func (p Priority) String() string {
	switch p {
	case API:
		return "API"
	case OSCPanel:
		return "OSC_PANEL"
	case OSCInteraction:
		return "OSC_INTERACTION"
	case Game:
		return "GAME"
	}
	return fmt.Sprintf("P%d", int(p))
}

// Command is a queued action.
type Command struct {
	Priority Priority
	Time     time.Time
	seq      uint64
	Channel  string
	Action   string // "wave", "clear", "osc_trigger"
	Value    string // wavestr for "wave"
	Fn       func() // callback for "osc_trigger"
	SourceID string
}

type cmdHeap []*Command

func (h cmdHeap) Len() int { return len(h) }
func (h cmdHeap) Less(i, j int) bool {
	if h[i].Priority != h[j].Priority {
		return h[i].Priority < h[j].Priority
	}
	if !h[i].Time.Equal(h[j].Time) {
		return h[i].Time.Before(h[j].Time)
	}
	return h[i].seq < h[j].seq
}
func (h cmdHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *cmdHeap) Push(x any)   { *h = append(*h, x.(*Command)) }
func (h *cmdHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// DefaultMaxSize bounds the backlog so an OSC flood cannot grow memory.
const DefaultMaxSize = 512

// Queue is safe for concurrent use.
type Queue struct {
	mu        sync.Mutex
	h         cmdHeap
	maxSize   int
	seq       uint64
	lastTime  map[string]time.Time
	cooldowns map[Priority]time.Duration
	enabled   map[Priority]bool
	dropped   int
	notify    chan struct{}
	// deferred holds the newest command per source that arrived during its
	// cooldown; it is enqueued when the cooldown ends, so the final value
	// of a burst (e.g. a parameter returning to 0) is never lost.
	deferred map[string]*Command
}

// New creates a queue (maxSize<=0 -> DefaultMaxSize).
func New(maxSize int) *Queue {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	return &Queue{
		maxSize:  maxSize,
		lastTime: map[string]time.Time{},
		cooldowns: map[Priority]time.Duration{
			API:            0,
			OSCPanel:       100 * time.Millisecond,
			OSCInteraction: 20 * time.Millisecond,
			Game:           200 * time.Millisecond,
		},
		enabled:  map[Priority]bool{API: true, OSCPanel: true, OSCInteraction: true, Game: true},
		notify:   make(chan struct{}, 1),
		deferred: map[string]*Command{},
	}
}

// SetCooldown overrides a priority's cooldown.
func (q *Queue) SetCooldown(p Priority, d time.Duration) {
	q.mu.Lock()
	q.cooldowns[p] = d
	q.mu.Unlock()
}

// SetEnabled enables/disables a priority class.
func (q *Queue) SetEnabled(p Priority, on bool) {
	q.mu.Lock()
	q.enabled[p] = on
	q.mu.Unlock()
}

// Dropped returns the number of commands dropped because the queue was full.
func (q *Queue) Dropped() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

// Put enqueues c unless its priority is disabled or the queue is full.
// If its source is in cooldown, c replaces any earlier deferred command of
// that source and is enqueued when the cooldown ends; Put then returns
// false (not enqueued now). Returns true if enqueued immediately.
func (q *Queue) Put(c *Command) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if on, ok := q.enabled[c.Priority]; ok && !on {
		return false
	}
	now := time.Now()
	key := c.Priority.String() + "_" + c.SourceID
	if last, ok := q.lastTime[key]; ok {
		if wait := q.cooldowns[c.Priority] - now.Sub(last); wait > 0 {
			if _, pending := q.deferred[key]; !pending {
				time.AfterFunc(wait, func() { q.flushDeferred(key) })
			}
			q.deferred[key] = c
			return false
		}
	}
	return q.pushLocked(key, c, now)
}

func (q *Queue) flushDeferred(key string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	c := q.deferred[key]
	if c == nil {
		return // cleared meanwhile
	}
	delete(q.deferred, key)
	if on, ok := q.enabled[c.Priority]; ok && !on {
		return
	}
	q.pushLocked(key, c, time.Now())
}

// pushLocked must be called with q.mu held.
func (q *Queue) pushLocked(key string, c *Command, now time.Time) bool {
	q.lastTime[key] = now
	if len(q.h) >= q.maxSize {
		q.dropped++
		return false
	}
	q.seq++
	c.seq = q.seq
	c.Time = now
	heap.Push(&q.h, c)
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return true
}

// PutWave is a convenience for enqueueing a wave command.
func (q *Queue) PutWave(p Priority, channel, wavestr, source string) bool {
	return q.Put(&Command{Priority: p, Channel: channel, Action: "wave", Value: wavestr, SourceID: source})
}

// Get blocks until a command is available or ctx is done (nil, ctx.Err()).
func (q *Queue) Get(ctx context.Context) (*Command, error) {
	for {
		q.mu.Lock()
		if len(q.h) > 0 {
			c := heap.Pop(&q.h).(*Command)
			q.mu.Unlock()
			return c, nil
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-q.notify:
		}
	}
}

// Len returns the number of pending commands.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.h)
}

// Clear discards pending commands and cooldown state; returns count drained.
func (q *Queue) Clear() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.h)
	q.h = nil
	q.lastTime = map[string]time.Time{}
	q.deferred = map[string]*Command{}
	return n
}
