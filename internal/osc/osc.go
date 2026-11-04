// Package osc implements the subset of OSC 1.0 needed for VRChat:
// message/bundle decoding, message encoding, a UDP server with an
// address dispatcher, and a UDP client.
package osc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"shockingvrc/internal/logx"
)

// Message is a decoded OSC message. Args hold int, int64, float64, bool,
// string, []byte or nil.
type Message struct {
	Address string
	Args    []any
}

var errShort = errors.New("osc: packet too short")

func readString(b []byte, off int) (string, int, error) {
	end := bytes.IndexByte(b[off:], 0)
	if end < 0 {
		return "", 0, errShort
	}
	s := string(b[off : off+end])
	n := end + 1
	n += (4 - n%4) % 4
	if off+n > len(b) {
		n = len(b) - off
	}
	return s, off + n, nil
}

// Parse decodes a packet (message or bundle) into messages.
func Parse(b []byte) ([]Message, error) {
	if len(b) == 0 {
		return nil, errShort
	}
	if bytes.HasPrefix(b, []byte("#bundle\x00")) {
		var out []Message
		off := 16 // "#bundle\0" + 8 byte timetag
		for off+4 <= len(b) {
			size := int(binary.BigEndian.Uint32(b[off:]))
			off += 4
			if size < 0 || off+size > len(b) {
				return out, errShort
			}
			msgs, err := Parse(b[off : off+size])
			if err != nil {
				return out, err
			}
			out = append(out, msgs...)
			off += size
		}
		return out, nil
	}
	m, err := parseMessage(b)
	if err != nil {
		return nil, err
	}
	return []Message{m}, nil
}

func parseMessage(b []byte) (Message, error) {
	addr, off, err := readString(b, 0)
	if err != nil {
		return Message{}, err
	}
	if !strings.HasPrefix(addr, "/") {
		return Message{}, fmt.Errorf("osc: bad address %q", addr)
	}
	m := Message{Address: addr}
	if off >= len(b) {
		return m, nil
	}
	tags, off, err := readString(b, off)
	if err != nil {
		return m, err
	}
	if !strings.HasPrefix(tags, ",") {
		return m, nil
	}
	for _, t := range tags[1:] {
		switch t {
		case 'i':
			if off+4 > len(b) {
				return m, errShort
			}
			m.Args = append(m.Args, int(int32(binary.BigEndian.Uint32(b[off:]))))
			off += 4
		case 'f':
			if off+4 > len(b) {
				return m, errShort
			}
			m.Args = append(m.Args, float64(math.Float32frombits(binary.BigEndian.Uint32(b[off:]))))
			off += 4
		case 'h':
			if off+8 > len(b) {
				return m, errShort
			}
			m.Args = append(m.Args, int64(binary.BigEndian.Uint64(b[off:])))
			off += 8
		case 'd':
			if off+8 > len(b) {
				return m, errShort
			}
			m.Args = append(m.Args, math.Float64frombits(binary.BigEndian.Uint64(b[off:])))
			off += 8
		case 't':
			if off+8 > len(b) {
				return m, errShort
			}
			m.Args = append(m.Args, int64(binary.BigEndian.Uint64(b[off:])))
			off += 8
		case 's', 'S':
			var s string
			s, off, err = readString(b, off)
			if err != nil {
				return m, err
			}
			m.Args = append(m.Args, s)
		case 'b':
			if off+4 > len(b) {
				return m, errShort
			}
			n := int(binary.BigEndian.Uint32(b[off:]))
			off += 4
			if n < 0 || off+n > len(b) {
				return m, errShort
			}
			m.Args = append(m.Args, append([]byte{}, b[off:off+n]...))
			off += n + (4-n%4)%4
		case 'c', 'r', 'm':
			if off+4 > len(b) {
				return m, errShort
			}
			m.Args = append(m.Args, int(binary.BigEndian.Uint32(b[off:])))
			off += 4
		case 'T':
			m.Args = append(m.Args, true)
		case 'F':
			m.Args = append(m.Args, false)
		case 'N', 'I':
			m.Args = append(m.Args, nil)
		case '[', ']':
			// arrays are flattened
		default:
			return m, fmt.Errorf("osc: unsupported type tag %q", t)
		}
	}
	return m, nil
}

func writeString(buf *bytes.Buffer, s string) {
	buf.WriteString(s)
	buf.WriteByte(0)
	for buf.Len()%4 != 0 {
		buf.WriteByte(0)
	}
}

