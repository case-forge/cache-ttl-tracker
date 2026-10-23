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

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		have string
		want bool
	}{
		{"2.1.299", true},
		{"2.1.300", true},
		{"2.2.0", true},
		{"3.0.0", true},
		{"v2.1.299", true},
		{"2.1.299-beta.1", true},
		{"2.1.298", false},
		{"2.0.999", false},
		{"2.1", false},
		{"", false},
		{"nonsense", false},
	}
	for _, c := range cases {
		if got := versionAtLeast(c.have, IdleCompactMinVersion); got != c.want {
			t.Errorf("versionAtLeast(%q) = %v, want %v", c.have, got, c.want)
		}
	}
}

func writeSettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIdleCompactionIsOnWhenSettingsSayNothing(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	ic := DetectIdleCompaction("2.1.299", t.TempDir(), "")
	if !ic.Available || !ic.Enabled || ic.MinTokens != 200_000 {
		t.Errorf("got %+v", ic)
	}
}

func TestAnOlderClaudeCodeHasNoIdleCompaction(t *testing.T) {
	if DetectIdleCompaction("2.1.298", t.TempDir(), "").Available {
		t.Error("2.1.298 predates the feature")
	}
	if DetectIdleCompaction("", t.TempDir(), "").Available {
		t.Error("an unknown version must count as not there")
	}
}

func TestSettingsCanTurnIdleCompactionOff(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, filepath.Join(home, ".claude", "settings.json"), `{"idleCompaction": false}`)
	if DetectIdleCompaction("2.1.299", home, "").Enabled {
		t.Error("idleCompaction:false must read as off")
	}
}

func TestAMoreSpecificSettingsFileOverridesTheUserOne(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	writeSettings(t, filepath.Join(home, ".claude", "settings.json"), `{"idleCompaction": false}`)
	writeSettings(t, filepath.Join(project, ".claude", "settings.local.json"), `{"idleCompaction": true}`)
	if !DetectIdleCompaction("2.1.299", home, project).Enabled {
		t.Error("project-local settings outrank user settings")
	}
	writeSettings(t, filepath.Join(project, ".claude", "settings.json"), `{"idleCompaction": false}`)
	if !DetectIdleCompaction("2.1.299", home, project).Enabled {
		t.Error("local still outranks the shared project file")
	}
}

func TestMalformedSettingsAreSkippedNotFatal(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, filepath.Join(home, ".claude", "settings.json"), `{not json`)
	ic := DetectIdleCompaction("2.1.299", home, "")
	if !ic.Enabled {
		t.Error("an unreadable file says nothing, so the default (on) stands")
	}
}

func TestTheFloorCanBeMovedByEnvOrByTheSettingsEnvBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv(idleCompactMinTokensEnv, "")
	writeSettings(t, filepath.Join(home, ".claude", "settings.json"),
		`{"env": {"CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS": "100000"}}`)
	if got := DetectIdleCompaction("2.1.299", home, "").MinTokens; got != 100_000 {
		t.Errorf("settings env: got %d", got)
	}
	writeSettings(t, filepath.Join(home, ".claude", "settings.json"),
		`{"env": {"CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS": 120000, "ANTHROPIC_AUTH_TOKEN": "secret"}}`)
	if got := DetectIdleCompaction("2.1.299", home, "").MinTokens; got != 120_000 {
		t.Errorf("a bare number works too, and other env keys are irrelevant: got %d", got)
	}
	t.Setenv(idleCompactMinTokensEnv, "350000")
	if got := DetectIdleCompaction("2.1.299", home, "").MinTokens; got != 350_000 {
		t.Errorf("real env wins: got %d", got)
	}
	t.Setenv(idleCompactMinTokensEnv, "junk")
	if got := DetectIdleCompaction("2.1.299", home, "").MinTokens; got != 120_000 {
		t.Errorf("junk env ignored: got %d", got)
	}
}

