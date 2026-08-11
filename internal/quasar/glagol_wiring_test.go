package quasar

import (
	"reflect"
	"testing"
)

func TestParseGlagolHosts(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want map[string]string
	}{
		{"empty", "", map[string]string{}},
		{"single", "abc123=192.168.1.42:1961", map[string]string{"abc123": "192.168.1.42:1961"}},
		{
			"multiple",
			"a=1.2.3.4:1961,b=5.6.7.8:1961",
			map[string]string{"a": "1.2.3.4:1961", "b": "5.6.7.8:1961"},
		},
		{
			"whitespace tolerated",
			" a = 1.2.3.4:1961 , b=5.6.7.8:1961 ",
			map[string]string{"a": "1.2.3.4:1961", "b": "5.6.7.8:1961"},
		},
		{"malformed entry skipped", "a=1.2.3.4:1961,garbage,b=5.6.7.8:1961",
			map[string]string{"a": "1.2.3.4:1961", "b": "5.6.7.8:1961"}},
		{"empty entries skipped", "a=1.2.3.4:1961,,", map[string]string{"a": "1.2.3.4:1961"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseGlagolHosts(c.spec)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ParseGlagolHosts(%q) = %v, want %v", c.spec, got, c.want)
			}
		})
	}
}

func TestEnableGlagolThenHostFor(t *testing.T) {
	c := NewClient(&Session{})
	if _, ok := c.glagolHostFor("dev1"); ok {
		t.Fatal("expected no host registered yet")
	}
	c.EnableGlagol("dev1", "10.0.0.5:1961")
	got, ok := c.glagolHostFor("dev1")
	if !ok || got != "10.0.0.5:1961" {
		t.Fatalf("got %q, %v", got, ok)
	}
}