// Encode serializes a message. Supported arg types: bool, int, int32,
// int64, float32, float64, string, []byte, nil.
func Encode(addr string, args ...any) ([]byte, error) {
	var buf bytes.Buffer
	writeString(&buf, addr)
	tags := []byte{','}
	var data bytes.Buffer
	for _, a := range args {
		switch v := a.(type) {
		case bool:
			if v {
				tags = append(tags, 'T')
			} else {
				tags = append(tags, 'F')
			}
		case nil:
			tags = append(tags, 'N')
		case int:
			tags = append(tags, 'i')
			_ = binary.Write(&data, binary.BigEndian, int32(v))
		case int32:
			tags = append(tags, 'i')
			_ = binary.Write(&data, binary.BigEndian, v)
		case int64:
			tags = append(tags, 'h')
			_ = binary.Write(&data, binary.BigEndian, v)
		case float32:
			tags = append(tags, 'f')
			_ = binary.Write(&data, binary.BigEndian, v)
		case float64:
			tags = append(tags, 'f')
			_ = binary.Write(&data, binary.BigEndian, float32(v))
		case string:
			tags = append(tags, 's')
			writeString(&data, v)
		case []byte:
			tags = append(tags, 'b')
			_ = binary.Write(&data, binary.BigEndian, int32(len(v)))
			data.Write(v)
			for data.Len()%4 != 0 {
				data.WriteByte(0)
			}
		default:
			return nil, fmt.Errorf("osc: unsupported arg type %T", a)
		}
	}
	writeString(&buf, string(tags))
	buf.Write(data.Bytes())
	return buf.Bytes(), nil
}

// Client sends OSC messages over UDP.
type Client struct {
	conn net.Conn
}

// NewClient dials host:port (UDP, no handshake).
func NewClient(host string, port int) (*Client, error) {
	c, err := net.Dial("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	return &Client{conn: c}, nil
}

// Send encodes and sends a message.
func (c *Client) Send(addr string, args ...any) error {
	b, err := Encode(addr, args...)
	if err != nil {
		return err
	}
	_, err = c.conn.Write(b)
	return err
}

// Close releases the socket.
func (c *Client) Close() error { return c.conn.Close() }

// SendOnce is a convenience one-shot send.
func SendOnce(host string, port int, addr string, args ...any) error {
	c, err := NewClient(host, port)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Send(addr, args...)
}

// HandlerFunc handles one message.
type HandlerFunc func(addr string, args []any)

type route struct {
	pattern string
	re      *regexp.Regexp // nil = exact match
	h       HandlerFunc
}

// Dispatcher maps address patterns to handlers. A mapped address containing
// '*' matches like python-osc: '*' = "[^/]*?/*" anchored at the start only.
type Dispatcher struct {
	routes []route
	// Hook runs for every message before routing (recording, overlimit, OGB discovery).
	Hook func(addr string, args []any)
}

// Map registers h for pattern.
func (d *Dispatcher) Map(pattern string, h HandlerFunc) {
	r := route{pattern: pattern, h: h}
	if strings.Contains(pattern, "*") {
		parts := strings.Split(pattern, "*")
		for i := range parts {
			parts[i] = regexp.QuoteMeta(parts[i])
		}
		r.re = regexp.MustCompile("^" + strings.Join(parts, "[^/]*?/*"))
	}
	d.routes = append(d.routes, r)
}

// Dispatch routes one message; returns true if any handler matched.
func (d *Dispatcher) Dispatch(m Message) bool {
	if d.Hook != nil {
		d.Hook(m.Address, m.Args)
	}
	matched := false
	for _, r := range d.routes {
		if (r.re == nil && r.pattern == m.Address) || (r.re != nil && r.re.MatchString(m.Address)) {
			matched = true
			r.h(m.Address, m.Args)
		}
	}
	return matched
}

// Server is a UDP OSC listener. Its dispatcher can be swapped at runtime
// (SetDispatcher) so routing changes do not require reopening the socket.
type Server struct {
	conn *net.UDPConn
	d    atomic.Pointer[Dispatcher]
	wg   sync.WaitGroup
}

// Listen starts serving on host:port.
func Listen(host string, port int, d *Dispatcher) (*Server, error) {
	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	c, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	_ = c.SetReadBuffer(1 << 20)
	s := &Server{conn: c}
	s.d.Store(d)
	s.wg.Add(1)
	go s.loop()
	return s, nil
}

func (s *Server) loop() {
	defer s.wg.Done()
	buf := make([]byte, 65536)
	for {
		n, _, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		msgs, err := Parse(buf[:n])
		if err != nil {
			logx.Debug("[osc] bad packet: %v", err)
		}
		for _, m := range msgs {
			s.dispatchSafe(m)
		}
	}
}

func (s *Server) dispatchSafe(m Message) {
	defer func() {
		if r := recover(); r != nil {
			logx.Error("[osc] handler panic for %s: %v", m.Address, r)
		}
	}()
	s.d.Load().Dispatch(m)
}

// SetDispatcher atomically replaces the routing table.
func (s *Server) SetDispatcher(d *Dispatcher) { s.d.Store(d) }

// LocalAddr returns the bound address.
func (s *Server) LocalAddr() net.Addr { return s.conn.LocalAddr() }

// Close stops the server and waits for the loop to exit.
func (s *Server) Close() {
	_ = s.conn.Close()
	s.wg.Wait()
}
