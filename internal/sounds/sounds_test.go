package sounds

import "testing"

func TestCatalogsAreNonEmpty(t *testing.T) {
	if len(Effects()) == 0 {
		t.Fatal("Effects() is empty — sound_play.json failed to load?")
	}
	if len(SpeakerAudios()) == 0 {
		t.Fatal("SpeakerAudios() is empty — speaker_audio.json failed to load?")
	}
}

func TestFindEffectExactID(t *testing.T) {
	id, _, ok := FindEffect("win-1")
	if !ok || id != "win-1" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
}

func TestFindEffectByEnglishSlugSingleMatch(t *testing.T) {
	// "cuckoo-clock-1" is the only entry whose slug contains "cuckoo".
	id, _, ok := FindEffect("cuckoo")
	if !ok || id != "cuckoo-clock-1" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
}

func TestFindEffectAmbiguousReturnsCandidates(t *testing.T) {
	// bell-1 and bell-2 both exist (both confirmed working against a
	// real device) — "bell" alone must not silently pick one.
	// (explosion-2 used to be the example here, but it was removed by
	// cmd/yastation-soundcheck-apply after a real device confirmed only
	// explosion-1 actually exists.)
	id, candidates, ok := FindEffect("bell")
	if ok {
		t.Fatalf("expected ambiguous match, got id=%q", id)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates=%v", candidates)
	}
}

func TestFindEffectByRussianSubstring(t *testing.T) {
	// Both "Раскат грома 1" and "Раскат грома 2" exist, so the query
	// must include the "1" to be unambiguous.
	id, _, ok := FindEffect("Раскат грома 1")
	if !ok || id != "thunder-1" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
}

func TestFindEffectUnknownReturnsNoCandidates(t *testing.T) {
	_, candidates, ok := FindEffect("zzzznonexistent")
	if ok {
		t.Fatal("expected no match")
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates=%v", candidates)
	}
}

func TestFindSpeakerAudioExactFullID(t *testing.T) {
	id, _, ok := FindSpeakerAudio("alice-sounds-game-win-1")
	if !ok || id != "alice-sounds-game-win-1" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
}

func TestFindSpeakerAudioBySlugSingleMatch(t *testing.T) {
	// "boot" only appears once in the games category.
	id, _, ok := FindSpeakerAudio("boot")
	if !ok || id != "alice-sounds-game-boot-1" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
}

func TestFindSpeakerAudioByFullSlugWithVariantNumber(t *testing.T) {
	// "win 1" (space instead of dash) should resolve to exactly win-1,
	// not be ambiguous with win-2/win-3.
	id, _, ok := FindSpeakerAudio("win 1")
	if !ok || id != "alice-sounds-game-win-1" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
}

func TestFindSpeakerAudioByRussianSubstring(t *testing.T) {
	id, _, ok := FindSpeakerAudio("бензопила")
	if !ok || id != "alice-sounds-things-chainsaw-1" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
}

func TestFindSpeakerAudioAmbiguous(t *testing.T) {
	// "game win" matches win-1/2/3 but not the unrelated "nature wind"
	// entries (their slug is "alice sounds nature wind", which doesn't
	// contain "game win").
	_, candidates, ok := FindSpeakerAudio("game win")
	if ok {
		t.Fatal("expected ambiguous match for 'game win' (win-1/2/3)")
	}
	if len(candidates) != 3 {
		t.Fatalf("candidates=%v", candidates)
	}
}

func TestSlugStripsTrailingVariantNumber(t *testing.T) {
	if got := slug("8bit-coin-1"); got != "8bit coin" {
		t.Fatalf("slug=%q", got)
	}
	if got := slug("alice-sounds-game-win-1"); got != "alice sounds game win" {
		t.Fatalf("slug=%q", got)
	}
}

func TestFormatCandidatesEmptyVsNonEmpty(t *testing.T) {
	if got := FormatCandidates[Effect]("nope", nil); got == "" {
		t.Fatal("expected non-empty message for no candidates")
	}
	msg := FormatCandidates("win", []Effect{{ID: "win-1", NameRU: "Победа 1"}})
	if msg == "" {
		t.Fatal("expected non-empty message")
	}
}