func TestArmedNeedsTheFloorTheOneHourTTLAndARealWindow(t *testing.T) {
	ic := IdleCompaction{Available: true, Enabled: true, MinTokens: 200_000}
	n := func(v int) *int { return &v }
	cases := []struct {
		name string
		ctx  *int
		ttl  int
		want bool
	}{
		{"at the floor", n(200_000), 3600, true},
		{"over the floor", n(450_000), 3600, true},
		{"under the floor", n(199_999), 3600, false},
		{"five-minute TTL", n(450_000), 300, false},
		{"just compacted", n(PostCompact), 3600, false},
		{"unknown window", nil, 3600, false},
	}
	for _, c := range cases {
		if got := ic.Armed(c.ctx, c.ttl); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if (IdleCompaction{Available: true, Enabled: false, MinTokens: 1}).Armed(n(450_000), 3600) {
		t.Error("off means not armed")
	}
	if (IdleCompaction{Available: false, Enabled: true, MinTokens: 1}).Armed(n(450_000), 3600) {
		t.Error("absent means not armed")
	}
}

// renderIdle renders a refresh with the harness version in the payload and
// the clock set in seconds. read is the cache_read figure, i.e. the window
// size the transcript reports.
func renderIdle(t *testing.T, home, transcript, version string, elapsedSec int) string {
	t.Helper()
	return renderIdleCache(t, home, transcript, version, elapsedSec, nil)
}

// renderIdleCache is renderIdle with a prompt_cache object in the payload.
func renderIdleCache(t *testing.T, home, transcript, version string, elapsedSec int, cache any) string {
	t.Helper()
	stateDir := state.Dir(home)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	last := time.Now().UTC().Add(-time.Duration(elapsedSec) * time.Second)
	data, _ := json.Marshal(state.CacheState{LastMessageUTC: state.FormatTimestamp(last)})
	if err := os.WriteFile(filepath.Join(stateDir, "idle.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"session_id":      "idle",
		"transcript_path": transcript,
		"model":           map[string]any{"id": "claude-opus-5"},
		"version":         version,
	}
	if cache != nil {
		body["prompt_cache"] = cache
	}
	payload, _ := json.Marshal(body)
	var out bytes.Buffer
	Run(bytes.NewReader(payload), &out, home)
	return plain(out.String())
}

func bigWindow(t *testing.T, dir string, read int) string {
	t.Helper()
	return writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, read, "", false))
}

func TestAnArmedClockCountsToFiftyFourMinutesNotSixty(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	out := renderIdle(t, home, bigWindow(t, home, 250_000), "2.1.299", 20*60)
	if !strings.Contains(out, "cache: warm") {
		t.Errorf("early on it is just warm: %q", out)
	}
	if !strings.Contains(out, "compacts") || !strings.Contains(out, "idle compaction") {
		t.Errorf("the deadline is a compaction, not an expiry: %q", out)
	}
	if strings.Contains(out, "cold by") {
		t.Errorf("an armed clock must not forecast a cold cache: %q", out)
	}
}

func TestAnArmedClockTurnsYellowAndRedSoonerThanThePlainOne(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	path := bigWindow(t, home, 250_000)

	// 28m is still green on a 60m clock (frac 0.47) but past the 50% mark of 54m.
	if out := renderIdle(t, home, path, "2.1.299", 28*60); !strings.Contains(out, "cooling") {
		t.Errorf("28m armed should already be cooling: %q", out)
	}
	if out := renderIdle(t, home, path, "", 28*60); !strings.Contains(out, "cache: warm") {
		t.Errorf("28m unarmed is still warm: %q", out)
	}
}

func TestTheLastFiveMinutesSayCompactingInRatherThanExpiring(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	path := bigWindow(t, home, 250_000)

	out := renderIdle(t, home, path, "2.1.299", 49*60+35)
	if !strings.Contains(out, "compacting in 5m") {
		t.Errorf("want compacting in 5m: %q", out)
	}
	if strings.Contains(out, "expiring") {
		t.Errorf("must not say expiring: %q", out)
	}
	if out := renderIdle(t, home, path, "2.1.299", 52*60+10); !strings.Contains(out, "compacting in 2m") {
		t.Errorf("want compacting in 2m: %q", out)
	}
	if out := renderIdle(t, home, path, "2.1.299", 53*60+50); !strings.Contains(out, "compacting in 1m") {
		t.Errorf("want compacting in 1m: %q", out)
	}
}

func TestRedWordingNeverAdvisesToCompact(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	out := renderIdle(t, home, bigWindow(t, home, 250_000), "2.1.299", 51*60)
	if strings.Contains(strings.ToLower(out), "compact now") || strings.Contains(strings.ToLower(out), "compact first") {
		t.Errorf("a state report, not an instruction: %q", out)
	}
}

func TestPastFiftyFourMinutesWithNoCompactionTheOrdinaryClockTakesOver(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	path := bigWindow(t, home, 250_000)
	out := renderIdle(t, home, path, "2.1.299", 56*60)
	if !strings.Contains(out, "expiring") || !strings.Contains(out, "cold by") {
		t.Errorf("a compaction that never came must fall back to the plain clock: %q", out)
	}
	out = renderIdle(t, home, path, "2.1.299", 65*60)
	if !strings.Contains(out, "cache: cold") {
		t.Errorf("past the hour it is cold: %q", out)
	}
}

