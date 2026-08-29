package app

import (
	"fmt"
	"strings"

	"github.com/denizsincar29/yastation/internal/quasar"
	"github.com/denizsincar29/yastation/internal/sounds"
)

// speech.go — markup helpers for /say, /whisper, and the bare-text
// default handler.
//
// Two independent bits of markup:
//
//   - ((whispered text)) — Alice's tts capability takes a single
//     whisper bool for the *entire* call (see quasar.Client.SayWhisper)
//     — there's no known way to toggle it mid-utterance in one call —
//     so mixing whispered and normal speech in one /say actually sends
//     multiple TTS calls back to back, one per ((...)) boundary, not one
//     seamless utterance. A whole line can also be whisper-only via the
//     "~" prefix (mirrors "- " for /cmd) or /whisper, without needing
//     the parens.
//
//   - [sound query] — expands, via internal/sounds.FindSpeakerAudio, to
//     a <speaker audio="....opus"> tag inserted right there in the
//     text — Yandex's own embeddable-sound markup for TTS
//     (https://yandex.ru/dev/dialogs/alice/doc/ru/sounds), confirmed
//     working by hand. Unlike whisper, this one *can* sit inside a
//     single call — it's just literal text Alice's TTS engine already
//     understands, no separate call needed.

type speechSegment struct {
	Text    string
	Whisper bool
}

// splitWhisperSegments splits text on ((...)) into ordered segments,
// each tagged with whether it's inside double parens. Segments that end
// up empty after trimming are dropped. An unterminated "((" runs to the
// end of the string as a whispered segment rather than being silently
// discarded.
func splitWhisperSegments(text string) []speechSegment {
	var segs []speechSegment
	for text != "" {
		start := strings.Index(text, "((")
		if start == -1 {
			if s := strings.TrimSpace(text); s != "" {
				segs = append(segs, speechSegment{Text: s})
			}
			break
		}
		if before := strings.TrimSpace(text[:start]); before != "" {
			segs = append(segs, speechSegment{Text: before})
		}
		rest := text[start+2:]
		end := strings.Index(rest, "))")
		if end == -1 {
			if s := strings.TrimSpace(rest); s != "" {
				segs = append(segs, speechSegment{Text: s, Whisper: true})
			}
			break
		}
		if s := strings.TrimSpace(rest[:end]); s != "" {
			segs = append(segs, speechSegment{Text: s, Whisper: true})
		}
		text = rest[end+2:]
	}
	return segs
}

// expandSoundTags replaces every "[query]" or "№query№" marker in text
// with the matching <speaker audio="....opus"> tag. Two delimiter
// styles because "[" sits off the Russian keyboard layout — typing it
// means switching to Latin first, same as "~" does for the
// whisper-line prefix (see registerCommands, which for the same reason
// also accepts ";" alongside "~"). "№" (Shift+3 on a Russian layout)
// stays on the same layout as the Russian text around it and is just as
// rare to run into by accident in ordinary speech as "[" already was.
// Whichever opener — "[" or "№" — comes first in the remaining text
// wins for that marker; its own matching closer ("]" or another "№")
// is what ends it, the two styles don't mix within one marker. An
// unmatched or ambiguous query is a hard error rather than left as
// literal "[...]"/"№...№" text — Alice would just read the delimiters
// aloud, which is never what's wanted — listing candidates when there's
// more than one match.
func expandSoundTags(text string) (string, error) {
	const numero = "№"
	var b strings.Builder
	for text != "" {
		bracketIdx := strings.IndexByte(text, '[')
		numeroIdx := strings.Index(text, numero)

		var start int
		var openLen int
		var closeDelim string
		switch {
		case bracketIdx == -1 && numeroIdx == -1:
			b.WriteString(text)
			text = ""
			continue
		case numeroIdx == -1 || (bracketIdx != -1 && bracketIdx < numeroIdx):
			start, openLen, closeDelim = bracketIdx, 1, "]"
		default:
			start, openLen, closeDelim = numeroIdx, len(numero), numero
		}

		b.WriteString(text[:start])
		rest := text[start+openLen:]
		end := strings.Index(rest, closeDelim)
		if end == -1 {
			// No closing delimiter — not our markup, keep it literal.
			b.WriteString(text[start:])
			text = ""
			continue
		}
		query := strings.TrimSpace(rest[:end])
		fullID, candidates, ok := sounds.FindSpeakerAudio(query)
		if !ok {
			return "", fmt.Errorf("%s", sounds.FormatCandidates(query, candidates))
		}
		fmt.Fprintf(&b, `<speaker audio="%s.opus">`, fullID)
		text = rest[end+len(closeDelim):]
	}
	return b.String(), nil
}

