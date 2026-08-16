package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func newTestApp() (*App, *fakeStation) {
	f := &fakeStation{scenarios: []string{"Вечер"}}
	a := New(f)
	cfg, err := DefaultCommandsConfig()
	if err != nil {
		panic(err)
	}
	if err := a.RegisterCustomCommands(cfg); err != nil {
		panic(err)
	}
	return a, f
}

func TestDefaultTextIsSaid(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	out, err := a.Execute(context.Background(), "привет с компа")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Алиса сказала: привет с компа" {
		t.Fatalf("out=%q", out)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "say::привет с компа" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestAskAliasSendsCommand(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	_, err := a.Execute(context.Background(), "/ask чё делаешь")
	if err != nil {
		t.Fatal(err)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "cmd::чё делаешь" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestDashPrefixIsCommandAlias(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	out, err := a.Execute(context.Background(), "- какая погода")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Алиса услышала команду: какая погода" {
		t.Fatalf("out=%q", out)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "cmd::какая погода" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestTildePrefixIsWhisperAlias(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	out, err := a.Execute(context.Background(), "~тише едешь")
	if err != nil {
		t.Fatal(err)
	}
	if out != "[шёпотом] тише едешь" {
		t.Fatalf("out=%q", out)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "whisper::тише едешь" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestTildeWithoutTextErrors(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	if _, err := a.Execute(context.Background(), "~"); err == nil {
		t.Fatal("expected error for bare ~")
	}
}

func TestSayWithWhisperSegmentSendsTwoSeparateCalls(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, "/say привет ((это по секрету)) пока")
	calls := f.Calls()
	want := []string{"say::привет", "whisper::это по секрету", "say::пока"}
	if len(calls) != len(want) {
		t.Fatalf("calls=%v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d: got %q want %q", i, calls[i], want[i])
		}
	}
}

func TestSayWithoutWhisperMarkupIsUnaffected(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, "/say обычная фраза без разметки")
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != "say::обычная фраза без разметки" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestSayWithSoundTagExpandsToSpeakerTag(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, "/say поздравляю с победой [boot]")
	calls := f.Calls()
	want := `say::поздравляю с победой <speaker audio="alice-sounds-game-boot-1.opus">`
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("calls=%v want=%q", calls, want)
	}
}

func TestSayWithUnknownSoundTagErrors(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	if _, err := a.Execute(context.Background(), "/say привет [zzzznonexistent]"); err == nil {
		t.Fatal("expected error for an unmatched sound query")
	}
}

func TestSayWithAmbiguousSoundTagErrors(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	// "win" matches win-1/2/3 in the games category.
	if _, err := a.Execute(context.Background(), "/say [game win]"); err == nil {
		t.Fatal("expected error for an ambiguous sound query")
	}
}

func TestSplitWhisperSegmentsUnterminatedRunsToEnd(t *testing.T) {
	segs := splitWhisperSegments("привет ((шёпот без закрытия")
	if len(segs) != 2 {
		t.Fatalf("segs=%v", segs)
	}
	if segs[0].Whisper || segs[0].Text != "привет" {
		t.Fatalf("segs[0]=%+v", segs[0])
	}
	if !segs[1].Whisper || segs[1].Text != "шёпот без закрытия" {
		t.Fatalf("segs[1]=%+v", segs[1])
	}
}

func TestVolumeParsesFloat(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	if _, err := a.Execute(context.Background(), "/volume 3"); err != nil {
		t.Fatal(err)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "volume::3" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestVolumeRejectsGarbage(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	if _, err := a.Execute(context.Background(), "/volume loud"); err == nil {
		t.Fatal("expected error for non-numeric volume")
	}
}

func TestStationOverrideArg(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	if _, err := a.Execute(context.Background(), "/say station=Кухня привет"); err != nil {
		t.Fatal(err)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "say:Кухня:привет" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestTimerAndAlarmAndReminder(t *testing.T) {
	// timer/alarm/reminder are now template-based commands loaded from
	// config.json.default (DefaultCommandsConfig), so they go through
	// Command() with a rendered Russian phrase rather than a dedicated
	// StationAPI method.
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, "/timer 10 проверить духовку")
	mustExec(t, a, "/alarm 7:30")
	mustExec(t, a, "/reminder завтра купить хлеб")

	calls := f.Calls()
	want := []string{
		"cmd::поставь таймер на 10 минут проверить духовку",
		"cmd::поставь будильник на 7:30",
		"cmd::напомни завтра: купить хлеб",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls=%v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d: got %q want %q", i, calls[i], want[i])
		}
	}
}

func TestScenariosListAndRun(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	out := mustExec(t, a, "/scenarios")
	if !strings.Contains(out, "Вечер") {
		t.Fatalf("out=%q", out)
	}
	mustExec(t, a, "/scenario Вечер")
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "scenario:Вечер" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestUnknownCommandErrors(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	if _, err := a.Execute(context.Background(), "/nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestEveryAndUnscheduleAll(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()

	mustExec(t, a, "/every 0.02s /say tick")
	time.Sleep(110 * time.Millisecond)
	mustExec(t, a, "/unschedule_all")

	calls := f.Calls()
	if len(calls) < 3 {
		t.Fatalf("expected several ticks, got %v", calls)
	}
	time.Sleep(60 * time.Millisecond)
	if len(f.Calls()) != len(calls) {
		t.Fatal("task kept firing after /unschedule_all")
	}
}

func TestScheduleStatusListing(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	mustExec(t, a, "/every 1h /say a")
	out := mustExec(t, a, "/schedules")
	if !strings.Contains(out, "every 1h") || !strings.Contains(out, "/say a") {
		t.Fatalf("out=%q", out)
	}
}

func TestExecuteScript(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()

	dir := t.TempDir()
	path := dir + "/script.txt"
	script := "# comment\n\n/volume 0.5\n/say доброе утро\n/weather\n"
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	mustExec(t, a, "/execute "+path)
	calls := f.Calls()
	want := []string{"volume::0.5", "say::доброе утро", "weather:"}
	if len(calls) != len(want) {
		t.Fatalf("calls=%v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d: got %q want %q", i, calls[i], want[i])
		}
	}
}

func TestExecuteScriptMissingFile(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	if _, err := a.Execute(context.Background(), "/execute /no/such/file.txt"); err == nil {
		t.Fatal("expected error for missing script file")
	}
}

func TestWhisperUsesStructuredWhisperFlag(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, "/whisper тише едешь")
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != "whisper::тише едешь" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestSoundFuzzyMatchByName(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, "/sound кукушка в часах")
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != "sound::cuckoo-clock-1" {
		t.Fatalf("calls=%v", calls)
	}
	if _, err := a.Execute(context.Background(), "/sound"); err == nil {
		t.Fatal("expected error without a query")
	}
}

func TestSoundAmbiguousQueryErrors(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	// bell-1 and bell-2 both match "bell" — must not silently pick one.
	// (kettle-whistle-1/explosion-2 used to be the examples here, but
	// cmd/yastation-soundcheck-apply removed kettle-whistle-1 entirely
	// and made "explosion" unambiguous after a real device confirmed
	// only kettle-whistle-1's sibling ids don't exist / explosion-2
	// doesn't exist.)
	if _, err := a.Execute(context.Background(), "/sound bell"); err == nil {
		t.Fatal("expected error for an ambiguous query")
	}
}

func TestSoundsListsCatalog(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	out := mustExec(t, a, "/sounds")
	if !strings.Contains(out, "bell-1") || !strings.Contains(out, "Всего:") {
		t.Fatalf("unfiltered /sounds output missing expected content: %q", out)
	}
	// alias
	out2 := mustExec(t, a, "/soundlist")
	if out2 != out {
		t.Fatalf("/soundlist should match /sounds: %q vs %q", out2, out)
	}
}

func TestSoundsFilterMatchesIDNameOrCategory(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	// substring of an id
	out := mustExec(t, a, "/sounds cough")
	if !strings.Contains(out, "cough-1") || strings.Contains(out, "bell-1") {
		t.Fatalf("id filter leaked or missed entries: %q", out)
	}
	// substring of a Russian category name
	out = mustExec(t, a, "/sounds Люди")
	if !strings.Contains(out, "cough-1") || !strings.Contains(out, "laugh-1") || strings.Contains(out, "bell-1") {
		t.Fatalf("category filter wrong: %q", out)
	}
	// no match
	out = mustExec(t, a, "/sounds zzzznonexistent")
	if strings.Contains(out, "Всего:") {
		t.Fatalf("expected no-match message, got: %q", out)
	}
}

func TestStopAll(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, "/stopall")
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != "stopall:" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestSceneListsOptionsWhenNoArgs(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	f.capabilities = testSceneCapabilities()

	out, err := a.Execute(context.Background(), "/scene")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "night") || !strings.Contains(out, "Ночь") {
		t.Fatalf("out=%q", out)
	}
}

func TestSceneMatchesByRussianName(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	f.capabilities = testSceneCapabilities()

	mustExec(t, a, "/scene ночь")
	calls := f.Calls()
	// last call should be the scene set (caps: was recorded first)
	last := calls[len(calls)-1]
	if last != "scene::night" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestSceneNoSuchCapabilityErrors(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	// default fakeStation.Capabilities has no color_setting entry
	if _, err := a.Execute(context.Background(), "/scene night"); err == nil {
		t.Fatal("expected error when the device has no color_setting capability")
	}
}

func testSceneCapabilities() []any {
	return []any{
		map[string]any{
			"type": "devices.capabilities.color_setting",
			"parameters": map[string]any{
				"instance": "color",
				"scenes": []any{
					map[string]any{"id": "lava_lamp", "name": "Лава лампа"},
					map[string]any{"id": "inactive", "name": "Неактивный"},
					map[string]any{"id": "night", "name": "Ночь"},
					map[string]any{"id": "candle", "name": "Свеча"},
				},
			},
		},
	}
}

func TestRefresh(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, "/refresh")
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != "refresh" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestWeatherViaCapability(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, "/weather")
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != "weather:" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestMusicViaCapability(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, "/music")
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != "music:" {
		t.Fatalf("calls=%v", calls)
	}
}

func TestCapabilitiesReturnsJSON(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	out, err := a.Execute(context.Background(), "/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stub-capability") {
		t.Fatalf("out=%q", out)
	}
	if calls := f.Calls(); len(calls) != 1 || calls[0] != "caps:" {
		t.Fatalf("calls=%v", f.Calls())
	}
}

func TestRawSendsCapabilityWithJSONValue(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, `/raw devices.capabilities.quasar tts {"text":"привет"}`)
	calls := f.Calls()
	if len(calls) != 1 || !strings.Contains(calls[0], "devices.capabilities.quasar:tts") || !strings.Contains(calls[0], "привет") {
		t.Fatalf("calls=%v", calls)
	}
}

func TestRawSendsCapabilityWithPlainStringValueWhenNotJSON(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	mustExec(t, a, "/raw devices.capabilities.quasar.server_action text_action включи новости")
	calls := f.Calls()
	if len(calls) != 1 || !strings.Contains(calls[0], "включи новости") {
		t.Fatalf("calls=%v", calls)
	}
}

func TestRawRequiresThreeArgs(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	if _, err := a.Execute(context.Background(), "/raw onlytype onlyinstance"); err == nil {
		t.Fatal("expected error for missing value")
	}
}

func TestExecuteArgsBypassesLineTokenizing(t *testing.T) {
	a, f := newTestApp()
	defer a.Close()
	out, err := a.ExecuteArgs(context.Background(), "say", []string{`hello "world"`})
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0] != `say::hello "world"` {
		t.Fatalf("calls=%v", calls)
	}
}

func TestEnqueueDoesNotBlockCaller(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			a.Enqueue("/say tick")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Enqueue blocked")
	}
}

func mustExec(t *testing.T, a *App, line string) string {
	t.Helper()
	out, err := a.Execute(context.Background(), line)
	if err != nil {
		t.Fatalf("%s -> unexpected error: %v", line, err)
	}
	return out
}

func TestSndcatListsCategoriesOnly(t *testing.T) {
	a, _ := newTestApp()
	defer a.Close()
	out := mustExec(t, a, "/sndcat")
	if strings.Contains(out, "cough-1") {
		t.Fatalf("/sndcat should not list individual sound ids: %q", out)
	}
	if !strings.Contains(out, "Люди") || !strings.Contains(out, "Всего категорий:") {
		t.Fatalf("missing expected category info: %q", out)
	}
	out2 := mustExec(t, a, "/soundcategories")
	if out2 != out {
		t.Fatalf("/soundcategories should match /sndcat")
	}
}
