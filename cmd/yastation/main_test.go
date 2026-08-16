package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/denizsincar29/yastation/internal/app"
	"github.com/denizsincar29/yastation/internal/quasar"
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
func (f *fakeStation) RunScenario(name string) error              { return f.record("scenario") }
func (f *fakeStation) ListScenarios() []string                    { return nil }
func (f *fakeStation) Diagnostics() (string, error)               { return "ok", nil }
func (f *fakeStation) Capabilities(station string) ([]any, error) { return nil, nil }
func (f *fakeStation) RawCapability(station, capType, instance string, value any) error {
	return nil
}
func (f *fakeStation) SayWhisper(station, text string) error    { return nil }
func (f *fakeStation) PlaySound(station, soundID, soundName string) error { return nil }
func (f *fakeStation) StopEverything(station string) error      { return nil }
func (f *fakeStation) LightScene(station, sceneID string) error { return nil }
func (f *fakeStation) Weather(station string) error             { return nil }
func (f *fakeStation) PlayMusic(station string) error           { return nil }
func (f *fakeStation) Refresh() error                           { return nil }

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

func TestCompleterCommands(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()
	client := &quasar.Client{Speakers: []quasar.Device{{Name: "Кухня"}, {Name: "Комната"}}}
	c := newCompleter(a, client)

	line := []rune("/sou")
	cands, length := c.Do(line, len(line))
	if length != 4 {
		t.Fatalf("length=%d, want 4", length)
	}
	got := completionStrings(line, cands, length)
	want := map[string]bool{"/sound": true, "/sounds": true, "/soundlist": true, "/sndcat": true, "/soundcategories": true}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected candidate %q in %v", g, got)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected at least one command candidate for /sou")
	}
}

func TestCompleterSoundIDs(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()
	client := &quasar.Client{}
	c := newCompleter(a, client)

	line := []rune("/sound coug")
	cands, length := c.Do(line, len(line))
	if length != 4 { // "coug"
		t.Fatalf("length=%d, want 4", length)
	}
	got := completionStrings(line, cands, length)
	foundCough1 := false
	for _, g := range got {
		if g == "cough-1" {
			foundCough1 = true
		}
		if !strings.HasPrefix(g, "coug") {
			t.Fatalf("candidate %q doesn't start with coug", g)
		}
	}
	if !foundCough1 {
		t.Fatalf("expected cough-1 among candidates: %v", got)
	}

	// non-sound command must not offer sound ids
	line2 := []rune("/say coug")
	cands2, _ := c.Do(line2, len(line2))
	if len(cands2) != 0 {
		t.Fatalf("/say shouldn't complete sound ids, got %v", cands2)
	}
}

func TestCompleterStationNames(t *testing.T) {
	f := &fakeStation{}
	a := app.New(f)
	defer a.Close()
	client := &quasar.Client{Speakers: []quasar.Device{{Name: "Кухня"}, {Name: "Комната"}}}
	c := newCompleter(a, client)

	line := []rune("/volume station=Ку")
	cands, length := c.Do(line, len(line))
	got := completionStrings(line, cands, length)
	if len(got) != 1 || got[0] != "Кухня" {
		t.Fatalf("got=%v, want [Кухня]", got)
	}
}

// completionStrings reconstructs the full completed word for each
// candidate, the way a real readline session would show it, for easier
// assertions than juggling rune suffixes directly.
func completionStrings(line []rune, cands [][]rune, length int) []string {
	word := string(line[len(line)-length:])
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = word + string(c)
	}
	return out
}
