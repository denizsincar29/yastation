package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/denizsincar29/yastation/internal/app"
)

// fakeStation is a minimal app.StationAPI so runOnce can be tested
// without a real Yandex account.
type fakeStation struct {
	mu    sync.Mutex
	calls []string
	fail  bool
}

func (f *fakeStation) record(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("boom")
	}
	f.calls = append(f.calls, s)
	return nil
}

func (f *fakeStation) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeStation) Say(station, text string) error {
	return f.record(fmt.Sprintf("say:%s:%s", station, text))
}
func (f *fakeStation) Command(station, text string) error {
	return f.record(fmt.Sprintf("cmd:%s:%s", station, text))
}
func (f *fakeStation) Notify(station, text string, volume float64) error { return f.record("notify") }
func (f *fakeStation) Volume(station string, level float64) error {
	return f.record(fmt.Sprintf("volume:%v", level))
}
func (f *fakeStation) Play(station string) error     { return f.record("play") }
func (f *fakeStation) Pause(station string) error    { return f.record("pause") }
func (f *fakeStation) Stop(station string) error     { return f.record("stop") }
func (f *fakeStation) Next(station string) error     { return f.record("next") }
func (f *fakeStation) Previous(station string) error { return f.record("prev") }
func (f *fakeStation) Timer(station string, minutes int, label string) error {
	return f.record("timer")
}
func (f *fakeStation) Alarm(station, at, label string) error     { return f.record("alarm") }
func (f *fakeStation) Reminder(station, text, when string) error { return f.record("reminder") }
func (f *fakeStation) Weather(station string) error              { return f.record("weather") }
func (f *fakeStation) News(station string) error                 { return f.record("news") }
func (f *fakeStation) RunScenario(name string) error             { return f.record("scenario") }
func (f *fakeStation) ListScenarios() []string                   { return nil }
func (f *fakeStation) Diagnostics() (string, error)              { return "ok", nil }

func TestRunOnceExecutesInOrder(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()

	code := runOnce(context.Background(), a, []string{"/volume 0.3", "/say привет"})
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	calls := f.Calls()
	if len(calls) != 2 || calls[0] != "volume:0.3" || calls[1] != "say::привет" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestRunOnceReturnsNonZeroOnError(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()

	code := runOnce(context.Background(), a, []string{"/nope"})
	if code != 1 {
		t.Fatalf("expected exit code 1 for unknown command, got %d", code)
	}
}

func TestRunOnceContinuesAfterOneFailure(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()

	code := runOnce(context.Background(), a, []string{"/nope", "/volume 0.5"})
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "volume:0.5" {
		t.Fatalf("expected the second command to still run: %v", calls)
	}
}
