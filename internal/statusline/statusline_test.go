package statusline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/case-forge/cache-ttl-tracker/internal/state"
)

func init() {
	// The opt-in segments are read fresh from the environment on every
	// call (unlike the Python original, which froze them as module-level
	// constants at import time); see FullPriceSegment/LimitCheckSegment.
	// Setting them here just saves every test that wants them on from
	// calling t.Setenv individually.
	os.Setenv("CLAUDE_PLUGIN_OPTION_SHOW_FULL_PRICE", "1")
	os.Setenv("CLAUDE_PLUGIN_OPTION_SHOW_200K_CHECK", "1")
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plain(s string) string { return ansiRe.ReplaceAllString(s, "") }

func usageLine(over map[string]any) string {
	usage := map[string]any{
		"input_tokens":                2,
		"cache_creation_input_tokens": 2217,
		"cache_read_input_tokens":     219830,
		"output_tokens":               1328,
	}
	for k, v := range over {
		usage[k] = v
	}
	rec := map[string]any{
		"type":    "assistant",
		"message": map[string]any{"role": "assistant", "usage": usage},
	}
	data, _ := json.Marshal(rec)
	return string(data)
}

func writeLines(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- what counts as context ---------------------------------------------

func TestContextIsInputPlusBothCacheFigures(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "session.jsonl", usageLine(nil))
	got := ContextTokens(path)
	if got == nil || *got != 222049 {
		t.Errorf("got %v, want 222049", got)
	}
}

func TestOutputTokensAreExcluded(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "big-output.jsonl", usageLine(map[string]any{"output_tokens": 500000}))
	got := ContextTokens(path)
	if got == nil || *got != 222049 {
		t.Errorf("got %v, want 222049 (output_tokens must not count)", got)
	}
}

func TestTheNewestUsageBlockWins(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "two.jsonl",
		usageLine(map[string]any{"cache_read_input_tokens": 1000}),
		usageLine(map[string]any{"cache_read_input_tokens": 400000}))
	got := ContextTokens(path)
	if got == nil || *got != 402219 {
		t.Errorf("got %v, want 402219 (newest block)", got)
	}
}

func TestMissingTranscriptHidesTheCounter(t *testing.T) {
	if got := ContextTokens("/nonexistent/session.jsonl"); got != nil {
		t.Errorf("got %v, want nil", got)
	}
	if seg := ContextSegment(Payload{TranscriptPath: "/nonexistent.jsonl"}, ScanTranscript("/nonexistent.jsonl", "")); seg != "" {
		t.Errorf("got %q, want empty", seg)
	}
}

func TestATranscriptWithNoUsageBlockHidesTheCounter(t *testing.T) {
	dir := t.TempDir()
	rec, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": "hi"}})
	path := writeLines(t, dir, "plain.jsonl", string(rec))
	if got := ContextTokens(path); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestATruncatedLeadingLineIsSurvivable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cut.jsonl")
	content := `{"message": {"usage": {"input_to` + "\n" + usageLine(nil) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ContextTokens(path)
	if got == nil || *got != 222049 {
		t.Errorf("got %v, want 222049 despite a truncated leading line", got)
	}
}

// --- cost, which is the reason this tail-reads ---------------------------

func TestOnlyTheTailIsRead(t *testing.T) {
	dir := t.TempDir()
	hidden := usageLine(map[string]any{"cache_read_input_tokens": 999999})
	var padLines []string
	for i := 0; i < 3000; i++ {
		rec, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": strings.Repeat("x", 500)}})
		padLines = append(padLines, string(rec))
	}
	lines := append([]string{hidden}, padLines...)
	lines = append(lines, usageLine(nil))
	path := writeLines(t, dir, "huge.jsonl", lines...)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= tailBytes {
		t.Fatalf("fixture too small to prove tail-only reading: %d bytes", info.Size())
	}
	got := ContextTokens(path)
	if got == nil || *got != 222049 {
		t.Errorf("got %v, want 222049 (the hidden block must not be found)", got)
	}
}

// --- the ceiling the percentage is against --------------------------------

func TestOpusAndSonnetAreOneMillion(t *testing.T) {
	for _, model := range []string{"claude-opus-5", "claude-sonnet-5", "claude-fable-5"} {
		if got := ContextLimit(Payload{Model: &ModelInfo{ID: model}}); got != 1_000_000 {
			t.Errorf("%s: got %d, want 1_000_000", model, got)
		}
	}
}

