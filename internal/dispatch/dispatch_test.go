package dispatch

import (
	"context"
	"strings"
	"testing"
)

func TestDefaultHandlerForFreeText(t *testing.T) {
	d := New()
	var got string
	d.Default = func(ctx context.Context, text string) (string, error) {
		got = text
		return "ok", nil
	}
	out, err := d.Execute(context.Background(), "привет с компа")
	if err != nil {
		t.Fatal(err)
	}
	if got != "привет с компа" || out != "ok" {
		t.Fatalf("got=%q out=%q", got, out)
	}
}

func TestCommandWithArgsAndQuotes(t *testing.T) {
	d := New()
	var gotArgs []string
	d.Handle("test", func(ctx context.Context, args []string) (string, error) {
		gotArgs = args
		return "done", nil
	}, "say")

	out, err := d.Execute(context.Background(), `/say "привет мир" foo`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out=%q", out)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "привет мир" || gotArgs[1] != "foo" {
		t.Fatalf("gotArgs=%v", gotArgs)
	}
}

func TestAliases(t *testing.T) {
	d := New()
	calls := 0
	d.Handle("cmd", func(ctx context.Context, args []string) (string, error) {
		calls++
		return "", nil
	}, "cmd", "c", "ask")

	for _, line := range []string{"/cmd hi", "/c hi", "/ask hi"} {
		if _, err := d.Execute(context.Background(), line); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls via aliases, got %d", calls)
	}
}

func TestUnknownCommandError(t *testing.T) {
	d := New()
	_, err := d.Execute(context.Background(), "/nope")
	if err == nil || !strings.Contains(err.Error(), "неизвестная") {
		t.Fatalf("expected unknown command error, got %v", err)
	}
}

func TestUnclosedQuoteError(t *testing.T) {
	d := New()
	d.Handle("x", func(ctx context.Context, args []string) (string, error) { return "", nil }, "x")
	_, err := d.Execute(context.Background(), `/x "unterminated`)
	if err == nil {
		t.Fatal("expected error for unclosed quote")
	}
}

func TestHelpGroupsByCategoryNotJustAlphabet(t *testing.T) {
	d := New()
	noop := func(ctx context.Context, args []string) (string, error) { return "", nil }
	// "Основное" is registered first, so it must come first in Help()
	// output regardless of alphabetical order relative to "Плеер".
	d.HandleCat("Основное", "say help", noop, "say")
	d.HandleCat("Плеер", "play help", noop, "play")
	d.HandleCat("Плеер", "pause help", noop, "pause")

	out := d.Help()
	iBasic := strings.Index(out, "Основное:")
	iPlayer := strings.Index(out, "Плеер:")
	iPause := strings.Index(out, "/pause")
	iPlay := strings.Index(out, "/play  ")
	if iBasic == -1 || iPlayer == -1 {
		t.Fatalf("missing category headers: %q", out)
	}
	if iBasic > iPlayer {
		t.Fatalf("expected Основное category before Плеер (registration order), got %q", out)
	}
	// within a category, alphabetical: pause before play
	if iPause == -1 || iPlay == -1 || iPause > iPlay {
		t.Fatalf("expected pause before play within category, got %q", out)
	}
}

func TestQuestionMarkReturnsSingleCommandHelp(t *testing.T) {
	d := New()
	noop := func(ctx context.Context, args []string) (string, error) { return "ran", nil }
	d.Handle("Голосовая команда", noop, "cmd", "c", "ask")

	out, err := d.Execute(context.Background(), "/cmd?")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/cmd") || !strings.Contains(out, "Голосовая команда") {
		t.Fatalf("out=%q", out)
	}
	if !strings.Contains(out, "/c") || !strings.Contains(out, "/ask") {
		t.Fatalf("expected aliases listed, out=%q", out)
	}
}

func TestQuestionMarkOnUnknownCommandErrors(t *testing.T) {
	d := New()
	_, err := d.Execute(context.Background(), "/nope?")
	if err == nil {
		t.Fatal("expected error for unknown command help")
	}
}

func TestBareQuestionMarkStillWorksAsHelpAlias(t *testing.T) {
	d := New()
	called := false
	d.Handle("список команд", func(ctx context.Context, args []string) (string, error) {
		called = true
		return "helped", nil
	}, "help", "?")

	out, err := d.Execute(context.Background(), "/?")
	if err != nil {
		t.Fatal(err)
	}
	if !called || out != "helped" {
		t.Fatalf("bare /? should still call the help handler, called=%v out=%q", called, out)
	}
}
