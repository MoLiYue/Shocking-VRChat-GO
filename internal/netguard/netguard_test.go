package netguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuard(t *testing.T) {
	g := New("t", "mybox.lan")
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := g.Wrap(ok)
	cases := []struct {
		name   string
		method string
		path   string
		host   string
		hdr    map[string]string
		want   int
	}{
		{"script without headers", "GET", "/api/v1/shock/all/1", "127.0.0.1:8800", nil, 200},
		{"lan ip", "GET", "/api/v1/status", "192.168.1.5:8800", nil, 200},
		{"localhost", "GET", "/api/v1/status", "localhost:8800", nil, 200},
		{"ipv6", "GET", "/api/v1/status", "[::1]:8800", nil, 200},
		{"configured host", "GET", "/api/v1/status", "MyBox.lan:8800", nil, 200},
		{"rebinding host", "GET", "/api/v1/status", "evil.example:8800", nil, 403},
		{"same-origin fetch", "POST", "/api/v1/settings", "127.0.0.1:8800",
			map[string]string{"Origin": "http://127.0.0.1:8800", "Sec-Fetch-Site": "same-origin"}, 200},
		{"img tag from other site", "GET", "/api/v1/shock/all/10", "127.0.0.1:8800",
			map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors"}, 403},
		{"cross-site POST", "POST", "/api/v1/strength_limit", "127.0.0.1:8800",
			map[string]string{"Origin": "https://evil.example"}, 403},
		{"null origin", "POST", "/api/v1/strength_limit", "127.0.0.1:8800",
			map[string]string{"Origin": "null"}, 403},
		{"link to UI from elsewhere", "GET", "/dashboard", "127.0.0.1:8800",
			map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate"}, 200},
		{"navigation to API from elsewhere", "GET", "/api/v1/shock/all/10", "127.0.0.1:8800",
			map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate"}, 403},
		{"cross-site websocket", "GET", "/ws/live", "127.0.0.1:8800",
			map[string]string{"Origin": "https://evil.example"}, 403},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Host = c.host
		for k, v := range c.hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: got %d want %d", c.name, w.Code, c.want)
		}
	}
}
