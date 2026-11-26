package statusline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/case-forge/cache-ttl-tracker/internal/state"
)

const pauseModel = "claude-opus-5"

func stampOf(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// refusalRecord is the placeholder Claude Code writes when a request is
// refused by a usage limit. resets is zero for a record that names no reset.
func refusalRecord(at time.Time, kind string, resets time.Time) string {
	info := map[string]any{"status": "rejected", "rateLimitType": kind}
	if !resets.IsZero() {
		info["resetsAt"] = resets.Unix()
	}
	rec := map[string]any{
		"type":              "assistant",
		"timestamp":         stampOf(at),
		"isApiErrorMessage": true,
		"error":             "rate_limit",
		"apiError":          "usage_limit_reached",
		"apiErrorParams":    map[string]any{"rate_limit_info": info},
		"message": map[string]any{
			"role":  "assistant",
			"model": "<synthetic>",
			"usage": map[string]any{
				"input_tokens": 0, "output_tokens": 0,
				"cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
			},
		},
	}
	data, _ := json.Marshal(rec)
	return string(data)
}

// renderPause runs the status line with the hooks' last touch at touch.
func renderPause(t *testing.T, transcript string, touch time.Time, version string) string {
	t.Helper()
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	stateDir := state.Dir(home)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cs := state.CacheState{LastMessageUTC: state.FormatTimestamp(touch)}
	data, _ := json.Marshal(cs)
	if err := os.WriteFile(filepath.Join(stateDir, "pausetest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"session_id":      "pausetest",
		"transcript_path": transcript,
		"model":           map[string]any{"id": pauseModel},
		"prompt_cache":    map[string]any{"ttl": "1h"},
		"version":         version,
	}
	data, _ = json.Marshal(payload)
	var out bytes.Buffer
	Run(bytes.NewReader(data), &out, home)
	return plain(out.String())
}

// --- the scan ---------------------------------------------------------------

func TestARefusalNewerThanEveryResponseIsAPause(t *testing.T) {
	now := time.Now().UTC()
	served := now.Add(-70 * time.Minute)
	first := now.Add(-65 * time.Minute)
	second := now.Add(-2 * time.Minute)
	resets := now.Add(30 * time.Minute).Truncate(time.Second)
	path := writeLines(t, t.TempDir(), "t.jsonl",
		turnRecord(pauseModel, 0, 0, 250_000, stampOf(served), false),
		refusalRecord(first, "five_hour", resets),
		refusalRecord(second, "five_hour", resets))

	scan := ScanTranscript(path, pauseModel)
	p := scan.Limit
	if p == nil {
		t.Fatal("no pause found")
	}
	if !p.RefusedAt.Equal(second) || !p.FirstRefusedAt.Equal(first) {
		t.Errorf("refusals: newest %v oldest %v", p.RefusedAt, p.FirstRefusedAt)
	}
	if p.ServedBefore == nil || !p.ServedBefore.Equal(served) {
		t.Errorf("ServedBefore = %v, want %v", p.ServedBefore, served)
	}
	if p.ResetsAt == nil || !p.ResetsAt.Equal(resets) || p.Kind != "five_hour" {
		t.Errorf("resets %v kind %q", p.ResetsAt, p.Kind)
	}
	// The placeholder is not a model and has no window.
	if scan.NewestModel != pauseModel {
		t.Errorf("NewestModel = %q", scan.NewestModel)
	}
	if scan.ContextTokens == nil || *scan.ContextTokens != 250_003 {
		t.Errorf("ContextTokens = %v", scan.ContextTokens)
	}
}

func TestAServedResponseAfterARefusalEndsThePause(t *testing.T) {
	now := time.Now().UTC()
	path := writeLines(t, t.TempDir(), "t.jsonl",
		refusalRecord(now.Add(-90*time.Minute), "five_hour", now.Add(-30*time.Minute)),
		turnRecord(pauseModel, 0, 0, 250_000, stampOf(now.Add(-10*time.Minute)), false))
	if p := ScanTranscript(path, pauseModel).Limit; p != nil {
		t.Errorf("a pause was reported after a served response: %+v", p)
	}
}

func TestAnotherKindOfFailedRequestIsNotAPause(t *testing.T) {
	now := time.Now().UTC()
	other := strings.Replace(refusalRecord(now, "five_hour", now), "usage_limit_reached", "overloaded", 1)
	path := writeLines(t, t.TempDir(), "t.jsonl",
		turnRecord(pauseModel, 0, 0, 250_000, stampOf(now.Add(-time.Minute)), false), other)
	scan := ScanTranscript(path, pauseModel)
	if scan.Limit != nil {
		t.Error("an overloaded error was read as a usage limit")
	}
	if scan.NewestModel != pauseModel {
		t.Errorf("NewestModel = %q, the placeholder leaked through", scan.NewestModel)
	}
}

// --- when the pause governs the clock ---------------------------------------

func TestActiveBoundaries(t *testing.T) {
	refused := time.Date(2026, 11, 22, 23, 43, 29, 0, time.UTC)
	resets := refused.Add(17 * time.Minute)
	withReset := &LimitPause{RefusedAt: refused, ResetsAt: &resets}
	noReset := &LimitPause{RefusedAt: refused}
	cases := []struct {
		name   string
		p      *LimitPause
		touch  time.Time
		now    time.Time
		active bool
	}{
		{"the refused turn's own Stop", withReset, refused.Add(time.Second), resets.Add(time.Hour), true},
		{"touch exactly at the grace", withReset, refused.Add(limitGrace), resets.Add(time.Hour), true},
		{"new prompt before the reset", withReset, refused.Add(5 * time.Minute), resets.Add(-time.Second), true},
		{"new prompt at the reset", withReset, resets, resets, false},
		{"new prompt after the reset", withReset, resets.Add(time.Minute), resets.Add(time.Minute), false},
		{"no reset known, prompt just sent", noReset, refused.Add(time.Minute), refused.Add(time.Minute + limitGrace - time.Second), true},
		{"no reset known, prompt has stood", noReset, refused.Add(time.Minute), refused.Add(time.Minute + limitGrace), false},
	}
	for _, c := range cases {
		if got := c.p.Active(c.touch, c.now) != nil; got != c.active {
			t.Errorf("%s: active = %v, want %v", c.name, got, c.active)
		}
	}
	var none *LimitPause
	if none.Active(refused, refused) != nil {
		t.Error("no pause must stay no pause")
	}
}

func TestLastServedFallsBackToTheFirstRefusal(t *testing.T) {
	first := time.Date(2026, 11, 22, 22, 48, 18, 0, time.UTC)
	p := &LimitPause{RefusedAt: first.Add(time.Hour), FirstRefusedAt: first}
	if got := p.LastServed(first.Add(time.Hour)); !got.Equal(first) {
		t.Errorf("a touch during the pause: got %v, want the first refusal", got)
	}
	older := first.Add(-20 * time.Minute)
	if got := p.LastServed(older); !got.Equal(older) {
		t.Errorf("an older touch must stand: got %v", got)
	}
}

// --- rendering --------------------------------------------------------------

func TestAMessageSentDuringAPauseDoesNotRestartTheClock(t *testing.T) {
	now := time.Now().UTC()
	resets := now.Add(30 * time.Minute)
	path := writeLines(t, t.TempDir(), "t.jsonl",
		turnRecord(pauseModel, 0, 0, 100_000, stampOf(now.Add(-70*time.Minute)), false),
		refusalRecord(now.Add(-65*time.Minute), "five_hour", resets),
		refusalRecord(now.Add(-59*time.Second), "five_hour", resets))

	// The prompt typed a minute ago, and its Stop a second later.
	out := renderPause(t, path, now.Add(-58*time.Second), "2.1.250")
	if !strings.Contains(out, "cold") || !strings.Contains(out, "1h10m") {
		t.Errorf("the clock should read cold from the last served response: %q", out)
	}
	if strings.Contains(out, "warm") {
		t.Errorf("a refused message warmed the cache: %q", out)
	}
	want := "5-hour limit until " + resets.In(displayTZ()).Format("15:04")
	if !strings.Contains(out, want) {
		t.Errorf("missing %q in %q", want, out)
	}
	if strings.Contains(out, "idle compaction") {
		t.Errorf("nothing was armed, so nothing to say about idle compaction: %q", out)
	}
}

func TestWithoutAPauseTheSameTouchIsWarm(t *testing.T) {
	now := time.Now().UTC()
	path := writeLines(t, t.TempDir(), "t.jsonl",
		turnRecord(pauseModel, 0, 0, 100_000, stampOf(now.Add(-70*time.Minute)), false))
	out := renderPause(t, path, now.Add(-58*time.Second), "2.1.250")
	if !strings.Contains(out, "warm") || strings.Contains(out, "limit") {
		t.Errorf("got %q", out)
	}
}

func TestAWeeklyLimitAndAnUnnamedOneAreNamed(t *testing.T) {
	now := time.Now().UTC()
	dir := t.TempDir()
	served := turnRecord(pauseModel, 0, 0, 100_000, stampOf(now.Add(-20*time.Minute)), false)
	weekly := writeLines(t, dir, "w.jsonl", served, refusalRecord(now.Add(-10*time.Minute), "seven_day", now.Add(48*time.Hour)))
	if out := renderPause(t, weekly, now.Add(-10*time.Minute), "2.1.250"); !strings.Contains(out, "weekly limit until ") {
		t.Errorf("got %q", out)
	}
	bare := writeLines(t, dir, "b.jsonl", served, refusalRecord(now.Add(-10*time.Minute), "", time.Time{}))
	out := renderPause(t, bare, now.Add(-10*time.Minute), "2.1.250")
	if !strings.Contains(out, "/ usage limit") || strings.Contains(out, "until") {
		t.Errorf("got %q", out)
	}
}

func TestIdleCompactionIsOnHoldThenSkippedDuringAPause(t *testing.T) {
	now := time.Now().UTC()
	resets := now.Add(2 * time.Hour)
	for _, c := range []struct {
		idleMin int
		want    string
		not     string
	}{
		{53, "idle compaction on hold", "skipped"},
		{55, "idle compaction skipped", "on hold"},
	} {
		served := now.Add(-time.Duration(c.idleMin) * time.Minute)
		path := writeLines(t, t.TempDir(), "t.jsonl",
			turnRecord(pauseModel, 0, 0, 250_000, stampOf(served), false),
			refusalRecord(served.Add(time.Minute), "five_hour", resets))
		out := renderPause(t, path, served.Add(time.Minute+time.Second), "2.1.299")
		if !strings.Contains(out, c.want) || strings.Contains(out, c.not) {
			t.Errorf("%dm idle: got %q", c.idleMin, out)
		}
		// The compaction forecast would be false: its request is refused too.
		if strings.Contains(out, "compacts ") || strings.Contains(out, "compacting") {
			t.Errorf("%dm idle: a compaction was forecast during a pause: %q", c.idleMin, out)
		}
	}
}

func TestOnceTheLimitLiftsOnlyTheTrueClockRemains(t *testing.T) {
	now := time.Now().UTC()
	served := now.Add(-3 * time.Hour)
	path := writeLines(t, t.TempDir(), "t.jsonl",
		turnRecord(pauseModel, 0, 0, 250_000, stampOf(served), false),
		refusalRecord(now.Add(-2*time.Hour), "five_hour", now.Add(-time.Hour)))

	// Nobody has come back: the note goes, the clock still runs from the
	// last served response, not from the refused turn.
	out := renderPause(t, path, now.Add(-2*time.Hour).Add(time.Second), "2.1.299")
	if strings.Contains(out, "limit") || strings.Contains(out, "idle compaction") {
		t.Errorf("the limit has lifted, the note should be gone: %q", out)
	}
	if !strings.Contains(out, "cold") || !strings.Contains(out, "3h00m") {
		t.Errorf("got %q", out)
	}

	// A prompt sent after the reset is a real request again.
	out = renderPause(t, path, now.Add(-30*time.Second), "2.1.299")
	if !strings.Contains(out, "warm") {
		t.Errorf("a prompt after the reset should restart the clock: %q", out)
	}
}
