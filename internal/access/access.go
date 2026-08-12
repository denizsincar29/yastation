// Package access is a small allowlist of Yandex accounts (by uid, see
// quasar.WhoAmI) permitted to use yastation-server's bring-your-own-token
// mode. It replaces the old model of one shared YASTATION_HTTP_TOKEN
// bearer secret for everyone with per-person, revocable entries: instead
// of "knows the secret", access means "this specific Yandex account is on
// the list" — add/remove one person without touching anyone else's
// access, and the list is just a JSON file naming real accounts, not an
// opaque token you have to remember to rotate.
//
// The file only ever stores identity (uid + a human-readable label,
// see quasar.Identity) — never anyone's actual OAuth token. Whoever's
// allowed still brings their own live X-Yandex-Token on every request;
// this package only answers "is this uid allowed at all".
package access

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry is one allowed Yandex account.
type Entry struct {
	// Name is a human label, usually Identity.RealName or a custom one
	// given at add time — purely for people reading/editing the file,
	// never used for the access decision itself.
	Name string `json:"name"`
	// UID is Yandex's stable account id (quasar.Identity.UID) — the
	// actual key access is checked against.
	UID   string `json:"uid"`
	Login string `json:"login,omitempty"`
	// AddedAt is informational only.
	AddedAt time.Time `json:"added_at"`
}

// List is the parsed contents of access.json.
type List struct {
	Entries []Entry `json:"entries"`
}

// FilePath resolves where the allowlist lives: $YASTATION_ACCESS_FILE if
// set, otherwise <user config dir>/yastation/access.json — same
// convention as quasar.TokenFilePath and app.ConfigFilePath.
func FilePath() string {
	if p := os.Getenv("YASTATION_ACCESS_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "yastation", "access.json")
}

// Load reads and parses path. A missing file is not an error — it's
// treated as an empty list (deny-by-default: nobody's allowed until
// cmd/yastation-access adds someone), so a fresh install doesn't
// accidentally leave BYOT open to anyone with a valid Yandex token.
func Load(path string) (*List, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &List{}, nil
		}
		return nil, err
	}
	var l List
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("%s повреждён: %w", path, err)
	}
	return &l, nil
}

// Save writes l to path as pretty-printed JSON, creating the parent
// directory if needed. Mode 0600: the file names real people even though
// it holds no secrets, no reason to make it world-readable.
func Save(path string, l *List) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

// IsAllowed reports whether uid is on the list. Empty uid is never
// allowed, even against an (illegal) empty-uid entry.
func (l *List) IsAllowed(uid string) bool {
	if uid == "" || l == nil {
		return false
	}
	for _, e := range l.Entries {
		if e.UID == uid {
			return true
		}
	}
	return false
}

// Find returns the entry for uid, if any.
func (l *List) Find(uid string) (Entry, bool) {
	if l == nil {
		return Entry{}, false
	}
	for _, e := range l.Entries {
		if e.UID == uid {
			return e, true
		}
	}
	return Entry{}, false
}

// Add appends e, replacing any existing entry with the same UID (so
// re-adding someone updates their name/login instead of duplicating
// them). Returns false if e.UID was empty (refused, not appended).
func (l *List) Add(e Entry) bool {
	if e.UID == "" {
		return false
	}
	if e.AddedAt.IsZero() {
		e.AddedAt = time.Now()
	}
	for i, existing := range l.Entries {
		if existing.UID == e.UID {
			l.Entries[i] = e
			return true
		}
	}
	l.Entries = append(l.Entries, e)
	return true
}

// Remove deletes the entry with the given uid. Returns false if no such
// entry existed.
func (l *List) Remove(uid string) bool {
	for i, e := range l.Entries {
		if e.UID == uid {
			l.Entries = append(l.Entries[:i], l.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// FindByQuery looks up an entry the fuzzy way, for the CLI: exact UID
// match first, then exact login match, then a case-insensitive substring
// match against Name. Returns ok=false if nothing or more than one entry
// matched (ambiguous query — caller should ask the user to be specific).
func (l *List) FindByQuery(query string) (Entry, bool) {
	if l == nil || query == "" {
		return Entry{}, false
	}
	for _, e := range l.Entries {
		if e.UID == query {
			return e, true
		}
	}
	for _, e := range l.Entries {
		if e.Login == query {
			return e, true
		}
	}
	q := strings.ToLower(query)
	var matches []Entry
	for _, e := range l.Entries {
		if strings.Contains(strings.ToLower(e.Name), q) {
			matches = append(matches, e)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return Entry{}, false
}
