package app

import (
	"errors"
	"fmt"
	"sync"
)

// fakeStation records every call it receives instead of talking to
// Yandex, so tests can assert on exactly what the dispatcher decided to
// send.
type fakeStation struct {
	mu        sync.Mutex
	calls     []string
	scenarios []string
	failNext  bool
}

var errFake = errors.New("simulated failure")

func (f *fakeStation) record(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return errFake
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
func (f *fakeStation) Notify(station, text string, volume float64) error {
	return f.record(fmt.Sprintf("notify:%s:%s:%v", station, text, volume))
}
func (f *fakeStation) Volume(station string, level float64) error {
	return f.record(fmt.Sprintf("volume:%s:%v", station, level))
}
func (f *fakeStation) Play(station string) error     { return f.record("play:" + station) }
func (f *fakeStation) Pause(station string) error    { return f.record("pause:" + station) }
func (f *fakeStation) Stop(station string) error     { return f.record("stop:" + station) }
func (f *fakeStation) Next(station string) error     { return f.record("next:" + station) }
func (f *fakeStation) Previous(station string) error { return f.record("prev:" + station) }
func (f *fakeStation) Timer(station string, minutes int, label string) error {
	return f.record(fmt.Sprintf("timer:%s:%d:%s", station, minutes, label))
}
func (f *fakeStation) Alarm(station, at, label string) error {
	return f.record(fmt.Sprintf("alarm:%s:%s:%s", station, at, label))
}
func (f *fakeStation) Reminder(station, text, when string) error {
	return f.record(fmt.Sprintf("reminder:%s:%s:%s", station, when, text))
}
func (f *fakeStation) Weather(station string) error  { return f.record("weather:" + station) }
func (f *fakeStation) News(station string) error     { return f.record("news:" + station) }
func (f *fakeStation) RunScenario(name string) error { return f.record("scenario:" + name) }
func (f *fakeStation) ListScenarios() []string       { return f.scenarios }
func (f *fakeStation) Diagnostics() (string, error)  { return "diag-ok", nil }
