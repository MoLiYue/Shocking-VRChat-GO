package osc

import (
	"net"
	"sync"
	"testing"
	"time"
)

func TestEncodeParseRoundTrip(t *testing.T) {
	b, err := Encode("/chatbox/input", "hi ⚡", true, false, 3, 0.5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b)%4 != 0 {
		t.Fatal("not 4-byte aligned")
	}
	msgs, err := Parse(b)
	if err != nil || len(msgs) != 1 {
		t.Fatal(err)
	}
	m := msgs[0]
	if m.Address != "/chatbox/input" || len(m.Args) != 6 {
		t.Fatalf("%+v", m)
	}
	if m.Args[0] != "hi ⚡" || m.Args[1] != true || m.Args[2] != false || m.Args[3] != 3 || m.Args[4] != 0.5 || m.Args[5] != nil {
		t.Fatalf("args %#v", m.Args)
	}
}

func TestBundle(t *testing.T) {
	m1, _ := Encode("/a", 1.0)
	m2, _ := Encode("/b", true)
	b := []byte("#bundle\x00\x00\x00\x00\x00\x00\x00\x00\x01")
	for _, m := range [][]byte{m1, m2} {
		n := len(m)
		b = append(b, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		b = append(b, m...)
	}
	msgs, err := Parse(b)
	if err != nil || len(msgs) != 2 || msgs[1].Address != "/b" {
		t.Fatalf("%v %+v", err, msgs)
	}
}

func TestDispatcherWildcard(t *testing.T) {
	d := &Dispatcher{}
	var hits []string
	d.Map("/avatar/parameters/Shock/*", func(a string, _ []any) { hits = append(hits, "w:"+a) })
	d.Map("/avatar/parameters/lms-penis-proximityA*", func(a string, _ []any) { hits = append(hits, "p:"+a) })
	d.Map("/avatar/parameters/exact", func(a string, _ []any) { hits = append(hits, "e:"+a) })
	for _, a := range []string{
		"/avatar/parameters/Shock/TouchAreaA",
		"/avatar/parameters/Shock/wildcard/x", // python-osc '*' also spans '/'
		"/avatar/parameters/lms-penis-proximityA1",
		"/avatar/parameters/exact",
		"/avatar/parameters/exactly",
		"/other",
	} {
		d.Dispatch(Message{Address: a})
	}
	want := []string{
		"w:/avatar/parameters/Shock/TouchAreaA",
		"w:/avatar/parameters/Shock/wildcard/x",
		"p:/avatar/parameters/lms-penis-proximityA1",
		"e:/avatar/parameters/exact",
	}
	if len(hits) != len(want) {
		t.Fatalf("hits %v", hits)
	}
	for i := range want {
		if hits[i] != want[i] {
			t.Fatalf("hits %v", hits)
		}
	}
}

func TestServerClient(t *testing.T) {
	d := &Dispatcher{}
	var mu sync.Mutex
	got := make(chan float64, 1)
	d.Map("/x", func(_ string, args []any) {
		mu.Lock()
		defer mu.Unlock()
		got <- args[0].(float64)
	})
	s, err := Listen("127.0.0.1", 0, d)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	addr := s.LocalAddr().(*net.UDPAddr)
	if err := SendOnce("127.0.0.1", addr.Port, "/x", 0.25); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if v != 0.25 {
			t.Fatal(v)
		}
	case <-time.After(time.Second):
		t.Fatal("no message")
	}
}