func TestUnderTheFloorTheCacheStillGoesColdAtTheTTL(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	path := bigWindow(t, home, 150_000)
	out := renderIdle(t, home, path, "2.1.299", 56*60)
	if strings.Contains(out, "compact") || !strings.Contains(out, "expiring") || !strings.Contains(out, "cold by") {
		t.Errorf("under 200k nothing changes: %q", out)
	}
}

func TestTurningIdleCompactionOffRestoresTheOrdinaryClock(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	writeSettings(t, filepath.Join(home, ".claude", "settings.json"), `{"idleCompaction": false}`)
	out := renderIdle(t, home, bigWindow(t, home, 250_000), "2.1.299", 56*60)
	if strings.Contains(out, "compact") || !strings.Contains(out, "expiring") {
		t.Errorf("off means the old behaviour: %q", out)
	}
}

func TestAnOlderClaudeCodeKeepsTheOrdinaryClock(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	out := renderIdle(t, home, bigWindow(t, home, 250_000), "2.1.298", 56*60)
	if strings.Contains(out, "compact") || !strings.Contains(out, "expiring") {
		t.Errorf("no feature, no notice: %q", out)
	}
}

func TestTheVersionFallsBackToTheTranscriptWhenThePayloadHasNone(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	rec := map[string]any{
		"type": "assistant", "version": "2.1.301",
		"message": map[string]any{"model": "claude-opus-5", "usage": map[string]any{
			"input_tokens": 3, "cache_read_input_tokens": 250000, "cache_creation_input_tokens": 1000,
			"cache_creation": map[string]any{"ephemeral_1h_input_tokens": 1000}}},
	}
	data, _ := json.Marshal(rec)
	path := writeLines(t, home, "v.jsonl", string(data))
	out := renderIdle(t, home, path, "", 52*60)
	if !strings.Contains(out, "compacting in") {
		t.Errorf("the transcript's own version stamp should arm it: %q", out)
	}
}

func TestAMeasuredFiveMinuteTTLNeverArms(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	path := writeLines(t, home, "t.jsonl", turnRecord("claude-opus-5", 0, 1000, 250_000, "", false))
	out := renderIdleCache(t, home, path, "2.1.299", 3*60, map[string]any{"ttl": "5m"})
	if strings.Contains(out, "compact") {
		t.Errorf("a 5m cache is gone long before 54 minutes: %q", out)
	}
}

func TestACompactionTheHooksMissedStillShowsItsTime(t *testing.T) {
	t.Setenv(idleCompactMinTokensEnv, "")
	home := t.TempDir()
	stamp := time.Now().UTC().Add(-90 * time.Second).Format(time.RFC3339Nano)
	compact, _ := json.Marshal(map[string]any{"type": "user", "isCompactSummary": true, "timestamp": stamp,
		"message": map[string]any{"content": "s"}})
	path := writeLines(t, home, "c.jsonl",
		turnRecord("claude-opus-5", 1000, 0, 250_000, "2026-07-26T16:00:00.000Z", false), string(compact))

	out := renderIdle(t, home, path, "2.1.299", 54*60+30)
	compacted, _ := time.Parse(time.RFC3339Nano, stamp)
	want := compacted.In(time.Local).Format("15:04")
	if !strings.Contains(out, "cache: compacted at "+want) {
		t.Errorf("expected the compaction time %s, no clock: %q", want, out)
	}
	if !strings.Contains(out, "context: just compacted") {
		t.Errorf("expected the compacted context note: %q", out)
	}
}

func TestAPromptNewerThanTheLastTouchStartsTheClock(t *testing.T) {
	home := t.TempDir()
	stateDir := state.Dir(home)
	_ = os.MkdirAll(stateDir, 0o755)
	old := time.Now().UTC().Add(-40 * time.Minute)
	prompt := time.Now().UTC().Add(-30 * time.Second)
	data, _ := json.Marshal(state.CacheState{
		LastMessageUTC: state.FormatTimestamp(old), LastPromptUTC: state.FormatTimestamp(prompt)})
	_ = os.WriteFile(filepath.Join(stateDir, "p.json"), data, 0o644)
	payload, _ := json.Marshal(map[string]any{"session_id": "p"})
	var out bytes.Buffer
	Run(bytes.NewReader(payload), &out, home)
	if got := plain(out.String()); !strings.Contains(got, "cache: warm 00:") {
		t.Errorf("a prompt 30s ago means a 30s-old cache, not 40 minutes: %q", got)
	}
}
