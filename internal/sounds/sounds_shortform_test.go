package sounds

import "testing"

func TestDeriveShortIDNoCollisionsAcrossCatalog(t *testing.T) {
	seen := map[string]string{}
	for _, sa := range SpeakerAudios() {
		if prev, ok := seen[sa.ShortID]; ok {
			t.Fatalf("ShortID collision: %q used by both %s and %s", sa.ShortID, prev, sa.FullID)
		}
		seen[sa.ShortID] = sa.FullID
	}
}

func TestDeriveShortRUExtractsNumberAfterNumeroSignNotFirstDigit(t *testing.T) {
	// "Монета (8 бит) №1" / "№2" — the "8" in "8 бит" comes before "№",
	// so a naive first-digit-anywhere scan would wrongly produce
	// "монета-8" for both variants (an accidental collision, not a real
	// ambiguity). The right answer takes the number right after "№".
	if got := deriveShortRU("Монета (8 бит) №1"); got != "монета-1" {
		t.Fatalf("got %q", got)
	}
	if got := deriveShortRU("Монета (8 бит) №2"); got != "монета-2" {
		t.Fatalf("got %q", got)
	}
}

func TestDeriveShortRUNoNumberSign(t *testing.T) {
	if got := deriveShortRU("Взрыв"); got != "взрыв" {
		t.Fatalf("got %q", got)
	}
}

func TestDeriveShortIDStripsCategorySegment(t *testing.T) {
	cases := map[string]string{
		"alice-sounds-things-bell-1":         "bell-1",
		"alice-sounds-transport-ship-horn-1": "ship-horn-1",
		"alice-music-harp-1":                 "harp-1",
		"alice-sounds-game-8-bit-coin-1":     "8-bit-coin-1",
	}
	for in, want := range cases {
		if got := deriveShortID(in); got != want {
			t.Fatalf("deriveShortID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindSpeakerAudioResolvesShortEnglishID(t *testing.T) {
	id, _, ok := FindSpeakerAudio("bell-1")
	if !ok || id != "alice-sounds-things-bell-1" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
}

func TestFindSpeakerAudioResolvesShortRussianKeyword(t *testing.T) {
	id, _, ok := FindSpeakerAudio("колокол-2")
	if !ok || id != "alice-sounds-things-bell-2" {
		t.Fatalf("id=%q ok=%v", id, ok)
	}
}

func TestFindSpeakerAudioAmbiguousBareWordListsCandidates(t *testing.T) {
	_, candidates, ok := FindSpeakerAudio("bell")
	if ok {
		t.Fatal("expected ambiguous, not a single match")
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates=%v", candidates)
	}
}

func TestSpeakerAudioNameRUHasNoNumeroSign(t *testing.T) {
	for _, sa := range SpeakerAudios() {
		if strings := sa.NameRU; contains(strings, "№") {
			t.Fatalf("%s: NameRU still has № : %q", sa.FullID, sa.NameRU)
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
