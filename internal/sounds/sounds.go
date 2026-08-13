// Package sounds indexes two unrelated sound catalogs Alice exposes, so
// commands can look one up by a partial name instead of requiring an
// exact id:
//
//   - Effects (sound_play.json): short ids for the sound_play smart-home
//     capability (see quasar.Client.PlaySound) — e.g. "chainsaw-1". This
//     is what actually plays a standalone sound effect via a command.
//   - SpeakerAudio (speaker_audio.json): full "alice-sounds-..."/
//     "alice-music-..." ids for the <speaker audio="....opus"> tag
//     embedded *inside* TTS text (see
//     https://yandex.ru/dev/dialogs/alice/doc/ru/sounds) — a completely
//     different mechanism (skill-dialog TTS markup, not a smart-home
//     capability), confirmed by hand on real hardware.
//
// Both catalogs are transcribed data (the sound_play one from a
// user-provided list matching a real device's capability state, the
// speaker_audio one scraped from Yandex's own docs) — not derived or
// guessed, but also not exhaustive: sound_play in particular has no
// documented way to list its full id space, so entries missing here
// simply aren't looked up yet.
package sounds

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

//go:embed sound_play.json
var soundPlayJSON []byte

//go:embed speaker_audio.json
var speakerAudioJSON []byte

// Effect is one entry in the sound_play library.
type Effect struct {
	ID       string `json:"id"`
	NameRU   string `json:"name_ru"`
	Category string `json:"category"`
}

// SpeakerAudio is one entry in the <speaker audio="..."> embeddable
// library. FullID has no "alice-sounds-"/"alice-music-" ambiguity baked
// in twice — it's the complete id as it appears in the tag, e.g.
// "alice-sounds-game-win-1" (no ".opus", no surrounding markup).
type SpeakerAudio struct {
	FullID   string `json:"full_id"`
	NameRU   string `json:"name_ru"`
	Category string `json:"category"`
}

var (
	effects       []Effect
	speakerAudios []SpeakerAudio
)

func init() {
	if err := json.Unmarshal(soundPlayJSON, &effects); err != nil {
		panic("internal/sounds: sound_play.json повреждён: " + err.Error())
	}
	if err := json.Unmarshal(speakerAudioJSON, &speakerAudios); err != nil {
		panic("internal/sounds: speaker_audio.json повреждён: " + err.Error())
	}
}

// Effects returns every known sound_play entry.
func Effects() []Effect { return effects }

// SpeakerAudios returns every known <speaker audio> entry.
func SpeakerAudios() []SpeakerAudio { return speakerAudios }

// slug turns an id like "8bit-coin-1" or a full id like
// "alice-sounds-game-win-1" into a bare, matchable word list by
// stripping a trailing "-<number>" (the library's own "variant N"
// suffix) and swapping "-" for " ". Used as the English-ish search
// surface for a query, since these ids already read as English words.
func slug(id string) string {
	parts := strings.Split(id, "-")
	if n := len(parts); n > 1 {
		if _, err := strconv.Atoi(parts[n-1]); err == nil {
			parts = parts[:n-1]
		}
	}
	return strings.Join(parts, " ")
}

// FindEffect fuzzy-matches query (Russian or English, full or partial)
// against the sound_play catalog: exact id first, then an id whose slug
// exactly equals the query, then substring matches against the id's
// slug or the Russian name. Returns ok=false with candidates listing
// every partial match when the query is ambiguous (more than one hit)
// or matches nothing at all.
func FindEffect(query string) (id string, candidates []Effect, ok bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return "", nil, false
	}
	for _, e := range effects {
		if strings.EqualFold(e.ID, q) {
			return e.ID, nil, true
		}
	}
	var exact, partial []Effect
	for _, e := range effects {
		s := strings.ToLower(slug(e.ID))
		if s == q {
			exact = append(exact, e)
			continue
		}
		if strings.Contains(s, q) || strings.Contains(strings.ToLower(e.NameRU), q) {
			partial = append(partial, e)
		}
	}
	if len(exact) == 1 {
		return exact[0].ID, nil, true
	}
	if len(exact) > 1 {
		return "", exact, false
	}
	if len(partial) == 1 {
		return partial[0].ID, nil, true
	}
	return "", partial, false
}

// FindSpeakerAudio is FindEffect for the SpeakerAudio catalog.
func FindSpeakerAudio(query string) (fullID string, candidates []SpeakerAudio, ok bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return "", nil, false
	}
	for _, a := range speakerAudios {
		if strings.EqualFold(a.FullID, q) {
			return a.FullID, nil, true
		}
	}
	var exact, partial []SpeakerAudio
	for _, a := range speakerAudios {
		s := strings.ToLower(slug(a.FullID))
		if s == q {
			exact = append(exact, a)
			continue
		}
		if strings.Contains(s, q) || strings.Contains(strings.ToLower(a.NameRU), q) {
			partial = append(partial, a)
		}
	}
	if len(exact) == 1 {
		return exact[0].FullID, nil, true
	}
	if len(exact) > 1 {
		return "", exact, false
	}
	if len(partial) == 1 {
		return partial[0].FullID, nil, true
	}
	return "", partial, false
}

// FormatCandidates renders an ambiguous/failed lookup's candidates as a
// short human-readable list, for error messages.
func FormatCandidates[T Effect | SpeakerAudio](query string, candidates []T) string {
	if len(candidates) == 0 {
		return fmt.Sprintf("звук не найден: %q", query)
	}
	var lines []string
	for _, c := range candidates {
		switch v := any(c).(type) {
		case Effect:
			lines = append(lines, fmt.Sprintf("%s (%s)", v.ID, v.NameRU))
		case SpeakerAudio:
			lines = append(lines, fmt.Sprintf("%s (%s)", v.FullID, v.NameRU))
		}
	}
	return fmt.Sprintf("неоднозначный запрос %q, уточни:\n  %s", query, strings.Join(lines, "\n  "))
}
