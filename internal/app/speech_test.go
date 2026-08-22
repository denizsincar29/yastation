package app

import (
	"reflect"
	"testing"
)

func TestSplitWhisperSegmentsPlainText(t *testing.T) {
	segs := splitWhisperSegments("просто текст")
	want := []speechSegment{{Text: "просто текст"}}
	if !reflect.DeepEqual(segs, want) {
		t.Fatalf("segs=%+v", segs)
	}
}

func TestSplitWhisperSegmentsSingleWhisperBlock(t *testing.T) {
	segs := splitWhisperSegments("((весь текст шёпотом))")
	want := []speechSegment{{Text: "весь текст шёпотом", Whisper: true}}
	if !reflect.DeepEqual(segs, want) {
		t.Fatalf("segs=%+v", segs)
	}
}

func TestSplitWhisperSegmentsMixed(t *testing.T) {
	segs := splitWhisperSegments("привет ((это секрет)) пока")
	want := []speechSegment{
		{Text: "привет"},
		{Text: "это секрет", Whisper: true},
		{Text: "пока"},
	}
	if !reflect.DeepEqual(segs, want) {
		t.Fatalf("segs=%+v", segs)
	}
}

func TestSplitWhisperSegmentsMultipleWhisperBlocks(t *testing.T) {
	segs := splitWhisperSegments("((тише)) обычно ((снова тише))")
	want := []speechSegment{
		{Text: "тише", Whisper: true},
		{Text: "обычно"},
		{Text: "снова тише", Whisper: true},
	}
	if !reflect.DeepEqual(segs, want) {
		t.Fatalf("segs=%+v", segs)
	}
}

func TestSplitWhisperSegmentsEmptyInput(t *testing.T) {
	if segs := splitWhisperSegments(""); len(segs) != 0 {
		t.Fatalf("segs=%v", segs)
	}
}

func TestSplitWhisperSegmentsEmptyWhisperBlockDropped(t *testing.T) {
	segs := splitWhisperSegments("текст (()) ещё текст")
	want := []speechSegment{{Text: "текст"}, {Text: "ещё текст"}}
	if !reflect.DeepEqual(segs, want) {
		t.Fatalf("segs=%+v", segs)
	}
}

func TestExpandSoundTagsNoMarkup(t *testing.T) {
	got, err := expandSoundTags("просто текст без скобок")
	if err != nil {
		t.Fatal(err)
	}
	if got != "просто текст без скобок" {
		t.Fatalf("got=%q", got)
	}
}

func TestExpandSoundTagsSingleTag(t *testing.T) {
	got, err := expandSoundTags("поздравляю [boot] с победой")
	if err != nil {
		t.Fatal(err)
	}
	want := `поздравляю <speaker audio="alice-sounds-game-boot-1.opus"> с победой`
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestExpandSoundTagsMultipleTags(t *testing.T) {
	got, err := expandSoundTags("[boot] загрузка, [alice-sounds-game-win-1] победа")
	if err != nil {
		t.Fatal(err)
	}
	want := `<speaker audio="alice-sounds-game-boot-1.opus"> загрузка, <speaker audio="alice-sounds-game-win-1.opus"> победа`
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestExpandSoundTagsUnclosedBracketIsLiteral(t *testing.T) {
	got, err := expandSoundTags("текст [не закрыто")
	if err != nil {
		t.Fatal(err)
	}
	if got != "текст [не закрыто" {
		t.Fatalf("got=%q", got)
	}
}

func TestExpandSoundTagsUnknownQueryErrors(t *testing.T) {
	if _, err := expandSoundTags("[zzzznonexistent]"); err == nil {
		t.Fatal("expected error")
	}
}

func TestExpandSoundTagsAmbiguousQueryErrors(t *testing.T) {
	if _, err := expandSoundTags("[game win]"); err == nil {
		t.Fatal("expected error for ambiguous query (win-1/2/3)")
	}
}

func TestExpandSoundTagsNumeroStyle(t *testing.T) {
	// "№" — the Russian-keyboard-friendly alternative to "[" "]"
	got, err := expandSoundTags("поздравляю №boot№ с победой")
	if err != nil {
		t.Fatal(err)
	}
	want := `поздравляю <speaker audio="alice-sounds-game-boot-1.opus"> с победой`
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestExpandSoundTagsMixedStyles(t *testing.T) {
	got, err := expandSoundTags("[boot] и №boot№ вперемешку")
	if err != nil {
		t.Fatal(err)
	}
	want := `<speaker audio="alice-sounds-game-boot-1.opus"> и <speaker audio="alice-sounds-game-boot-1.opus"> вперемешку`
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestExpandSoundTagsUnclosedNumeroIsLiteral(t *testing.T) {
	got, err := expandSoundTags("текст №не закрыто")
	if err != nil {
		t.Fatal(err)
	}
	if got != "текст №не закрыто" {
		t.Fatalf("got=%q", got)
	}
}

func TestSpeakReturnsOriginalTextInConfirmation(t *testing.T) {
	f := &fakeStation{}
	out, err := speak(f, "Кухня", "привет ((секрет))")
	if err != nil {
		t.Fatal(err)
	}
	if out != "Алиса сказала: привет ((секрет))" {
		t.Fatalf("out=%q", out)
	}
	calls := f.Calls()
	want := []string{"say:Кухня:привет", "whisper:Кухня:секрет"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestSpeakEmptyTextErrors(t *testing.T) {
	f := &fakeStation{}
	if _, err := speak(f, "", "   "); err == nil {
		t.Fatal("expected error for effectively empty text")
	}
}

func TestSpeakStopsOnFirstSegmentError(t *testing.T) {
	f := &fakeStation{failNext: true}
	if _, err := speak(f, "", "привет ((и это)) пока"); err == nil {
		t.Fatal("expected error to propagate from the first failing segment")
	}
	// Only the first (failing) call should have been attempted.
	if calls := f.Calls(); len(calls) != 0 {
		t.Fatalf("calls=%v", calls)
	}
}
