package logx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileLevel(t *testing.T) {
	SetStderr(nil)
	p := filepath.Join(t.TempDir(), "x.log")
	if err := OpenFile(p); err != nil {
		t.Fatal(err)
	}
	defer Close()
	SetLevel(INFO)
	Debug("dbg-hidden")
	Info("info-shown")
	SetLevel(ERROR)
	Info("info-still-in-file")
	SetLevel(DEBUG)
	Debug("dbg-shown")
	Close()
	b, _ := os.ReadFile(p)
	s := string(b)
	for _, want := range []string{"info-shown", "info-still-in-file", "dbg-shown"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(s, "dbg-hidden") {
		t.Error("DEBUG written at INFO level")
	}
	SetLevel(INFO)
}

func TestUIBufferHasNoCaller(t *testing.T) {
	SetStderr(nil)
	SetLevel(INFO)
	Info("hello-ui")
	e := Buffer(1)[0]
	if strings.Contains(e.Text, "logx_test") || !strings.HasSuffix(e.Text, "hello-ui") {
		t.Fatal(e.Text)
	}
}