func TestHaikuIsTwoHundredThousand(t *testing.T) {
	got := ContextLimit(Payload{Model: &ModelInfo{ID: "claude-haiku-4-5-20251001"}})
	if got != 200_000 {
		t.Errorf("got %d, want 200_000", got)
	}
}

func TestAnUnknownModelFallsBackRatherThanCrashing(t *testing.T) {
	if got := ContextLimit(Payload{Model: &ModelInfo{ID: "something-new"}}); got != DefaultContextLimit {
		t.Errorf("got %d, want default", got)
	}
	if got := ContextLimit(Payload{}); got != DefaultContextLimit {
		t.Errorf("got %d, want default", got)
	}
}

func TestTheLimitCanBeOverridden(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_OPTION_CONTEXT_LIMIT", "500000")
	if got := ContextLimit(Payload{Model: &ModelInfo{ID: "claude-opus-5"}}); got != 500_000 {
		t.Errorf("got %d, want 500000", got)
	}
}

func TestAJunkOverrideIsIgnored(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_OPTION_CONTEXT_LIMIT", "lots")
	if got := ContextLimit(Payload{Model: &ModelInfo{ID: "claude-opus-5"}}); got != 1_000_000 {
		t.Errorf("got %d, want 1_000_000", got)
	}
}

// --- rendering -------------------------------------------------------------

func TestTheSegmentShowsTokensCeilingAndPercentage(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "session.jsonl", usageLine(nil))
	payload := Payload{TranscriptPath: path, Model: &ModelInfo{ID: "claude-opus-5"}}
	seg := plain(ContextSegment(payload, ScanTranscript(path, "")))
	if strings.TrimSpace(seg) != "context 222k/1M 22%" {
		t.Errorf("got %q", seg)
	}
}

func TestMillionsAreShownWithTwoDecimals(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "m.jsonl", usageLine(map[string]any{"cache_read_input_tokens": 1240000}))
	payload := Payload{TranscriptPath: path, Model: &ModelInfo{ID: "claude-opus-5"}}
	seg := plain(ContextSegment(payload, ScanTranscript(path, "")))
	if !strings.Contains(seg, "1.24M/1M") {
		t.Errorf("got %q, want 1.24M/1M", seg)
	}
	if !strings.Contains(seg, "100%") {
		t.Errorf("got %q, want clamped 100%%, not 124%%", seg)
	}
}

func TestColourEscalatesWithFullness(t *testing.T) {
	dir := t.TempDir()
	colourAt := func(tokens int) string {
		path := writeLines(t, dir, strconv.Itoa(tokens)+".jsonl",
			usageLine(map[string]any{"input_tokens": tokens, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}))
		payload := Payload{TranscriptPath: path, Model: &ModelInfo{ID: "claude-opus-5"}}
		return ContextSegment(payload, ScanTranscript(path, ""))
	}
	if !strings.Contains(colourAt(220_000), Green) {
		t.Error("220k should be green")
	}
	if !strings.Contains(colourAt(650_000), Yellow) {
		t.Error("650k should be yellow")
	}
	if !strings.Contains(colourAt(850_000), Red) {
		t.Error("850k should be red")
	}
}

func TestTheCacheClockStillRendersWithoutATranscript(t *testing.T) {
	var out bytes.Buffer
	Run(strings.NewReader("{}"), &out, t.TempDir())
	if !strings.Contains(out.String(), "cache:") {
		t.Errorf("got %q, want a cache: line even with nothing else", out.String())
	}
}

// --- subagents, which have their own window --------------------------------

func TestASubagentTurnIsNotReadAsThisSession(t *testing.T) {
	dir := t.TempDir()
	main := usageLine(map[string]any{"cache_read_input_tokens": 700000})
	sub, _ := json.Marshal(map[string]any{
		"type": "assistant", "isSidechain": true,
		"message": map[string]any{"role": "assistant", "usage": map[string]any{
			"input_tokens": 4000, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 1000,
		}},
	})
	path := writeLines(t, dir, "sidechain.jsonl", main, string(sub))
	got := ContextTokens(path)
	if got == nil || *got != 702219 {
		t.Errorf("got %v, want 702219 (sidechain must be skipped, not treated as newest)", got)
	}
}

// --- compaction --------------------------------------------------------------

