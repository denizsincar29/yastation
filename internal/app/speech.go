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
	var b strings.Builder
	for _, p := range splitMarkers(text) {
		if p.kind == "text" {
			b.WriteString(p.text)
			continue
		}
		fullID, candidates, ok := sounds.FindSpeakerAudio(p.text)
		if !ok {
			return "", fmt.Errorf("%s", sounds.FormatCandidates(p.text, candidates))
		}
		fmt.Fprintf(&b, `<speaker audio="%s.opus">`, fullID)
	}
	return b.String(), nil
}

// soundMark is one marker-level piece of text: a literal run, or a single
// "[query]"/"№query№" sound marker (the query still unresolved, so the
// batch can choose between inlining it and playing it as a sound_play step).
type soundMark struct {
	kind string // "text" | "sound"
	text string // literal text for "text", the query for "sound"
}

// splitMarkers splits text on sound markers, preserving order. The marker
// rules are identical to what expandSoundTags used to parse inline: "["
// and "№" both open, the earlier of the two wins, each closes on its own
// delimiter ("]" / "№"), and an unterminated opener stays literal. An
// empty marker is kept as a "sound" piece — the resolver rejects it later.
func splitMarkers(text string) []soundMark {
	const numero = "№"
	var pieces []soundMark
	for text != "" {
		bracketIdx := strings.IndexByte(text, '[')
		numeroIdx := strings.Index(text, numero)

		var start int
		var openLen int
		var closeDelim string
		switch {
		case bracketIdx == -1 && numeroIdx == -1:
			pieces = append(pieces, soundMark{kind: "text", text: text})
			text = ""
			continue
		case numeroIdx == -1 || (bracketIdx != -1 && bracketIdx < numeroIdx):
			start, openLen, closeDelim = bracketIdx, 1, "]"
		default:
			start, openLen, closeDelim = numeroIdx, len(numero), numero
		}

		if start > 0 {
			pieces = append(pieces, soundMark{kind: "text", text: text[:start]})
		}
		rest := text[start+openLen:]
		end := strings.Index(rest, closeDelim)
		if end == -1 {
			// No closing delimiter — not our markup, keep it literal.
			pieces = append(pieces, soundMark{kind: "text", text: text[start:]})
			text = ""
			continue
		}
		pieces = append(pieces, soundMark{kind: "sound", text: strings.TrimSpace(rest[:end])})
		text = rest[end+len(closeDelim):]
	}
	return pieces
}

// batchActions turns a batch phrases string into ordered actions, each say
// step fitting quasar.MaxTTSChunkChars. An explicit "|" separator forces a
// boundary between parts; inside a part, ((...)) whisper segments become
// their own whisper steps and every markdown heading starts a fresh chunk
// (splitHeadings). A [query]/№query№ sound marker is inlined as a
// <speaker audio="..."> tag when the current chunk has room, else it plays
// as its own sound_play step (see batchSegmentActions) — so a long article
// or story that mixes normal voice, a whispered aside and several sounds
// still fits one cloud scenario, section by section.
func batchActions(text string) ([]quasar.BatchAction, error) {
	var acts []quasar.BatchAction
	for _, part := range strings.Split(text, "|") {
		for _, seg := range splitWhisperSegments(part) {
			for _, piece := range splitHeadings(seg.Text) {
				segActs, err := batchSegmentActions(speechSegment{Text: piece, Whisper: seg.Whisper}, quasar.MaxTTSChunkChars)
				if err != nil {
					return nil, err
				}
				acts = append(acts, segActs...)
			}
		}
	}
	return acts, nil
}

