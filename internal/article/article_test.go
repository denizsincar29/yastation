package article

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const sampleHTML = `<!DOCTYPE html>
<html>
<head><title>Настройка Baofeng BF-888S — Блог радиолюбителя</title></head>
<body>
<header><h1>Шапка блога</h1><nav><a href="/">Главная</a></nav></header>
<main>
<article>
<h1>Baofeng BF-888S: первая настройка</h1>
<p>Рация работает на частотах УКВ. Начнём с <strong>программирования</strong> каналов.</p>
<h2>Как ввести частоту</h2>
<p>Включите рацию и наберите частоту на цифровой клавиатуре.</p>
<ul><li>Шаг 1: нажмите меню.</li><li>Шаг 2: выберите канал.</li></ul>
<script>document.write("это не текст")</script>
<p style="display:none">скрытый текст тоже не читаем</p>
<h3>Совет</h3>
<blockquote>Храните копию прошивки.</blockquote>
</article>
</main>
<footer>Подвал блога — контакты</footer>
</body>
</html>`

func TestParseHTMLPreservesHeadingsAndSkipsBoilerplate(t *testing.T) {
	doc := mustParse(t, sampleHTML)
	root := contentRoot(doc)
	var b strings.Builder
	walk(root, &b)
	got := cleanText(b.String())

	for _, wantLine := range []string{
		"# Baofeng BF-888S: первая настройка",
		"## Как ввести частоту",
		"### Совет",
		"Рация работает на частотах УКВ.",
		"Шаг 1: нажмите меню.",
		"Храните копию прошивки.",
	} {
		if !strings.Contains(got, wantLine) {
			t.Fatalf("missing %q in:\n%s", wantLine, got)
		}
	}
	for _, bad := range []string{"Шапка блога", "Подвал блога", "это не текст", "скрытый текст", "Главная"} {
		if strings.Contains(got, bad) {
			t.Fatalf("boilerplate leaked: %q in:\n%s", bad, got)
		}
	}
	if strings.Contains(got, "## Главная") {
		t.Fatalf("nav heading leaked:\n%s", got)
	}
}

func TestExtractTitle(t *testing.T) {
	doc := mustParse(t, sampleHTML)
	if got := extractTitle(doc); got != "Настройка Baofeng BF-888S — Блог радиолюбителя" {
		t.Fatalf("title=%q", got)
	}
}

func TestContentRootPrefersArticle(t *testing.T) {
	doc := mustParse(t, sampleHTML)
	root := contentRoot(doc)
	if root.Data != "article" {
		t.Fatalf("content root=%q, want article", root.Data)
	}
}

func TestFetchHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>Тест</title></head><body><article><h1>Заголовок</h1><p>Тело статьи.</p></article></body></html>`))
	}))
	defer srv.Close()

	art, err := Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if art.Title != "Тест" {
		t.Fatalf("title=%q", art.Title)
	}
	if art.Text != "# Заголовок\nТело статьи." {
		t.Fatalf("text=%q", art.Text)
	}
	if art.Source != srv.URL {
		t.Fatalf("source=%q", art.Source)
	}
}

func TestFetchPlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("просто\nтекст\n"))
	}))
	defer srv.Close()

	art, err := Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if art.Text != "просто\nтекст" {
		t.Fatalf("text=%q", art.Text)
	}
	if art.Title != "" {
		t.Fatalf("plain text shouldn't have a title: %q", art.Title)
	}
}

func TestFetchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestFetchBadURL(t *testing.T) {
	if _, err := Fetch(context.Background(), "not a url"); err == nil {
		t.Fatal("expected error for malformed URL")
	}
}

func mustParse(t *testing.T, s string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}