// batchActions turns a batch phrases string into ordered say actions, each
// fitting quasar.MaxTTSChunkChars. An explicit "|" separator forces a
// boundary between parts; inside a part, ((...)) whisper segments become
// their own whisper steps and [query]/№query№ sound tags are expanded
// first, so chunking never cuts through one. This is how /batch speaks a
// long story that mixes normal voice, a whispered aside and embedded
// sounds — all as one cloud scenario.
func batchActions(text string) ([]quasar.BatchAction, error) {
	var acts []quasar.BatchAction
	for _, part := range strings.Split(text, "|") {
		for _, seg := range splitWhisperSegments(part) {
			expanded, err := expandSoundTags(seg.Text)
			if err != nil {
				return nil, err
			}
			for _, chunk := range chunkSoundSafe(expanded, quasar.MaxTTSChunkChars) {
				acts = append(acts, quasar.BatchAction{Kind: "say", Text: chunk, Whisper: seg.Whisper})
			}
		}
	}
	return acts, nil
}

// chunkSoundSafe splits expanded TTS text — which may contain embedded
// <speaker audio="..."> tags — into pieces that each fit within max,
// never cutting through a tag. Tags are short enough to always fit on
// their own, so each is treated as an atomic token: a chunk either holds
// the whole tag or none of it.
func chunkSoundSafe(text string, max int) []string {
	var chunks []string
	var cur []rune
	for _, piece := range soundPieces(text) {
		pr := []rune(piece)
		if len(cur)+len(pr) <= max {
			cur = append(cur, pr...)
			continue
		}
		if s := strings.TrimSpace(string(cur)); s != "" {
			chunks = append(chunks, s)
		}
		cur = nil
		if len(pr) > max {
			// Only a long literal run lands here — a tag is short. Reuse the
			// plain chunker, which keeps sentence/comma/space boundaries (the
			// piece holds no tags to protect).
			chunks = append(chunks, splitChunks(piece, max)...)
			continue
		}
		cur = append(cur, pr...)
	}
	if s := strings.TrimSpace(string(cur)); s != "" {
		chunks = append(chunks, s)
	}
	return chunks
}

// soundPieces splits expanded TTS text into alternating literal and sound
// tag pieces, preserving order.
func soundPieces(text string) []string {
	var pieces []string
	for text != "" {
		i := strings.Index(text, "<speaker audio=")
		if i == -1 {
			pieces = append(pieces, text)
			break
		}
		if i > 0 {
			pieces = append(pieces, text[:i])
		}
		rest := text[i:]
		j := strings.IndexByte(rest, '>')
		if j == -1 {
			pieces = append(pieces, rest)
			break
		}
		pieces = append(pieces, rest[:j+1])
		text = rest[j+1:]
	}
	return pieces
}

func splitChunks(text string, max int) []string {
	runes := []rune(text)
	if len(runes) <= max {
		if text != "" {
			return []string{text}
		}
		return nil
	}
	var out []string
	start := 0
	for start < len(runes) {
		end := start + max
		if end >= len(runes) {
			// The rest already fits — take it whole instead of hunting a
			// boundary inside it (avoids splitting the final chunk more
			// than needed).
			if s := strings.TrimSpace(string(runes[start:])); s != "" {
				out = append(out, s)
			}
			break
		}
		cut := bestCut(runes, start, end)
		if cut <= start {
			cut = end
		}
		if s := strings.TrimSpace(string(runes[start:cut])); s != "" {
			out = append(out, s)
		}
		start = cut
	}
	return out
}

// bestCut returns the index just past the last good boundary in
// runes[start:end]: a sentence ender followed by whitespace (so a decimal
// point like "3.5" isn't taken for one), else the last
// comma/semicolon/dash, else the last space. 0 if there's no boundary at
// all — caller hard-splits.
func bestCut(runes []rune, start, end int) int {
	for i := end - 1; i >= start; i-- {
		if !isSentenceEnd(runes[i]) {
			continue
		}
		if i+1 < len(runes) && runes[i+1] != ' ' {
			continue
		}
		return i + 1
	}
	for i := end - 1; i >= start; i-- {
		switch runes[i] {
		case ',', ';', '—':
			return i + 1
		}
	}
	for i := end - 1; i >= start; i-- {
		if runes[i] == ' ' {
			return i + 1
		}
	}
	return 0
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?', '…':
		return true
	}
	return false
}

// speak sends text to station: split on ((whisper)) boundaries into
// separate sequential TTS calls, expanding [sound] markers within each
// one first. Returns the original (unexpanded) text in the confirmation
// message — the markup is an implementation detail, not what the
// person actually asked to say.
func speak(client StationAPI, station, text string) (string, error) {
	segs := splitWhisperSegments(text)
	if len(segs) == 0 {
		return "", fmt.Errorf("нечего сказать")
	}
	for _, seg := range segs {
		expanded, err := expandSoundTags(seg.Text)
		if err != nil {
			return "", err
		}
		if seg.Whisper {
			err = client.SayWhisper(station, expanded)
		} else {
			err = client.Say(station, expanded)
		}
		if err != nil {
			return "", err
		}
	}
	return "Алиса сказала: " + text, nil
}
