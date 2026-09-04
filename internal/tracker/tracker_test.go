package tracker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/case-forge/cache-ttl-tracker/internal/state"
)

func writeState(t *testing.T, dir, session string, cs state.CacheState) string {
	t.Helper()
	path := filepath.Join(dir, session+".json")
	data, err := json.Marshal(cs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readState(t *testing.T, path string) state.CacheState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cs state.CacheState
	if err := json.Unmarshal(data, &cs); err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestPreCompactResetsAStaleTimestamp(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().UTC().Add(-58 * time.Minute)
	statePath := writeState(t, dir, "sess1", state.CacheState{LastMessageUTC: state.FormatTimestamp(old), PostRedReminder: false})

	Run(dir, filepath.Join(dir, "overage.on"), time.Now().UTC(), "sess1", "PreCompact")

	written := readState(t, statePath)
	newTS, err := state.ParseTimestamp(written.LastMessageUTC)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(newTS) > 5*time.Second {
		t.Errorf("timestamp not reset: %v", newTS)
	}
}

func TestPreCompactClearsAnExistingReminderLatch(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().UTC().Add(-58 * time.Minute)
	statePath := writeState(t, dir, "sess1", state.CacheState{LastMessageUTC: state.FormatTimestamp(old), PostRedReminder: true})

	Run(dir, filepath.Join(dir, "overage.on"), time.Now().UTC(), "sess1", "PreCompact")

	if readState(t, statePath).PostRedReminder {
		t.Error("PreCompact must unconditionally clear the reminder latch")
	}
}

func TestPreCompactWithNoPriorStateStillWritesFresh(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "sess1.json")

	Run(dir, filepath.Join(dir, "overage.on"), time.Now().UTC(), "sess1", "PreCompact")

	written := readState(t, statePath)
	newTS, err := state.ParseTimestamp(written.LastMessageUTC)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(newTS) > 5*time.Second {
		t.Errorf("timestamp not fresh: %v", newTS)
	}
	if written.PostRedReminder {
		t.Error("expected PostRedReminder false with no prior state")
	}
}

func TestAStopRightAfterPrecompactSeesAFreshGapNotAStaleOne(t *testing.T) {
	// The bug this fixes: without the PreCompact branch, the Stop that ends
	// the first post-compaction turn would read the pre-compaction
	// timestamp and, if that older gap was in the red zone, latch a
	// spurious reminder.
	dir := t.TempDir()
	old := time.Now().UTC().Add(-59 * time.Minute)
	statePath := writeState(t, dir, "sess1", state.CacheState{LastMessageUTC: state.FormatTimestamp(old), PostRedReminder: false})
	overageFlag := filepath.Join(dir, "overage.on")

	Run(dir, overageFlag, time.Now().UTC(), "sess1", "PreCompact")
	Run(dir, overageFlag, time.Now().UTC(), "sess1", "Stop")

	if readState(t, statePath).PostRedReminder {
		t.Error("Stop right after PreCompact must not see a stale red-zone gap")
	}
}

func TestPostToolUseAndStopBehaviourIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "sess1.json")
	overageFlag := filepath.Join(dir, "overage.on")

	Run(dir, overageFlag, time.Now().UTC(), "sess1", "PostToolUse")
	if readState(t, statePath).PostRedReminder {
		t.Error("fresh PostToolUse must not set the reminder")
	}

	Run(dir, overageFlag, time.Now().UTC(), "sess1", "Stop")
	if readState(t, statePath).PostRedReminder {
		t.Error("fresh Stop must not set the reminder")
	}
}

func TestAStopPastTheRedZoneSetsTheReminderLatch(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().UTC().Add(-56 * time.Minute)
	statePath := writeState(t, dir, "sess1", state.CacheState{LastMessageUTC: state.FormatTimestamp(old), PostRedReminder: false})
	overageFlag := filepath.Join(dir, "overage.on")

	Run(dir, overageFlag, time.Now().UTC(), "sess1", "Stop")

	if !readState(t, statePath).PostRedReminder {
		t.Error("a 56-minute gap against a 1h TTL is in the red zone and must latch")
	}
}

func TestTheReminderLatchClearsItselfOneMessageLater(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().UTC().Add(-56 * time.Minute)
	statePath := writeState(t, dir, "sess1", state.CacheState{LastMessageUTC: state.FormatTimestamp(old), PostRedReminder: false})
	overageFlag := filepath.Join(dir, "overage.on")

	Run(dir, overageFlag, time.Now().UTC(), "sess1", "Stop")
	if !readState(t, statePath).PostRedReminder {
		t.Fatal("expected the latch to be set on the first Stop")
	}

	Run(dir, overageFlag, time.Now().UTC(), "sess1", "Stop")
	if readState(t, statePath).PostRedReminder {
		t.Error("the latch must clear on the second Stop, not persist")
	}
}

func TestPostToolUseFiringMidTurnLeavesTheLatchAlone(t *testing.T) {
	// PostToolUse must refresh the timestamp but never itself evaluate or
	// clear the red-zone latch: only Stop may do that.
	dir := t.TempDir()
	statePath := writeState(t, dir, "sess1", state.CacheState{LastMessageUTC: state.FormatTimestamp(time.Now().UTC()), PostRedReminder: true})
	overageFlag := filepath.Join(dir, "overage.on")

	Run(dir, overageFlag, time.Now().UTC(), "sess1", "PostToolUse")

	if !readState(t, statePath).PostRedReminder {
		t.Error("PostToolUse must leave an existing latch untouched")
	}
}

func TestOverageFlagShortensTheEffectiveTTL(t *testing.T) {
	dir := t.TempDir()
	overageFlag := filepath.Join(dir, "overage.on")
	if err := os.WriteFile(overageFlag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// 4 minutes against a 5-minute TTL is past the 55/60 red-zone fraction.
	old := time.Now().UTC().Add(-4*time.Minute - 35*time.Second)
	statePath := writeState(t, dir, "sess1", state.CacheState{LastMessageUTC: state.FormatTimestamp(old), PostRedReminder: false})

	Run(dir, overageFlag, time.Now().UTC(), "sess1", "Stop")

	if !readState(t, statePath).PostRedReminder {
		t.Error("under the 5m overage TTL, a 4m35s gap is in the red zone")
	}
}

func TestASessionIDWithPathCharactersCannotEscapeTheStateDir(t *testing.T) {
	// ParsePayload does this sanitising for the real hook entrypoint (see
	// state.SanitizeSessionID); Run itself trusts its sessionID argument,
	// so this proves the write lands inside dir rather than escaping it
	// when a caller passes an already-sanitised id containing no path
	// separators.
	dir := t.TempDir()
	overageFlag := filepath.Join(dir, "overage.on")
	sessionID := state.SanitizeSessionID("../etc/passwd")

	Run(dir, overageFlag, time.Now().UTC(), sessionID, "Stop")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != sessionID+".json" {
		t.Errorf("expected exactly one file %q, got %v", sessionID+".json", entries)
	}
}
