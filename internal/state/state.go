// Package state is the per-session cache-touch state shared by the tracker
// hook (writer) and the statusline (reader). Kept separate from both so
// their JSON shape can't drift apart the way two independent readers of the
// same file silently can.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// CacheState is the on-disk shape of a session's state file.
type CacheState struct {
	LastMessageUTC  string `json:"last_message_utc"`
	PostRedReminder bool   `json:"post_red_reminder"`
}

const (
	FiveMinSeconds = 300
	OneHourSeconds = 3600
	// RedZoneFraction is the elapsed/ttl ratio at which a turn is "close
	// enough to expiry" to latch the one-message reminder.
	RedZoneFraction = 55.0 / 60.0
)

var safeID = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// SanitizeSessionID strips anything that isn't a safe filename character,
// falling back to "default" for an empty/fully-stripped id.
func SanitizeSessionID(raw string) string {
	cleaned := safeID.ReplaceAllString(raw, "")
	if cleaned == "" {
		return "default"
	}
	return cleaned
}

// Dir is this build's state directory. Deliberately distinct from any
// sibling build's own directory (e.g. a "-dev" build): two independent
// hooks reading-modifying-writing the same per-session file would race and
// overwrite each other's fields.
func Dir(home string) string {
	return filepath.Join(home, ".claude", "cache-ttl-track-core")
}

// OverageFlagPath is the touch-file that switches the assumed TTL from 1h
// to 5m. Checked fresh on every call (never cached) so flipping it takes
// effect on the very next message, no restart needed.
func OverageFlagPath(home string) string {
	return filepath.Join(home, ".claude", "cache-ttl-overage-monitor.on")
}

func OverageMonitorOn(home string) bool {
	_, err := os.Stat(OverageFlagPath(home))
	return err == nil
}

// Read is best-effort: a missing or malformed file is reported via the
// error, and callers treat that identically to "no prior state".
func Read(path string) (CacheState, error) {
	var cs CacheState
	data, err := os.ReadFile(path)
	if err != nil {
		return cs, err
	}
	if err := json.Unmarshal(data, &cs); err != nil {
		return cs, err
	}
	return cs, nil
}

// Write is best-effort: errors (e.g. an unwritable state dir) are swallowed
// by design: a hook must never fail the turn it's attached to.
func Write(dir, path string, cs CacheState) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(cs)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// FormatTimestamp matches Python's datetime.isoformat(timespec="seconds")
// closely enough to round-trip: RFC3339's "Z07:00" element parses both a
// literal "Z" (what this writes) and Python's "+00:00" (what an old state
// file written by the retired Python build carries), so ParseTimestamp
// reads either without a format migration.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func ParseTimestamp(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}
