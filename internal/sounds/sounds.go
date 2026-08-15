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
// speaker_audio is transcribed from Yandex's own docs (not derived or
// guessed, but not exhaustive either — no full id space is documented).
//
// sound_play has no documented catalog at all: the entries here started
// as a hand-assembled list of plausible ids (many "-2"/"-3" variants
// guessed by symmetry from a confirmed "-1"), only one of which
// (chainsaw-1) was originally checked against a real capabilities dump.
// The list has since been re-verified end-to-end against a real device
// with cmd/yastation-soundcheck: 33 of the original ~95 guessed ids
// actually work, the rest returned BAD_REQUEST and were removed by
// cmd/yastation-soundcheck-apply. A second pass (cmd/yastation-soundcheck
// -ids-file candidates_stripped.json) tried progressive hyphen-strips of
// every id that failed — e.g. "human-cough-1" without its leading
// segment, "cough-1" — and found 11 more real ids hiding under shorter
// names than their category-prefixed originals (cough-1/2, door-1/2,
// horn-1/2, laugh-1/2/3, sneeze-1, walking-dead-1). Every id currently
// in sound_play.json (44 total) is confirmed against real hardware, not
// guessed.
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
// find is the shared fuzzy-match engine behind FindEffect and
// FindSpeakerAudio: exact id match first; then an id whose "full slug"
// (id with "-" turned into spaces, digits kept — e.g. "win 1") or "base
// slug" (same, but with the trailing "-N" variant number stripped —
// "win") exactly equals the query; then a substring match against the
// full slug or the Russian name. Ambiguous (2+ partial matches) or
// empty results come back as candidates with ok=false rather than
// guessing.
func find[T any](query string, items []T, id func(T) string, name func(T) string) (matchID string, candidates []T, ok bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return "", nil, false
	}
	for _, it := range items {
		if strings.EqualFold(id(it), q) {
			return id(it), nil, true
		}
	}
	var exact, partial []T
	for _, it := range items {
		full := strings.ToLower(strings.ReplaceAll(id(it), "-", " "))
		base := strings.ToLower(slug(id(it)))
		switch {
		case full == q || base == q:
			exact = append(exact, it)
		case strings.Contains(full, q) || strings.Contains(strings.ToLower(name(it)), q):
			partial = append(partial, it)
		}
	}
	if len(exact) == 1 {
		return id(exact[0]), nil, true
	}
	if len(exact) > 1 {
		return "", exact, false
	}
	if len(partial) == 1 {
		return id(partial[0]), nil, true
	}
	return "", partial, false
}

// FindEffect fuzzy-matches query (Russian or English, full or partial —
// e.g. "explosion", "win 1", "Раскат грома 1") against the sound_play
// catalog. See find for the exact matching rules.
func FindEffect(query string) (id string, candidates []Effect, ok bool) {
	return find(query, effects,
		func(e Effect) string { return e.ID },
		func(e Effect) string { return e.NameRU })
}

// FindSpeakerAudio is FindEffect for the SpeakerAudio catalog.
func FindSpeakerAudio(query string) (fullID string, candidates []SpeakerAudio, ok bool) {
	return find(query, speakerAudios,
		func(a SpeakerAudio) string { return a.FullID },
		func(a SpeakerAudio) string { return a.NameRU })
}

// EffectNameByID returns the Russian display name for a sound_play id
// (e.g. "explosion-2" -> "Взрыв 2"), or "" if the id isn't in the
// catalog. Needed because Yandex's sound_play capability now validates
// that the scenario's value carries *both* "sound" and "sound_name" —
// sending "sound" alone (which used to be accepted) now comes back as
// BAD_REQUEST.
func EffectNameByID(id string) string {
	for _, e := range effects {
		if e.ID == id {
			return e.NameRU
		}
	}
	return ""
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