func TestACompactionNewerThanTheLastUsageBlockSuppressesTheFigure(t *testing.T) {
	dir := t.TempDir()
	compact, _ := json.Marshal(map[string]any{"type": "user", "isCompactSummary": true, "isSidechain": false,
		"message": map[string]any{"role": "user", "content": "summary"}})
	path := writeLines(t, dir, "compacted.jsonl",
		usageLine(map[string]any{"cache_read_input_tokens": 900000}), string(compact))

	got := ContextTokens(path)
	if got == nil || *got != PostCompact {
		t.Errorf("got %v, want PostCompact", got)
	}
	payload := Payload{TranscriptPath: path, Model: &ModelInfo{ID: "claude-opus-5"}}
	seg := plain(ContextSegment(payload, ScanTranscript(path, "")))
	if strings.TrimSpace(seg) != "context: just compacted" {
		t.Errorf("got %q", seg)
	}
	if !strings.Contains(ContextSegment(payload, ScanTranscript(path, "")), Green) {
		t.Error("post-compaction the window really is small, so green is accurate")
	}
}

func TestTheCacheLabelIsSuppressedWhileJustCompactedShows(t *testing.T) {
	dir := t.TempDir()
	compact, _ := json.Marshal(map[string]any{"type": "user", "isCompactSummary": true, "message": map[string]any{"content": "s"}})
	path := writeLines(t, dir, "compacted.jsonl", usageLine(map[string]any{"cache_read_input_tokens": 900000}), string(compact))
	scan := ScanTranscript(path, "")
	if !LabelSuppressedByCompaction(scan) {
		t.Error("expected the label to be suppressed")
	}
}

func TestARealReadingDoesNotSuppressTheLabel(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "session.jsonl", usageLine(nil))
	if LabelSuppressedByCompaction(ScanTranscript(path, "")) {
		t.Error("a plain reading must not suppress the label")
	}
}

func TestAMissingTranscriptDoesNotSuppressTheLabel(t *testing.T) {
	if LabelSuppressedByCompaction(ScanTranscript("/nope.jsonl", "")) {
		t.Error("a missing transcript must not suppress the label")
	}
}

// --- the label: state only, no advice ---------------------------------------

func TestTheColdLabelSaysTheSavingIsGoneNotThatCompactingIsBanned(t *testing.T) {
	if got := Label(Red, false, true); got != "cold" {
		t.Errorf("got %q, want cold", got)
	}
	if got := Label(Red, true, true); got != "expiring" {
		t.Errorf("got %q, want expiring", got)
	}
}

func TestTheColdLabelNamesTheStateAndNotOnlyTheRemedy(t *testing.T) {
	if got := Label(Red, false, true); got != "cold" {
		t.Errorf("got %q", got)
	}
	if strings.Contains(Label(Green, true, true), "cold") {
		t.Error("green must never say cold")
	}
	if strings.Contains(Label(Yellow, false, true), "cold") {
		t.Error("yellow must never say cold")
	}
	if strings.Contains(Label(Red, true, true), "cold") {
		t.Error("bold red (expiring) must never say cold")
	}
}

func TestColdIsNotAssertedWhenTheTwoTTLsDisagree(t *testing.T) {
	if got := Label(Red, false, false); got != "" {
		t.Errorf("got %q, want empty when coldKnown is false", got)
	}
}

func TestTheLabelReportsCacheStateAndNeverAdvises(t *testing.T) {
	colours := []string{Green, Yellow, Red}
	bolds := []bool{true, false}
	coldKnowns := []bool{true, false}
	imperatives := []string{"compact", "should", "do not", "now"}
	for _, c := range colours {
		for _, b := range bolds {
			for _, ck := range coldKnowns {
				word := strings.ToLower(Label(c, b, ck))
				for _, imp := range imperatives {
					if strings.Contains(word, imp) {
						t.Errorf("label %q contains imperative %q", word, imp)
					}
				}
			}
		}
	}
}

func TestBoldRedIsExpiringRatherThanCold(t *testing.T) {
	if got := Label(Red, true, true); got != "expiring" {
		t.Errorf("got %q, want expiring", got)
	}
	if got := Label(Red, false, true); got != "cold" {
		t.Errorf("got %q, want cold", got)
	}
}

func TestColourDirectionMatchesTheCacheClock(t *testing.T) {
	cacheFresh, _ := Bracket(10, 3600)
	cacheExpiring, _ := Bracket(3500, 3600)
	if cacheFresh != Green || cacheExpiring != Red {
		t.Errorf("cache clock direction wrong: fresh=%q expiring=%q", cacheFresh, cacheExpiring)
	}

	dir := t.TempDir()
	ctxColour := func(tokens int) string {
		path := writeLines(t, dir, "c"+strconv.Itoa(tokens)+".jsonl",
			usageLine(map[string]any{"input_tokens": tokens, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}))
		payload := Payload{TranscriptPath: path, Model: &ModelInfo{ID: "claude-opus-5"}}
		return ContextSegment(payload, ScanTranscript(path, ""))
	}
	if !strings.Contains(ctxColour(50_000), Green) {
		t.Error("empty window should be green, like a warm cache")
	}
	if !strings.Contains(ctxColour(950_000), Red) {
		t.Error("full window should be red, like an expiring cache")
	}
}

