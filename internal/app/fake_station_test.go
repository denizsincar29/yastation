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
func (f *fakeStation) RunScenario(name string) error { return f.record("scenario:" + name) }
func (f *fakeStation) ListScenarios() []string       { return f.scenarios }
func (f *fakeStation) Diagnostics() (string, error)  { return "diag-ok", nil }
func (f *fakeStation) Capabilities(station string) ([]any, error) {
	f.record("caps:" + station)
	return []any{"stub-capability"}, nil
}
func (f *fakeStation) RawCapability(station, capType, instance string, value any) error {
	return f.record(fmt.Sprintf("raw:%s:%s:%s:%v", station, capType, instance, value))
}
