// Package logx is a small loguru-like logger: leveled, with stderr, rotating
// file, and an in-memory ring buffer that feeds the Web UI log viewer.
package logx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Level is a log severity.
type Level int

const (
	DEBUG   Level = 10
	INFO    Level = 20
	SUCCESS Level = 25
	WARNING Level = 30
	ERROR   Level = 40
)

func (l Level) String() string {
	switch l {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case SUCCESS:
		return "SUCCESS"
	case WARNING:
		return "WARNING"
	case ERROR:
		return "ERROR"
	}
	return "LEVEL"
}

// ParseLevel converts a loguru level name into a Level (default INFO).
func ParseLevel(s string) Level {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "TRACE", "DEBUG":
		return DEBUG
	case "INFO":
		return INFO
	case "SUCCESS":
		return SUCCESS
	case "WARNING", "WARN":
		return WARNING
	case "ERROR", "CRITICAL":
		return ERROR
	}
	return INFO
}

// Entry is a buffered log record as consumed by the Web UI.
type Entry struct {
	Text  string  `json:"text"`
	Level string  `json:"level"`
	Time  float64 `json:"time"`
}

const (
	bufferMax   = 500
	rotateBytes = 5 * 1024 * 1024
	retention   = 2
)

var (
	mu       sync.Mutex
	level              = INFO
	stderr   io.Writer = os.Stderr
	file     *os.File
	filePath string
	fileSize int64
	buffer   []Entry
	observer func(Entry)
)

// SetLevel sets the threshold for all sinks. The file additionally keeps
// INFO and above even when the UI level is stricter (WARNING/ERROR), so
// the log on disk stays useful; DEBUG reaches the file only when chosen.
func SetLevel(l Level) {
	mu.Lock()
	level = l
	mu.Unlock()
}

// GetLevel returns the current console/UI level.
func GetLevel() Level {
	mu.Lock()
	defer mu.Unlock()
	return level
}

// SetStderr overrides the console sink (nil disables it).
func SetStderr(w io.Writer) {
	mu.Lock()
	stderr = w
	mu.Unlock()
}

// SetObserver registers a callback invoked for each buffered entry.
func SetObserver(f func(Entry)) {
	mu.Lock()
	observer = f
	mu.Unlock()
}

// OpenFile enables the rotating file sink.
func OpenFile(path string) error {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		_ = file.Close()
		file = nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	if st != nil {
		fileSize = st.Size()
	}
	file, filePath = f, path
	return nil
}

// Close flushes and closes the file sink.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		_ = file.Sync()
		_ = file.Close()
		file = nil
	}
}

func rotateLocked() {
	if file == nil {
		return
	}
	_ = file.Close()
	ts := time.Now().Format("2006-01-02_15-04-05")
	ext := filepath.Ext(filePath)
	base := strings.TrimSuffix(filePath, ext)
	_ = os.Rename(filePath, fmt.Sprintf("%s.%s%s", base, ts, ext))
	// retention: keep newest N rotated files
	matches, _ := filepath.Glob(base + ".*" + ext)
	if len(matches) > retention {
		// names sort chronologically thanks to the timestamp format
		for _, m := range matches[:len(matches)-retention] {
			_ = os.Remove(m)
		}
	}
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		file = nil
		return
	}
	file, fileSize = f, 0
}

// Buffer returns a copy of the most recent n buffered entries.
func Buffer(n int) []Entry {
	mu.Lock()
	defer mu.Unlock()
	if n <= 0 || n > len(buffer) {
		n = len(buffer)
	}
	out := make([]Entry, n)
	copy(out, buffer[len(buffer)-n:])
	return out
}

func caller(skip int) string {
	pc, _, line, ok := runtime.Caller(skip)
	if !ok {
		return "?:?:0"
	}
	fn := runtime.FuncForPC(pc)
	name := "?"
	if fn != nil {
		name = fn.Name()
	}
	// shockingvrc/internal/handler.(*Shock).feeder -> handler:(*Shock).feeder
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	pkg, fun := name, name
	if i := strings.Index(name, "."); i >= 0 {
		pkg, fun = name[:i], name[i+1:]
	}
	return fmt.Sprintf("%s:%s:%d", pkg, fun, line)
}

func logf(l Level, format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	now := time.Now()
	where := caller(3)
	// The UI line omits the code location; it is noise for users and stays in the file.
	short := fmt.Sprintf("%s | %-8s | %s", now.Format("15:04:05.000"), l.String(), msg)
	full := fmt.Sprintf("%s | %-8s | %s - %s\n", now.Format("2006-01-02 15:04:05.000"), l.String(), where, msg)

	mu.Lock()
	fileLevel := level
	if fileLevel > INFO {
		fileLevel = INFO
	}
	if file != nil && l >= fileLevel {
		n, _ := file.WriteString(full)
		fileSize += int64(n)
		if fileSize > rotateBytes {
			rotateLocked()
		}
	}
	var obs func(Entry)
	var entry Entry
	if l >= level {
		if stderr != nil {
			_, _ = io.WriteString(stderr, full)
		}
		entry = Entry{Text: short, Level: strings.ToLower(l.String()), Time: float64(now.UnixNano()) / 1e9}
		buffer = append(buffer, entry)
		if len(buffer) > bufferMax {
			buffer = append([]Entry(nil), buffer[len(buffer)-bufferMax:]...)
		}
		obs = observer
	}
	mu.Unlock()
	// The observer must be non-blocking and must never log (no recursion).
	if obs != nil {
		obs(entry)
	}
}

func Debug(format string, args ...any)   { logf(DEBUG, format, args...) }
func Info(format string, args ...any)    { logf(INFO, format, args...) }
func Success(format string, args ...any) { logf(SUCCESS, format, args...) }
func Warn(format string, args ...any)    { logf(WARNING, format, args...) }
func Error(format string, args ...any)   { logf(ERROR, format, args...) }