// --- measured cache facts and model awareness -------------------------------

func turnRecord(model string, ttl1h, ttl5m, read int, stamp string, sidechain bool) string {
	if stamp == "" {
		stamp = "2026-07-26T16:52:23.464Z"
	}
	rec := map[string]any{
		"type":      "assistant",
		"timestamp": stamp,
		"message": map[string]any{
			"role":  "assistant",
			"model": model,
			"usage": map[string]any{
				"input_tokens":                3,
				"cache_read_input_tokens":     read,
				"cache_creation_input_tokens": ttl1h + ttl5m,
				"cache_creation": map[string]any{
					"ephemeral_1h_input_tokens": ttl1h,
					"ephemeral_5m_input_tokens": ttl5m,
				},
				"output_tokens": 40,
			},
		},
	}
	if sidechain {
		rec["isSidechain"] = true
	}
	data, _ := json.Marshal(rec)
	return string(data)
}

func TestAOneHourWriteIsMeasuredAsOneHour(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1334, 0, 200000, "", false))
	facts := ScanTranscript(path, "claude-opus-5")
	if facts.TTLSeconds == nil || *facts.TTLSeconds != 3600 {
		t.Errorf("got %v, want 3600", facts.TTLSeconds)
	}
}

func TestAFiveMinuteWriteIsMeasuredAsFiveMinutes(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 0, 1334, 200000, "", false))
	facts := ScanTranscript(path, "claude-opus-5")
	if facts.TTLSeconds == nil || *facts.TTLSeconds != 300 {
		t.Errorf("got %v, want 300", facts.TTLSeconds)
	}
}

func TestAPureReadKeepsLookingBackForATurnThatWrote(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl",
		turnRecord("claude-opus-5", 900, 0, 200000, "2026-07-26T16:00:00.000Z", false),
		turnRecord("claude-opus-5", 0, 0, 200000, "2026-07-26T16:30:00.000Z", false))
	facts := ScanTranscript(path, "claude-opus-5")
	if facts.TTLSeconds == nil || *facts.TTLSeconds != 3600 {
		t.Errorf("got %v, want 3600", facts.TTLSeconds)
	}
	if facts.LastTouch == nil || facts.LastTouch.Minute() != 30 {
		t.Errorf("last_touch should be the newest turn (minute 30): %v", facts.LastTouch)
	}
}

func TestNoWriteAnywhereInTheWindowLeavesTheTTLUnknown(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 0, 0, 200000, "", false))
	facts := ScanTranscript(path, "claude-opus-5")
	if facts.TTLSeconds != nil {
		t.Errorf("got %v, want nil", facts.TTLSeconds)
	}
}

func TestASubagentTurnIsNotMeasured(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-haiku-4-5", 0, 99, 200000, "", true))
	facts := ScanTranscript(path, "claude-opus-5")
	if facts.NewestModel != "" {
		t.Errorf("got %q, want empty (sidechain skipped)", facts.NewestModel)
	}
	if facts.TTLSeconds != nil {
		t.Errorf("got %v, want nil", facts.TTLSeconds)
	}
}

func TestAModelSwitchIsDetected(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))
	facts := ScanTranscript(path, "claude-sonnet-5")
	if facts.NewestModel != "claude-opus-5" {
		t.Errorf("got %q", facts.NewestModel)
	}
	if facts.LastTouch != nil {
		t.Error("expected no last_touch for a different model")
	}
	if !ModelSwitched(facts, "claude-sonnet-5") {
		t.Error("expected a switch to be detected")
	}
}

func TestSwitchingBackWithinTheWindowIsNotCold(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl",
		turnRecord("claude-opus-5", 1000, 0, 200000, "2026-07-26T16:00:00.000Z", false),
		turnRecord("claude-sonnet-5", 900, 0, 200000, "2026-07-26T16:30:00.000Z", false))
	facts := ScanTranscript(path, "claude-opus-5")
	if facts.NewestModel != "claude-sonnet-5" {
		t.Errorf("got %q", facts.NewestModel)
	}
	if facts.LastTouch == nil {
		t.Error("opus still has a cache in this window")
	}
	if ModelSwitched(facts, "claude-opus-5") {
		t.Error("switching back within the TTL must not read as cold")
	}
}

