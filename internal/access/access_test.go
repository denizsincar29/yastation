package access

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsEmptyList(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 0 {
		t.Fatalf("expected empty list, got %v", l.Entries)
	}
	if l.IsAllowed("anything") {
		t.Fatal("empty list should deny everything")
	}
}

func TestLoadCorruptFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	if err := writeFile(path, "not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for corrupt JSON")
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "access.json")
	l := &List{}
	l.Add(Entry{Name: "Deniz", UID: "1", Login: "deniz.s"})
	l.Add(Entry{Name: "Mama", UID: "2", Login: "mama"})

	if err := Save(path, l); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries=%v", got.Entries)
	}
	if !got.IsAllowed("1") || !got.IsAllowed("2") {
		t.Fatalf("entries=%v", got.Entries)
	}
	if got.IsAllowed("3") {
		t.Fatal("uid 3 was never added")
	}
}

func TestAddReplacesExistingUID(t *testing.T) {
	l := &List{}
	l.Add(Entry{Name: "Old Name", UID: "1"})
	l.Add(Entry{Name: "New Name", UID: "1"})
	if len(l.Entries) != 1 {
		t.Fatalf("expected 1 entry after re-adding same uid, got %d", len(l.Entries))
	}
	if l.Entries[0].Name != "New Name" {
		t.Fatalf("expected re-add to update name, got %q", l.Entries[0].Name)
	}
}

func TestAddRefusesEmptyUID(t *testing.T) {
	l := &List{}
	if l.Add(Entry{Name: "No UID"}) {
		t.Fatal("expected Add to refuse an entry with empty UID")
	}
	if len(l.Entries) != 0 {
		t.Fatalf("entries=%v", l.Entries)
	}
}

func TestRemove(t *testing.T) {
	l := &List{}
	l.Add(Entry{Name: "Deniz", UID: "1"})
	l.Add(Entry{Name: "Mama", UID: "2"})

	if !l.Remove("1") {
		t.Fatal("expected Remove to report success")
	}
	if l.IsAllowed("1") {
		t.Fatal("uid 1 should no longer be allowed")
	}
	if !l.IsAllowed("2") {
		t.Fatal("uid 2 should still be allowed")
	}
	if l.Remove("1") {
		t.Fatal("removing an already-removed uid should report false")
	}
}

func TestFindByQuery(t *testing.T) {
	l := &List{}
	l.Add(Entry{Name: "Мама Валя", UID: "1", Login: "valya"})
	l.Add(Entry{Name: "Папа Женя", UID: "2", Login: "zhenya"})

	if e, ok := l.FindByQuery("1"); !ok || e.Name != "Мама Валя" {
		t.Fatalf("uid lookup failed: %+v %v", e, ok)
	}
	if e, ok := l.FindByQuery("valya"); !ok || e.UID != "1" {
		t.Fatalf("login lookup failed: %+v %v", e, ok)
	}
	if e, ok := l.FindByQuery("Валя"); !ok || e.UID != "1" {
		t.Fatalf("name substring lookup failed: %+v %v", e, ok)
	}
	if _, ok := l.FindByQuery("nope"); ok {
		t.Fatal("expected no match")
	}
}

func TestFindByQueryAmbiguousReturnsNotOK(t *testing.T) {
	l := &List{}
	l.Add(Entry{Name: "Дениз один", UID: "1"})
	l.Add(Entry{Name: "Дениз два", UID: "2"})
	if _, ok := l.FindByQuery("Дениз"); ok {
		t.Fatal("expected ambiguous name substring match to fail")
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