// splitHeadings splits text on markdown heading lines ("#"/"##"/... at the
// start of a line) into sections. Every piece after the first begins where a
// heading started, and the "#" marks are stripped so Alice reads the section
// title, not the syntax — an article's sections become its own chunks
// instead of one wall of text. Prose with no headings comes back as a single
// piece.
func splitHeadings(text string) []string {
	var segs []string
	var cur []string
	flush := func() {
		if s := strings.TrimSpace(strings.Join(cur, "\n")); s != "" {
			segs = append(segs, s)
		}
		cur = nil
	}
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := headingLine(line); ok {
			flush()
			cur = []string{rest}
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return segs
}

// headingLine reports whether line is a markdown heading — 1..6 "#"s followed
// by a space or the end of the line (so "#foo" stays prose) — returning the
// line with the marks stripped. Leading whitespace is tolerated.
func headingLine(line string) (string, bool) {
	s := strings.TrimLeft(line, " \t")
	hashes := 0
	for hashes < len(s) && s[hashes] == '#' {
		hashes++
	}
	if hashes == 0 || hashes > 6 {
		return "", false
	}
	rest := s[hashes:]
	if rest != "" && rest[0] != ' ' {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// batchSegmentActions turns one whisper segment into ordered batch actions.
// Sound markers are resolved in the embeddable speaker-audio library and
// inlined as <speaker audio="..."> tags whenever the current chunk has
// room; when a tag would overflow the cap, the sound instead plays as a
// standalone sound_play step (quasar.BatchAction Kind "sound") if the same
// query also resolves in the sound_play library. Only when neither fits is
// the tag pushed into its own next chunk, so a sound is never lost. Long
// literal runs are split by splitChunks, keeping sentence boundaries.
func batchSegmentActions(seg speechSegment, max int) ([]quasar.BatchAction, error) {
	var acts []quasar.BatchAction
	var cur []rune
	flush := func() {
		if s := strings.TrimSpace(string(cur)); s != "" {
			acts = append(acts, quasar.BatchAction{Kind: "say", Text: s, Whisper: seg.Whisper})
		}
		cur = nil
	}
	for _, p := range splitMarkers(seg.Text) {
		if p.kind == "text" {
			pr := []rune(p.text)
			if len(cur)+len(pr) <= max {
				cur = append(cur, pr...)
				continue
			}
			flush()
			if len(pr) > max {
				for _, c := range splitChunks(p.text, max) {
					acts = append(acts, quasar.BatchAction{Kind: "say", Text: c, Whisper: seg.Whisper})
				}
				continue
			}
			cur = append(cur, pr...)
			continue
		}
		fullID, candidates, ok := sounds.FindSpeakerAudio(p.text)
		if !ok {
			return nil, fmt.Errorf("%s", sounds.FormatCandidates(p.text, candidates))
		}
		tag := fmt.Sprintf(`<speaker audio="%s.opus">`, fullID)
		if len(cur)+len([]rune(tag)) <= max {
			cur = append(cur, []rune(tag)...)
			continue
		}
		flush()
		if id, name, ok := soundEffect(p.text); ok {
			acts = append(acts, quasar.BatchAction{Kind: "sound", SoundID: id, SoundName: name})
			continue
		}
		cur = append(cur, []rune(tag)...)
	}
	flush()
	return acts, nil
}

// soundEffect resolves query in the sound_play library, returning the
// standalone-effect id and its Russian display name. Best-effort: a query
// that doesn't resolve uniquely there simply reports ok=false, so callers
// keep the inline tag instead of inventing a sound.
func soundEffect(query string) (id, name string, ok bool) {
	id, _, found := sounds.FindEffect(query)
	if !found || id == "" {
		return "", "", false
	}
	return id, sounds.EffectNameByID(id), true
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
		// The ender is a real sentence boundary if what follows is
		// whitespace or the end of the text — skipping any trailing
		// closing quote/paren, so «слово.» and (текст.) still count.
		// A literal "3.5" isn't: after the dot comes a digit, not
		// whitespace. Whitespace, not just " ", because the article
		// extractor joins paragraphs with "\n" — a period at the end
		// of a paragraph is followed by a newline, and dropping those
		// was cutting sentences at commas instead.
		j := i + 1
		for j < len(runes) && isClosingRune(runes[j]) {
			j++
		}
		if j < len(runes) && !isWhitespace(runes[j]) {
			continue
		}
		if j > end {
			j = end
		}
		return j
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

// isClosingRune reports whether r can sit between a sentence ender and the
// following whitespace without breaking the boundary — closing quotes and
// brackets, which in prose attach to the word before them («слово.»).
func isClosingRune(r rune) bool {
	switch r {
	case '»', '"', '\'', ')', ']', '}':
		return true
	}
	return false
}

func isWhitespace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r':
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
