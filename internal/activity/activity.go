// Package activity keeps a small in-memory ring buffer of recently proxied
// requests so the management UI can show a live activity feed without any
// external log pipeline.
package activity

import (
	"sync"
	"time"
)

// Entry is a single proxied request.
type Entry struct {
	Time       time.Time `json:"ts"`
	Service    string    `json:"service"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	DurationMs float64   `json:"durationMs"`
}

// Log is a fixed-capacity ring buffer of entries. The zero value is not
// usable; create one with NewLog. All methods are safe for concurrent use and
// nil-safe, so a nil *Log can be registered as an observer without checks.
type Log struct {
	mu   sync.Mutex
	buf  []Entry
	next int
	full bool
}

// NewLog returns a Log that retains the most recent capacity entries.
func NewLog(capacity int) *Log {
	if capacity <= 0 {
		capacity = 200
	}
	return &Log{buf: make([]Entry, capacity)}
}

// Record appends a request to the log, evicting the oldest entry when full.
// Its signature matches manager.RequestObserver.
func (l *Log) Record(service, method, path string, status int, d time.Duration) {
	if l == nil {
		return
	}
	e := Entry{
		Time:       time.Now(),
		Service:    service,
		Method:     method,
		Path:       path,
		Status:     status,
		DurationMs: float64(d.Microseconds()) / 1000,
	}
	l.mu.Lock()
	l.buf[l.next] = e
	l.next = (l.next + 1) % len(l.buf)
	if l.next == 0 {
		l.full = true
	}
	l.mu.Unlock()
}

// Recent returns up to n entries, newest first. n <= 0 returns everything.
func (l *Log) Recent(n int) []Entry {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	size := l.next
	if l.full {
		size = len(l.buf)
	}
	if n <= 0 || n > size {
		n = size
	}
	out := make([]Entry, 0, n)
	for i := 1; i <= n; i++ {
		idx := (l.next - i + len(l.buf)) % len(l.buf)
		out = append(out, l.buf[idx])
	}
	return out
}

// Len returns the number of retained entries.
func (l *Log) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.full {
		return len(l.buf)
	}
	return l.next
}
