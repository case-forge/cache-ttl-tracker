package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSanitizeSessionIDStripsUnsafeCharacters(t *testing.T) {
	cases := map[string]string{
		"abc-123_XYZ":   "abc-123_XYZ",
		"../etc/passwd": "etcpasswd",
		"":              "default",
		"!!!":           "default",
	}
	for in, want := range cases {
		if got := SanitizeSessionID(in); got != want {
			t.Errorf("SanitizeSessionID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTimestampRoundTripsAndAcceptsPythonsOffsetForm(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	formatted := FormatTimestamp(now)
	parsed, err := ParseTimestamp(formatted)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(now) {
		t.Errorf("round trip mismatch: %v != %v", parsed, now)
	}

	// A state file written by the retired Python build uses "+00:00"
	// instead of "Z", and must still parse so an upgrade doesn't orphan an
	// in-flight session's state.
	pythonStyle := "2026-08-25T00:28:00+00:00"
	if _, err := ParseTimestamp(pythonStyle); err != nil {
		t.Errorf("failed to parse Python-style offset timestamp: %v", err)
	}
}

func TestWriteThenReadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "sess1.json")
	cs := CacheState{LastMessageUTC: FormatTimestamp(time.Now().UTC()), PostRedReminder: true}

	Write(filepath.Dir(path), path, cs)

	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != cs {
		t.Errorf("got %+v, want %+v", got, cs)
	}
}

func TestReadOnAMissingFileIsAnError(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("expected an error reading a missing file")
	}
}

func TestOverageMonitorOnReflectsTheFlagFile(t *testing.T) {
	home := t.TempDir()
	if OverageMonitorOn(home) {
		t.Error("expected off by default")
	}
}
