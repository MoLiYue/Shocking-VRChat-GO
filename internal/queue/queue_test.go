package queue

import (
	"context"
	"testing"
	"time"
)

func TestPriorityOrderAndFIFO(t *testing.T) {
	q := New(0)
	q.PutWave(Game, "A", "g", "1")
	q.PutWave(API, "A", "a1", "1")
	q.PutWave(OSCInteraction, "A", "o", "1")
	q.PutWave(API, "A", "a2", "2")
	ctx := context.Background()
	var got []string
	for i := 0; i < 4; i++ {
		c, err := q.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, c.Value)
	}
	want := []string{"a1", "a2", "o", "g"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v want %v", got, want)
		}
	}
}

func TestCooldownAndDisable(t *testing.T) {
	q := New(0)
	if !q.PutWave(OSCInteraction, "A", "x", "s") {
		t.Fatal("first put rejected")
	}
	if q.PutWave(OSCInteraction, "A", "x", "s") {
		t.Fatal("cooldown not applied")
	}
	if !q.PutWave(OSCInteraction, "A", "x", "other") {
		t.Fatal("different source should pass")
	}
	time.Sleep(25 * time.Millisecond)
	if !q.PutWave(OSCInteraction, "A", "x", "s") {
		t.Fatal("cooldown should have expired")
	}
	q.SetEnabled(OSCInteraction, false)
	if q.PutWave(OSCInteraction, "A", "x", "z") {
		t.Fatal("disabled priority accepted")
	}
}

func TestBoundedAndClear(t *testing.T) {
	q := New(2)
	q.PutWave(API, "A", "1", "a")
	q.PutWave(API, "A", "2", "b")
	if q.PutWave(API, "A", "3", "c") || q.Dropped() != 1 {
		t.Fatal("expected drop")
	}
	if n := q.Clear(); n != 2 || q.Len() != 0 {
		t.Fatalf("clear drained %d", n)
	}
}

func TestGetUnblocksOnPutAndCancel(t *testing.T) {
	q := New(0)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan string, 1)
	go func() {
		c, err := q.Get(ctx)
		if err == nil {
			got <- c.Value
		}
	}()
	time.Sleep(10 * time.Millisecond)
	q.PutWave(API, "A", "hi", "x")
	select {
	case v := <-got:
		if v != "hi" {
			t.Fatal(v)
		}
	case <-time.After(time.Second):
		t.Fatal("Get did not unblock")
	}
	cancel()
	if _, err := q.Get(ctx); err == nil {
		t.Fatal("expected ctx error")
	}
}
