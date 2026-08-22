package activity

import (
	"testing"
	"time"
)

func TestLogRecentNewestFirstAndEviction(t *testing.T) {
	l := NewLog(3)
	if got := l.Recent(10); len(got) != 0 {
		t.Fatalf("empty log returned %d entries", len(got))
	}

	for i, p := range []string{"/a", "/b", "/c", "/d"} {
		l.Record("svc", "GET", p, 200+i, time.Millisecond)
	}

	if l.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (capacity)", l.Len())
	}
	got := l.Recent(0)
	want := []string{"/d", "/c", "/b"} // newest first, "/a" evicted
	if len(got) != len(want) {
		t.Fatalf("Recent returned %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Path != want[i] {
			t.Errorf("Recent[%d].Path = %q, want %q", i, got[i].Path, want[i])
		}
	}
	if got[0].Status != 203 || got[0].DurationMs != 1 {
		t.Errorf("newest entry = %+v, want status 203 / 1ms", got[0])
	}

	if got := l.Recent(2); len(got) != 2 || got[0].Path != "/d" || got[1].Path != "/c" {
		t.Errorf("Recent(2) = %v, want [/d /c]", got)
	}
}

func TestNilLogIsSafe(t *testing.T) {
	var l *Log
	l.Record("svc", "GET", "/", 200, 0) // must not panic
	if l.Recent(5) != nil || l.Len() != 0 {
		t.Fatal("nil log should report no entries")
	}
}