func TestNothingKnownIsNeverReportedAsASwitch(t *testing.T) {
	dir := t.TempDir()
	empty := writeLines(t, dir, "empty.jsonl")
	os.WriteFile(empty, nil, 0o644)
	facts := ScanTranscript(empty, "claude-opus-5")
	if ModelSwitched(facts, "claude-opus-5") {
		t.Error("an empty tail must not assert a switch")
	}
	missing := ScanTranscript(filepath.Join(dir, "nope.jsonl"), "claude-opus-5")
	if ModelSwitched(missing, "claude-opus-5") {
		t.Error("a missing transcript must not assert a switch")
	}
}

func TestAPayloadWithNoModelIDNeverReportsASwitch(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))
	facts := ScanTranscript(path, "")
	if ModelSwitched(facts, "") {
		t.Error("no current model must never assert a switch")
	}
}

func TestADecoratedModelIDIsNotADifferentModel(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))

	realSwitch := ScanTranscript(path, "claude-sonnet-5")
	if !ModelSwitched(realSwitch, "claude-sonnet-5") {
		t.Fatal("fixture cannot detect a real switch, so the decorated cases below prove nothing")
	}

	for _, selected := range []string{"claude-opus-5[1m]", "claude-opus-5-20260101", "us.anthropic.claude-opus-5", "CLAUDE-OPUS-5[1M]"} {
		facts := ScanTranscript(path, selected)
		if facts.LastTouch == nil {
			t.Errorf("%s: the decorated id must still match its own turns", selected)
		}
		if facts.ReadTokens <= 0 {
			t.Errorf("%s: cache reads must be attributed, not dropped", selected)
		}
		if ModelSwitched(facts, selected) {
			t.Errorf("%s: a decorated id must not read as a switch", selected)
		}
	}
}

func TestAnObservedCacheReadOutranksANameMismatch(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))
	facts := ScanTranscript(path, "claude-opus-5")
	facts.NewestModel = "some-future-id-shape-we-do-not-parse"
	if facts.ReadTokens <= 0 {
		t.Fatal("fixture must have read tokens")
	}
	if ModelSwitched(facts, "claude-opus-5") {
		t.Error("a demonstrated cache read must outrank a name mismatch")
	}
}

// --- rendering ---------------------------------------------------------------

func render(t *testing.T, home, transcript, model string, elapsedMin int, postRedReminder bool) string {
	t.Helper()
	session := "rendertest"
	stateDir := state.Dir(home)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	last := time.Now().UTC().Add(-time.Duration(elapsedMin) * time.Minute)
	cs := state.CacheState{LastMessageUTC: state.FormatTimestamp(last), PostRedReminder: postRedReminder}
	data, _ := json.Marshal(cs)
	if err := os.WriteFile(filepath.Join(stateDir, session+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"session_id":      session,
		"transcript_path": transcript,
		"model":           map[string]any{"id": model},
	}
	data, _ = json.Marshal(payload)
	var out bytes.Buffer
	Run(bytes.NewReader(data), &out, home)
	return out.String()
}

func TestASwitchRendersColdAndNamesTheNewModel(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))
	out := render(t, dir, path, "claude-sonnet-5", 10, false)
	if !strings.Contains(out, "cache: cold") {
		t.Error("expected cache: cold on a switch")
	}
	if !strings.Contains(out, "new model") {
		t.Error("expected 'new model' text")
	}
	if strings.Contains(out, "warm") {
		t.Error("must not read warm after a switch")
	}
	if !strings.Contains(out, "sonnet-5") {
		t.Error("expected the new model to be named")
	}
}

func TestTheRedZoneLatchGivesNoAdviceAgainstAWarmCache(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))
	out := render(t, dir, path, "claude-opus-5", 0, true)
	if strings.Contains(out, "compact") {
		t.Errorf("no compaction advice belongs on a warm cache: %q", out)
	}
	if !strings.Contains(out, "cache:") {
		t.Error("expected a cache: segment")
	}
	if !strings.Contains(out, "cold by") {
		t.Error("expected the clock to still show cold by")
	}
}

