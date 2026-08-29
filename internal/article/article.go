// Package article fetches a URL and turns the page into readable, sectioned
// text for Alice to read aloud. HTML pages are reduced to their main content
// with headings preserved as markdown "#" lines — internal/app's batch
// splitter treats a heading line as a chunk boundary (splitHeadings), so a
// long article comes out as digestible sections instead of one wall of text.
package article

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Article is the readable form of a fetched page.
type Article struct {
	// Title is the <title> of an HTML page, "" for plain text.
	Title string
	// Text is the extracted body: plain text, or markdown-ish with "#"
	// heading lines. For non-HTML sources it's the raw trimmed body.
	Text string
	// Source is the final URL after redirects.
	Source string
}

// maxBodyBytes caps how much of a page we'll download.
const maxBodyBytes = 5 << 20 // 5 MiB

// userAgent keeps servers that gate on a real browser from 403'ing; Go's
// default "Go-http-client/1.1" is commonly blocked.
const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// Fetch downloads url and extracts the readable text. HTML responses go
// through htmlToMarkdown; plain text/markdown come back as-is. The context
// bounds the request (cancel + deadline).
func Fetch(ctx context.Context, url string) (*Article, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("url: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,text/plain;q=0.9,*/*;q=0.5")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("не достучался до %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s ответил %s", url, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("читать ответ: %w", err)
	}

	source := url
	if resp.Request != nil && resp.Request.URL != nil {
		source = resp.Request.URL.String()
	}

	ctype := resp.Header.Get("Content-Type")
	switch {
	case strings.Contains(ctype, "html") || looksLikeHTML(body):
		return parseHTML(source, body)
	case strings.TrimSpace(ctype) == "" && strings.Contains(ctype, "text"):
		// text/* without explicit charset — treat as plain text.
		fallthrough
	default:
		return &Article{Text: strings.TrimSpace(string(body)), Source: source}, nil
	}
}

// looksLikeHTML guesses from the bytes that a body is HTML even when the
// server sent no Content-Type or a bare "text/plain" (some engines do).
func looksLikeHTML(body []byte) bool {
	head := string(body)
	trimmed := strings.TrimLeftFunc(head, func(r rune) bool {
		return r == '\uFEFF' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
	if strings.HasPrefix(strings.ToLower(trimmed), "<!doctype html") ||
		strings.HasPrefix(strings.ToLower(trimmed), "<html") ||
		strings.HasPrefix(strings.ToLower(trimmed), "<head") {
		return true
	}
	return false
}

// parseHTML parses body into an Article with heading lines preserved.
func parseHTML(source string, body []byte) (*Article, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		// x/net/html is lenient and almost never errors; if it does, fall
		// back to the raw text rather than failing the whole read.
		return &Article{Text: strings.TrimSpace(string(body)), Source: source}, nil
	}
	title := extractTitle(doc)
	root := contentRoot(doc)
	var b strings.Builder
	walk(root, &b)
	return &Article{
		Title:  title,
		Text:   cleanText(b.String()),
		Source: source,
	}, nil
}

// extractTitle returns the <title> text, trimmed.
func extractTitle(doc *html.Node) string {
	var found string
	var find func(n *html.Node)
	find = func(n *html.Node) {
		if found != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "title" {
			found = strings.TrimSpace(textOf(n))
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	return found
}

// contentRoot picks the element whose text is the article: the first
// <article>, else the first <main>, else <body> — searching the whole tree
// for each in that priority order (document order alone would hand <main>
// back before a nested <article>).
func contentRoot(doc *html.Node) *html.Node {
	for _, want := range []string{"article", "main", "body"} {
		var found *html.Node
		var find func(n *html.Node)
		find = func(n *html.Node) {
			if found != nil {
				return
			}
			if n.Type == html.ElementNode && n.Data == want {
				found = n
				return
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				find(c)
			}
		}
		find(doc)
		if found != nil {
			return found
		}
	}
	return doc
}

// skipTags are subtrees that never hold article prose (or carry boilerplate
// we don't want Alice to read).
var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "iframe": true, "object": true, "embed": true,
	"nav": true, "header": true, "footer": true, "aside": true,
	"form": true, "button": true, "select": true, "textarea": true,
	"input": true, "option": true, "canvas": true, "dialog": true,
}

// blockTags start and end a new line around their content.
var blockTags = map[string]bool{
	"p": true, "div": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "li": true, "blockquote": true,
	"pre": true, "br": true, "ul": true, "ol": true, "section": true,
	"article": true, "table": true, "tr": true, "td": true, "th": true,
	"hr": true, "figure": true, "figcaption": true, "main": true,
	"body": true, "dl": true, "dt": true, "dd": true, "details": true,
	"summary": true,
}

// walk emits node text into b, writing markdown "#" lines for headings and
// a newline per block boundary. Whitespace is normalized later by cleanText.
func walk(n *html.Node, b *strings.Builder) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(n.Data)
	case html.ElementNode:
		tag := n.Data
		if skipTags[tag] || isHidden(n) {
			return
		}
		if lv := headingLevel(n); lv != 0 {
			if text := strings.TrimSpace(textOf(n)); text != "" {
				b.WriteByte('\n')
				for i := 0; i < lv; i++ {
					b.WriteByte('#')
				}
				b.WriteByte(' ')
				b.WriteString(text)
			}
			return
		}
		switch tag {
		case "br":
			b.WriteByte('\n')
			return
		case "img":
			if alt := attr(n, "alt"); strings.TrimSpace(alt) != "" {
				b.WriteString(" " + strings.TrimSpace(alt))
			}
			return
		}
		if blockTags[tag] {
			b.WriteByte('\n')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, b)
		}
		if blockTags[tag] {
			b.WriteByte('\n')
		}
	}
}

// headingLevel returns 1..6 for an h1..h6 element, else 0.
func headingLevel(n *html.Node) int {
	if n.Type != html.ElementNode || len(n.Data) != 2 || n.Data[0] != 'h' {
		return 0
	}
	if lv := n.Data[1] - '0'; lv >= 1 && lv <= 6 {
		return int(lv)
	}
	return 0
}

// textOf gathers the concatenated text of node's descendants (inline tags
// included), skipping only the skipTags subtrees.
func textOf(n *html.Node) string {
	var b strings.Builder
	var rec func(n *html.Node)
	rec = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
		case html.ElementNode:
			if skipTags[n.Data] || isHidden(n) {
				return
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				rec(c)
			}
		}
	}
	rec(n)
	return b.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// isHidden skips elements that won't render: the hidden attribute, or an
// inline style of display:none (the cheap, dependency-free common case).
func isHidden(n *html.Node) bool {
	for _, a := range n.Attr {
		if a.Key == "hidden" {
			return true
		}
		if a.Key == "style" && strings.Contains(strings.ToLower(strings.ReplaceAll(a.Val, " ", "")), "display:none") {
			return true
		}
	}
	return false
}

// cleanText collapses every whitespace run to a single space, drops empty
// lines, and joins the rest with "\n".
func cleanText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if ln = strings.Join(strings.Fields(ln), " "); ln != "" {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}
