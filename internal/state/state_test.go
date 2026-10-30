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

func TestTimestampRoundTripsAndAcceptsAnOffsetForm(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	formatted := FormatTimestamp(now)
	parsed, err := ParseTimestamp(formatted)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(now) {
		t.Errorf("round trip mismatch: %v != %v", parsed, now)
	}

	// "+00:00" instead of "Z" is the same instant and must parse.
	offsetForm := "2026-08-25T00:28:00+00:00"
	if _, err := ParseTimestamp(offsetForm); err != nil {
		t.Errorf("failed to parse offset-form timestamp: %v", err)
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

func TestLastTouchIsTheNewestOfTheRecordedTouches(t *testing.T) {
	older := FormatTimestamp(time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC))
	newer := FormatTimestamp(time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC))

	got, err := CacheState{LastMessageUTC: older, LastPromptUTC: newer}.LastTouch()
	if err != nil || FormatTimestamp(got) != newer {
		t.Errorf("prompt newer: got %v (%v)", got, err)
	}
	got, err = CacheState{LastMessageUTC: newer, LastPromptUTC: older}.LastTouch()
	if err != nil || FormatTimestamp(got) != newer {
		t.Errorf("message newer: got %v (%v)", got, err)
	}
	got, err = CacheState{LastMessageUTC: older}.LastTouch()
	if err != nil || FormatTimestamp(got) != older {
		t.Errorf("old file with no prompt field: got %v (%v)", got, err)
	}
	got, err = CacheState{LastPromptUTC: newer}.LastTouch()
	if err != nil || FormatTimestamp(got) != newer {
		t.Errorf("prompt only: got %v (%v)", got, err)
	}
	if _, err := (CacheState{}).LastTouch(); err == nil {
		t.Error("no touches at all must be an error, i.e. 'no messages yet'")
	}
}