func TestTheRedZoneLabelStillAppearsOnAGenuinelyOldCache(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))
	out := render(t, dir, path, "claude-opus-5", 57, false)
	if !strings.Contains(out, "expiring") {
		t.Errorf("expected expiring in %q", out)
	}
	if strings.Contains(strings.ToLower(out), "compact") {
		t.Error("the label must never advise")
	}
}

func TestTheColdStateSurvivesAJustCompactedContext(t *testing.T) {
	dir := t.TempDir()
	compact, _ := json.Marshal(map[string]any{"type": "user", "isCompactSummary": true, "message": map[string]any{"content": "s"}})
	path := writeLines(t, dir, "compacted.jsonl", string(compact))
	out := render(t, dir, path, "claude-opus-5", 900, false)
	if !strings.Contains(out, "cache: cold") {
		t.Errorf("expected cache: cold, got %q", out)
	}
	if !strings.Contains(out, "context: just compacted") {
		t.Errorf("expected context: just compacted, got %q", out)
	}
}

func TestALapsedTTLRendersColdAndStopsForecastingItsExpiry(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))
	out := render(t, dir, path, "claude-opus-5", 416, false)
	if !strings.Contains(out, "cold") {
		t.Error("expected cold")
	}
	if strings.Contains(out, "cold by") {
		t.Error("an expiry already in the past is a fact, not a forecast")
	}
	if !strings.Contains(out, "since") {
		t.Error("expected since")
	}
	if strings.Contains(out, "warm") {
		t.Error("must not say warm")
	}
}

func TestAMeasuredOneHourTTLIsLabelled1h(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))
	out := render(t, dir, path, "claude-opus-5", 1, false)
	if !strings.Contains(out, "1h TTL)") {
		t.Errorf("expected 1h TTL, got %q", out)
	}
	if !strings.Contains(out, "warm") {
		t.Error("expected warm")
	}
}

func TestAMeasuredFiveMinuteTTLIsLabelled5mNot1h(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 0, 1000, 200000, "", false))
	out := render(t, dir, path, "claude-opus-5", 1, false)
	if !strings.Contains(out, "5m TTL)") {
		t.Errorf("expected 5m TTL, got %q", out)
	}
	if strings.Contains(out, "1h") {
		t.Error("must not mention 1h")
	}
}

func TestAMeasuredTTLOverridesTheOverageFlagFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.OverageFlagPath(dir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))
	out := render(t, dir, path, "claude-opus-5", 10, false)
	if !strings.Contains(out, "1h TTL)") {
		t.Errorf("expected the measured TTL to win, got %q", out)
	}
	if strings.Contains(out, "cold if overage") {
		t.Error("a measured TTL must remove the hedge wording")
	}
}

func TestWithoutAMeasurementTheFlagFileStillDecides(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.OverageFlagPath(dir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 0, 0, 200000, "", false))
	out := render(t, dir, path, "claude-opus-5", 10, false)
	if !strings.Contains(out, "ovg") {
		t.Errorf("expected the overage fallback, got %q", out)
	}
}

// --- the model is named on every line, not only on a switch ------------------

func TestTheModelIsNamedOnAnOrdinaryLine(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))
	out := render(t, dir, path, "claude-opus-5", 10, false)
	if !strings.Contains(out, "opus-5") {
		t.Errorf("expected opus-5 to be named, got %q", out)
	}
}

func TestTheVendorPrefixIsDroppedButTheFamilyIsNot(t *testing.T) {
	if got := ShortModel("claude-sonnet-5"); got != "sonnet-5" {
		t.Errorf("got %q", got)
	}
	if got := ShortModel("claude-haiku-4-5"); got != "haiku-4-5" {
		t.Errorf("got %q", got)
	}
	if got := ShortModel("something-else"); got != "something-else" {
		t.Errorf("got %q", got)
	}
}

