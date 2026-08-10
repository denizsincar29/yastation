package scheduler

import (
	"sync"
	"testing"
	"time"
)

func TestParseInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"30s": 30 * time.Second,
		"5m":  5 * time.Minute,
		"2h":  2 * time.Hour,
	}
	for spec, want := range cases {
		got, err := ParseInterval(spec)
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		if got != want {
			t.Fatalf("%s: got %v want %v", spec, got, want)
		}
	}
	if _, err := ParseInterval("5x"); err == nil {
		t.Fatal("expected error for unknown unit")
	}
}

func TestScheduleEveryFiresRepeatedly(t *testing.T) {
	var mu sync.Mutex
	count := 0
	s := New(func(cmd string) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	_, err := s.Schedule("every 0.02s", "/say tick")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(110 * time.Millisecond)
	s.CancelAll()

	mu.Lock()
	got := count
	mu.Unlock()
	if got < 3 {
		t.Fatalf("expected at least 3 fires in ~110ms at 20ms interval, got %d", got)
	}

	// make sure it actually stopped
	time.Sleep(60 * time.Millisecond)
	mu.Lock()
	after := count
	mu.Unlock()
	if after != got {
		t.Fatalf("task kept firing after CancelAll: %d -> %d", got, after)
	}
}

func TestScheduleAtFiresOnceAtGivenTime(t *testing.T) {
	fired := make(chan string, 1)
	s := New(func(cmd string) { fired <- cmd })
	at := time.Now().Add(30 * time.Millisecond)
	s.ScheduleAt(at, "at test", "/say доброе утро")

	select {
	case cmd := <-fired:
		if cmd != "/say доброе утро" {
			t.Fatalf("got %q", cmd)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("task never fired")
	}

	select {
	case <-fired:
		t.Fatal("at-task fired more than once")
	case <-time.After(80 * time.Millisecond):
	}
}

func TestNextAtRollsOverToTomorrow(t *testing.T) {
	now := time.Date(2026, 8, 10, 23, 0, 0, 0, time.UTC)
	got, err := nextAt("07:30", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Day() != now.Day()+1 || got.Hour() != 7 || got.Minute() != 30 {
		t.Fatalf("expected tomorrow 07:30, got %v", got)
	}
}

func TestNextAtStaysTodayIfAhead(t *testing.T) {
	now := time.Date(2026, 8, 10, 6, 0, 0, 0, time.UTC)
	got, err := nextAt("07:30", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Day() != now.Day() || got.Hour() != 7 {
		t.Fatalf("expected today 07:30, got %v", got)
	}
}

func TestListAndCancelAll(t *testing.T) {
	s := New(func(cmd string) {})
	s.Schedule("every 1h", "/say a")
	s.Schedule("every 2h", "/say b")
	if len(s.List()) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(s.List()))
	}
	s.CancelAll()
	if len(s.List()) != 0 {
		t.Fatalf("expected 0 tasks after CancelAll, got %d", len(s.List()))
	}
}
