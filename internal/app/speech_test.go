package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/denizsincar29/yastation/internal/quasar"
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

func TestBatchActionsShortStaysWhole(t *testing.T) {
	got, err := batchActions("привет как дела")
	if err != nil {
		t.Fatal(err)
	}
	want := []quasar.BatchAction{{Kind: "say", Text: "привет как дела"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v", got)
	}
}

func TestBatchActionsPipeForcesSteps(t *testing.T) {
	got, err := batchActions("один | два | три")
	if err != nil {
		t.Fatal(err)
	}
	want := []quasar.BatchAction{
		{Kind: "say", Text: "один"},
		{Kind: "say", Text: "два"},
		{Kind: "say", Text: "три"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v", got)
	}
}

func TestBatchActionsDropsEmptyPipeParts(t *testing.T) {
	got, err := batchActions("один | | два")
	if err != nil {
		t.Fatal(err)
	}
	want := []quasar.BatchAction{{Kind: "say", Text: "один"}, {Kind: "say", Text: "два"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v", got)
	}
}

func TestBatchActionsWhisperSegmentBecomesWhisperStep(t *testing.T) {
	got, err := batchActions("Стою у двери. ((Тихо тут…)) Открываю.")
	if err != nil {
		t.Fatal(err)
	}
	want := []quasar.BatchAction{
		{Kind: "say", Text: "Стою у двери."},
		{Kind: "say", Text: "Тихо тут…", Whisper: true},
		{Kind: "say", Text: "Открываю."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v", got)
	}
}

func TestBatchActionsSoundTagInlinesWhenRoom(t *testing.T) {
	// Short text around the sound leaves room for the whole <speaker audio>
	// tag inside a single say step — no separate sound_play needed.
	tag := `<speaker audio="alice-sounds-things-explosion-1.opus">`
	text := strings.Repeat("а", 10) + "[взрыв]" + strings.Repeat("б", 10)
	got, err := batchActions(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []quasar.BatchAction{{Kind: "say", Text: strings.Repeat("а", 10) + tag + strings.Repeat("б", 10)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestBatchActionsSoundTagBecomesSoundPlayWhenNoRoom(t *testing.T) {
	// 70 a's push the tag past the 96-rune cap, so the smart chunker plays
	// the explosion as a standalone sound_play step instead of splitting the
	// tag — "взрыв" resolves in the effects catalog (explosion-1).
	text := strings.Repeat("а", 70) + "[взрыв]" + strings.Repeat("б", 30)
	got, err := batchActions(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []quasar.BatchAction{
		{Kind: "say", Text: strings.Repeat("а", 70)},
		{Kind: "sound", SoundID: "explosion-1", SoundName: "Взрыв 1"},
		{Kind: "say", Text: strings.Repeat("б", 30)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	for _, g := range got {
		if g.Kind == "say" && len([]rune(g.Text)) > quasar.MaxTTSChunkChars {
			t.Fatalf("chunk exceeds cap: %d runes %q", len([]rune(g.Text)), g.Text)
		}
	}
}

func TestBatchActionsSoundWithoutEffectPushesInlineToNextChunk(t *testing.T) {
	// "бензопила" exists in the <speaker audio> catalog but has no sound_play
	// effect, so when the current chunk is full the tag is carried whole into
	// the next chunk — the sound is never lost.
	tag := `<speaker audio="alice-sounds-things-chainsaw-1.opus">`
	text := strings.Repeat("а", 70) + "[бензопила]" + strings.Repeat("б", 30)
	got, err := batchActions(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []quasar.BatchAction{
		{Kind: "say", Text: strings.Repeat("а", 70)},
		{Kind: "say", Text: tag + strings.Repeat("б", 30)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	for _, g := range got {
		if len([]rune(g.Text)) > quasar.MaxTTSChunkChars {
			t.Fatalf("chunk exceeds cap: %d runes %q", len([]rune(g.Text)), g.Text)
		}
		if strings.Contains(g.Text, "<speaker") && !strings.Contains(g.Text, ">") {
			t.Fatalf("tag cut in half: %q", g.Text)
		}
	}
}

func TestSplitHeadingsPlainProse(t *testing.T) {
	got := splitHeadings("просто\nтекст без заголовков")
	want := []string{"просто\nтекст без заголовков"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q", got)
	}
}

func TestSplitHeadingsSplitsOnHeadingsAndStripsMarks(t *testing.T) {
	got := splitHeadings("# Введение\nПервая часть.\n## Раздел\nВторая часть.")
	want := []string{"Введение. Первая часть.", "Раздел. Вторая часть."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q", got)
	}
}

func TestSplitHeadingsPlainProseKeepsNewlines(t *testing.T) {
	// A section without a heading is joined on "\n" as before — no ". "
	// injection into plain prose.
	got := splitHeadings("Первый абзац.\n# Заголовок\nТекст.")
	want := []string{"Первый абзац.", "Заголовок. Текст."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q", got)
	}
}

func TestSplitHeadingsHeadingAlone(t *testing.T) {
	// A heading with no body stays just the title, no dangling ". ".
	got := splitHeadings("# Заголовок\n## Пусто")
	want := []string{"Заголовок", "Пусто"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q", got)
	}
}

func TestSplitHeadingsHashWithoutSpaceIsProse(t *testing.T) {
	got := splitHeadings("это # не заголовок")
	want := []string{"это # не заголовок"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q", got)
	}
}

func TestBatchActionsHeadingStartsNewChunk(t *testing.T) {
	// A heading is a hard boundary: the section after it starts its own say
	// step even though the previous section's tail would have left room.
	got, err := batchActions("# Настройка\nКороткая секция.\n## Совет\nЕщё одна.")
	if err != nil {
		t.Fatal(err)
	}
	want := []quasar.BatchAction{
		{Kind: "say", Text: "Настройка. Короткая секция."},
		{Kind: "say", Text: "Совет. Ещё одна."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v", got)
	}
}

func TestBatchActionsLongSplitsToCap(t *testing.T) {
	got, err := batchActions(strings.Repeat("а", 300))
	if err != nil {
		t.Fatal(err)
	}
	var joined string
	for _, g := range got {
		joined += g.Text
		if len([]rune(g.Text)) > quasar.MaxTTSChunkChars {
			t.Fatalf("chunk exceeds cap: %d runes %q", len([]rune(g.Text)), g.Text)
		}
	}
	if joined != strings.Repeat("а", 300) {
		t.Fatalf("text mangled across chunks: %d runes", len([]rune(joined)))
	}
}

func TestSplitChunksCutsAtLastPeriod(t *testing.T) {
	// "Hello world." ends well before the cap; the x-tail has no boundary,
	// so the cut lands right after the period.
	text := "Hello world. " + strings.Repeat("x", quasar.MaxTTSChunkChars)
	got := splitChunks(text, quasar.MaxTTSChunkChars)
	want := []string{"Hello world.", strings.Repeat("x", quasar.MaxTTSChunkChars)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q", got)
	}
}

func TestSplitChunksPeriodBeforeNewlineIsBoundary(t *testing.T) {
	// The article extractor joins paragraphs with "\n", so a period at
	// the end of a paragraph is followed by a newline, not a space — it
	// must still count as a sentence boundary, or a long paragraph gets
	// cut mid-sentence at a comma instead.
	head := strings.Repeat("a", 80) + " конец.\n"
	tail := strings.Repeat("b", quasar.MaxTTSChunkChars)
	got := splitChunks(head+tail, quasar.MaxTTSChunkChars)
	if len(got) < 2 {
		t.Fatalf("got=%q", got)
	}
	if !strings.Contains(got[0], "конец.") {
		t.Fatalf("first chunk should end at the period: %q", got[0])
	}
	if !strings.HasPrefix(got[1], "b") {
		t.Fatalf("second chunk should start with the tail: %q", got[1])
	}
}

func TestSplitChunksQuoteAfterPeriodKeepsBoundary(t *testing.T) {
	// «предложение.» — the dot sits before a closing quote, then a space;
	// the boundary must survive the quote so the whole quoted sentence
	// stays in one chunk.
	head := strings.Repeat("a", 40) + " сказал: «Привет.»\n"
	tail := strings.Repeat("b", quasar.MaxTTSChunkChars)
	got := splitChunks(head+tail, quasar.MaxTTSChunkChars)
	if len(got) < 2 {
		t.Fatalf("got=%q", got)
	}
	if !strings.Contains(got[0], "«Привет.»") {
		t.Fatalf("first chunk should include the quoted sentence: %q", got[0])
	}
}

func TestSplitChunksPeriodWithoutFollowingSpaceIsNotABoundary(t *testing.T) {
	// The "." inside "3.5" isn't followed by a space, so it must not be
	// taken for a sentence end and split the number apart.
	text := strings.Repeat("a", quasar.MaxTTSChunkChars-10) + " 3.5 " + strings.Repeat("b", quasar.MaxTTSChunkChars)
	got := splitChunks(text, quasar.MaxTTSChunkChars)
	if len(got) != 2 {
		t.Fatalf("got=%q", got)
	}
	if !strings.Contains(got[0], "3.5") {
		t.Fatalf("first chunk broke the decimal apart: %q", got[0])
	}
}

func TestSplitChunksCutsAtLastCommaWhenNoPeriod(t *testing.T) {
	text := strings.Repeat("a", quasar.MaxTTSChunkChars-10) + ", " + strings.Repeat("b", quasar.MaxTTSChunkChars)
	got := splitChunks(text, quasar.MaxTTSChunkChars)
	// The cut comma is rewritten to "?" so the chunk doesn't trail off.
	want := []string{strings.Repeat("a", quasar.MaxTTSChunkChars-10) + "?", strings.Repeat("b", quasar.MaxTTSChunkChars)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q", got)
	}
}

func TestSplitChunksCommaBeforeNoPreferred(t *testing.T) {
	// An ordinary comma sits closer to the end of the window than the comma
	// before "но", but the clause boundary must win — a complex sentence
	// «А, но Б» splits at its turning point.
	text := strings.Repeat("а", 40) + ", но что-то, ещё" + strings.Repeat("б", 200)
	got := splitChunks(text, quasar.MaxTTSChunkChars)
	if len(got) < 2 {
		t.Fatalf("got=%q", got)
	}
	if !strings.HasPrefix(got[1], "но") {
		t.Fatalf("should cut at the comma before \"но\", not the later ordinary comma: %q", got)
	}
	if got[0] != strings.Repeat("а", 40)+"?" {
		t.Fatalf("cut chunk should end with \"?\": %q", got[0])
	}
}

func TestFixTrailingCommaReplacesWithQuestion(t *testing.T) {
	if got := fixTrailingComma("привет,"); got != "привет"+chunkEndCommaMark {
		t.Fatalf("got=%q", got)
	}
	if got := fixTrailingComma("привет"); got != "привет" {
		t.Fatalf("got=%q", got)
	}
	if got := fixTrailingComma(""); got != "" {
		t.Fatalf("got=%q", got)
	}
}

func TestSplitChunksCutsAtLastSpaceWhenNoPunctuation(t *testing.T) {
	text := strings.Repeat("a", quasar.MaxTTSChunkChars-10) + " " + strings.Repeat("b", quasar.MaxTTSChunkChars)
	got := splitChunks(text, quasar.MaxTTSChunkChars)
	want := []string{strings.Repeat("a", quasar.MaxTTSChunkChars-10), strings.Repeat("b", quasar.MaxTTSChunkChars)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q", got)
	}
}

func TestSplitChunksHardSplitsSingleLongWord(t *testing.T) {
	text := strings.Repeat("a", 300)
	got := splitChunks(text, quasar.MaxTTSChunkChars)
	var want []string
	for n := 300; n > 0; n -= quasar.MaxTTSChunkChars {
		chunk := n
		if chunk > quasar.MaxTTSChunkChars {
			chunk = quasar.MaxTTSChunkChars
		}
		want = append(want, strings.Repeat("a", chunk))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%q want=%q", got, want)
	}
}