func TestNoModelInThePayloadRendersNoSegment(t *testing.T) {
	if got := ModelSegment(Payload{}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := ModelSegment(Payload{Model: &ModelInfo{}}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestTheHarnessDisplayNameWinsOverOurOwnSlicing(t *testing.T) {
	seg := ModelSegment(Payload{Model: &ModelInfo{ID: "claude-opus-5", DisplayName: "Opus 5"}})
	if !strings.Contains(seg, "Opus 5") {
		t.Errorf("got %q", seg)
	}
	if strings.Contains(seg, "opus-5") {
		t.Errorf("got %q, must not also contain the sliced id", seg)
	}
}

func TestTheIDSliceIsTheFallbackWhenThereIsNoDisplayName(t *testing.T) {
	if !strings.Contains(ModelSegment(Payload{Model: &ModelInfo{ID: "claude-sonnet-5"}}), "sonnet-5") {
		t.Error("expected sonnet-5 fallback")
	}
	if !strings.Contains(ModelSegment(Payload{Model: &ModelInfo{ID: "claude-sonnet-5", DisplayName: "   "}}), "sonnet-5") {
		t.Error("expected sonnet-5 fallback when display_name is blank")
	}
}

func TestTheSwitchLineStillIdentifiesTheNewModel(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1000, 0, 200000, "", false))
	out := render(t, dir, path, "claude-sonnet-5", 10, false)
	if !strings.Contains(out, "sonnet-5") {
		t.Error("expected sonnet-5")
	}
	if !strings.Contains(out, "new model") {
		t.Error("expected new model")
	}
	if strings.Count(out, "sonnet-5") != 1 {
		t.Errorf("expected sonnet-5 exactly once, got %d in %q", strings.Count(out, "sonnet-5"), out)
	}
}

// --- one parse per refresh ----------------------------------------------------

func TestTheMergedScanStillReportsACompactionAndTheCacheTogether(t *testing.T) {
	dir := t.TempDir()
	compact, _ := json.Marshal(map[string]any{"isCompactSummary": true, "type": "user"})
	path := writeLines(t, dir, "t.jsonl",
		turnRecord("claude-opus-5", 1000, 0, 200000, "2026-07-26T16:00:00.000Z", false),
		string(compact))
	scan := ScanTranscript(path, "claude-opus-5")
	if scan.ContextTokens == nil || *scan.ContextTokens != PostCompact {
		t.Errorf("got %v, want PostCompact", scan.ContextTokens)
	}
	if scan.TTLSeconds == nil || *scan.TTLSeconds != 3600 {
		t.Errorf("got %v, want 3600", scan.TTLSeconds)
	}
	if scan.LastTouch == nil {
		t.Error("expected last_touch to still be found behind the compaction")
	}
}

// --- the full-price segment ---------------------------------------------------

func TestARoutineTurnSaysNothing(t *testing.T) {
	if got := FullPriceSegment(ScanResult{ReadTokens: 173000}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestALargeUncachedTurnIsReported(t *testing.T) {
	seg := plain(FullPriceSegment(ScanResult{TurnUncached: 227000}))
	if !strings.Contains(seg, "turn: 227k full price") {
		t.Errorf("got %q", seg)
	}
}

func TestAColdPrefixIsReportedRatherThanHidden(t *testing.T) {
	seg := plain(FullPriceSegment(ScanResult{TurnUncached: 174000}))
	if !strings.Contains(seg, "turn: 174k full price") {
		t.Errorf("got %q", seg)
	}
}

func TestNothingToMeasureStaysSilent(t *testing.T) {
	if got := FullPriceSegment(ScanResult{TurnUncached: 0}); got != "" {
		t.Errorf("got %q", got)
	}
	if got := FullPriceSegment(ScanResult{}); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestMillionsAreShownWithTwoDecimalsToo(t *testing.T) {
	seg := plain(FullPriceSegment(ScanResult{TurnUncached: 1240000}))
	if !strings.Contains(seg, "1.24M") {
		t.Errorf("got %q", seg)
	}
}

func promptRecord(stamp string) string {
	rec := map[string]any{"type": "user", "promptId": "p1", "timestamp": stamp, "message": map[string]any{"role": "user", "content": "go"}}
	data, _ := json.Marshal(rec)
	return string(data)
}

func toolResultRecord(stamp string) string {
	rec := map[string]any{"type": "user", "promptId": "p1", "timestamp": stamp,
		"toolUseResult": map[string]any{"stdout": "ok"}, "message": map[string]any{"role": "user", "content": "ok"}}
	data, _ := json.Marshal(rec)
	return string(data)
}

func TestTheFigureSurvivesTheCheapStepsThatFollowIt(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl",
		promptRecord("2026-07-26T16:50:00.000Z"),
		turnRecord("claude-opus-5", 200000, 0, 0, "2026-07-26T16:50:10.000Z", false),
		toolResultRecord("2026-07-26T16:50:20.000Z"),
		turnRecord("claude-opus-5", 0, 0, 200000, "2026-07-26T16:50:30.000Z", false),
		toolResultRecord("2026-07-26T16:50:40.000Z"),
		turnRecord("claude-opus-5", 0, 0, 200000, "2026-07-26T16:50:50.000Z", false),
	)
	scan := ScanTranscript(path, "claude-opus-5")
	if scan.TurnUncached < 200000 {
		t.Errorf("expected the expensive step to still be counted, got %d", scan.TurnUncached)
	}
	if !strings.Contains(plain(FullPriceSegment(scan)), "full price") {
		t.Error("expected a full price segment")
	}
}

func TestTheNextPromptResetsIt(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl",
		promptRecord("2026-07-26T16:40:00.000Z"),
		turnRecord("claude-opus-5", 500000, 0, 0, "2026-07-26T16:40:10.000Z", false),
		promptRecord("2026-07-26T16:50:00.000Z"),
		turnRecord("claude-opus-5", 0, 0, 200000, "2026-07-26T16:50:10.000Z", false),
	)
	scan := ScanTranscript(path, "claude-opus-5")
	if scan.TurnUncached >= fullPriceNoticeTokens {
		t.Errorf("the previous turn must not bleed into this one: %d", scan.TurnUncached)
	}
	if got := FullPriceSegment(scan); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestASubagentDoesNotInflateTheTurn(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl",
		promptRecord("2026-07-26T16:50:00.000Z"),
		turnRecord("claude-opus-5", 0, 0, 200000, "2026-07-26T16:50:10.000Z", false),
		turnRecord("claude-opus-5", 900000, 0, 0, "2026-07-26T16:50:20.000Z", true),
	)
	scan := ScanTranscript(path, "claude-opus-5")
	if scan.TurnUncached >= fullPriceNoticeTokens {
		t.Errorf("a sidechain must not inflate the turn: %d", scan.TurnUncached)
	}
}

func TestItAppearsOnARenderedLineWhenItShould(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 300000, 0, 50000, "", false))
	out := plain(render(t, dir, path, "claude-opus-5", 1, false))
	if !strings.Contains(out, "turn: 300k full price") {
		t.Errorf("got %q", out)
	}
}

