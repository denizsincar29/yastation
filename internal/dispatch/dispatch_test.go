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