// --- the elapsed clock past the hour -------------------------------------------

func TestTheClockKeepsSecondsWhileTheTTLIsLive(t *testing.T) {
	cases := map[float64]string{0: "00:00", 192: "03:12", 3599: "59:59"}
	for elapsed, want := range cases {
		if got := ElapsedClock(elapsed); got != want {
			t.Errorf("ElapsedClock(%v) = %q, want %q", elapsed, got, want)
		}
	}
}

func TestTheClockConvertsToHoursRatherThanRunningTo300(t *testing.T) {
	if got := ElapsedClock(3600); got != "1h00m" {
		t.Errorf("got %q, want 1h00m", got)
	}
	if got := ElapsedClock(17520); got != "4h52m" {
		t.Errorf("got %q, want 4h52m", got)
	}
}

func TestTheClockIsNotCapped(t *testing.T) {
	if got := ElapsedClock(86400 * 2); got != "48h00m" {
		t.Errorf("got %q, want 48h00m", got)
	}
}

// --- the 200k cross-check (temporary) -------------------------------------------

func boolPtr(b bool) *bool { return &b }
func intPtr(n int) *int    { return &n }

func TestThe200kCheckIsSilentWhenOurCountAgrees(t *testing.T) {
	if got := LimitCheckSegment(Payload{Exceeds200kTokens: boolPtr(false)}, ScanResult{ContextTokens: intPtr(100000)}); got != "" {
		t.Errorf("got %q", got)
	}
	if got := LimitCheckSegment(Payload{Exceeds200kTokens: boolPtr(true)}, ScanResult{ContextTokens: intPtr(300000)}); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestThe200kCheckSpeaksUpOnDisagreement(t *testing.T) {
	out := plain(LimitCheckSegment(Payload{Exceeds200kTokens: boolPtr(true)}, ScanResult{ContextTokens: intPtr(100000)}))
	if !strings.Contains(out, "200k flag disagrees") {
		t.Errorf("got %q", out)
	}
}

func TestThe200kCheckStaysQuietWithoutAReadingToCompare(t *testing.T) {
	pc := PostCompact
	if got := LimitCheckSegment(Payload{Exceeds200kTokens: boolPtr(true)}, ScanResult{ContextTokens: &pc}); got != "" {
		t.Errorf("got %q", got)
	}
	if got := LimitCheckSegment(Payload{Exceeds200kTokens: boolPtr(true)}, ScanResult{ContextTokens: nil}); got != "" {
		t.Errorf("got %q", got)
	}
	if got := LimitCheckSegment(Payload{}, ScanResult{ContextTokens: intPtr(100000)}); got != "" {
		t.Errorf("got %q", got)
	}
}
