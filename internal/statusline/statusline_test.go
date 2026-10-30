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

func windowPayload(size int) Payload {
	return Payload{ContextWindow: &struct {
		Size int `json:"context_window_size"`
	}{Size: size}}
}

func TestTheWindowIsWhatClaudeCodeReports(t *testing.T) {
	for _, size := range []int{200_000, 1_000_000, 750_000} {
		if got := ContextLimit(windowPayload(size)); got != size {
			t.Errorf("got %d, want %d", got, size)
		}
	}
}

func TestTheModelIDIsNotConsultedForTheWindow(t *testing.T) {
	p := Payload{Model: &ModelInfo{ID: "claude-haiku-4-5-20251001"}}
	if got := ContextLimit(p); got != 0 {
		t.Errorf("got %d, want 0 (unknown) when no window is reported", got)
	}
}

func TestNoWindowReportedMeansUnknownNotAGuess(t *testing.T) {
	for _, p := range []Payload{{}, windowPayload(0), windowPayload(-5)} {
		if got := ContextLimit(p); got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	}
}

func TestTheClockIsShownInTheMachinesOwnZone(t *testing.T) {
	if displayTZ() != time.Local {
		t.Error("the display zone must be the machine's own, with no setting")
	}
}

func TestWithoutAWindowTheSegmentShowsTheCountAndNoPercentage(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "session.jsonl", usageLine(nil))
	seg := plain(ContextSegment(Payload{TranscriptPath: path}, ScanTranscript(path, "")))
	if strings.TrimSpace(seg) != "context 222k" {
		t.Errorf("got %q", seg)
	}
}

// --- rendering -------------------------------------------------------------

func TestTheSegmentShowsTokensCeilingAndPercentage(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "session.jsonl", usageLine(nil))
	payload := windowPayload(1_000_000)
	payload.TranscriptPath = path
	payload.Model = &ModelInfo{ID: "claude-opus-5"}
	seg := plain(ContextSegment(payload, ScanTranscript(path, "")))
	if strings.TrimSpace(seg) != "context 222k/1M 22%" {
		t.Errorf("got %q", seg)
	}
}

func TestMillionsAreShownWithTwoDecimals(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "m.jsonl", usageLine(map[string]any{"cache_read_input_tokens": 1240000}))
	payload := windowPayload(1_000_000)
	payload.TranscriptPath = path
	payload.Model = &ModelInfo{ID: "claude-opus-5"}
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
		payload := windowPayload(1_000_000)
		payload.TranscriptPath = path
		payload.Model = &ModelInfo{ID: "claude-opus-5"}
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
	payload := windowPayload(1_000_000)
	payload.TranscriptPath = path
	payload.Model = &ModelInfo{ID: "claude-opus-5"}
	if seg := ContextSegment(payload, ScanTranscript(path, "")); seg != "" {
		t.Errorf("no figure and no label until the next response; the cache segment says when: %q", seg)
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

func TestFiveMinuteTTLUsesOneBandPerMinute(t *testing.T) {
	cases := []struct {
		elapsed float64
		colour  string
		bold    bool
	}{
		{0, Green, true}, {59, Green, true},
		{60, Green, false}, {119, Green, false},
		{120, Yellow, true}, {179, Yellow, true},
		{180, Yellow, false}, {239, Yellow, false},
		{240, Red, true}, {299, Red, true},
		{300, Red, false}, {900, Red, false},
	}
	for _, c := range cases {
		colour, bold := Bracket(c.elapsed, 300)
		if colour != c.colour || bold != c.bold {
			t.Errorf("Bracket(%v, 300) = %s bold=%v, want %s bold=%v", c.elapsed, colour, bold, c.colour, c.bold)
		}
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
		payload := windowPayload(1_000_000)
		payload.TranscriptPath = path
		payload.Model = &ModelInfo{ID: "claude-opus-5"}
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

func TestAPureReadDoesNotHideTheNewestTouch(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl",
		turnRecord("claude-opus-5", 900, 0, 200000, "2026-07-26T16:00:00.000Z", false),
		turnRecord("claude-opus-5", 0, 0, 200000, "2026-07-26T16:30:00.000Z", false))
	facts := ScanTranscript(path, "claude-opus-5")
	if facts.LastTouch == nil || facts.LastTouch.Minute() != 30 {
		t.Errorf("last_touch should be the newest turn (minute 30): %v", facts.LastTouch)
	}
}

func TestASubagentTurnIsNotMeasured(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-haiku-4-5", 0, 99, 200000, "", true))
	facts := ScanTranscript(path, "claude-opus-5")
	if facts.NewestModel != "" {
		t.Errorf("got %q, want empty (sidechain skipped)", facts.NewestModel)
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
	return renderTTL(t, home, transcript, model, elapsedMin, postRedReminder, map[string]any{"ttl": "1h"})
}

// renderTTL is render with an explicit prompt_cache object; nil leaves the
// payload without one.
func renderTTL(t *testing.T, home, transcript, model string, elapsedMin int, postRedReminder bool, cache any) string {
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
	if cache != nil {
		payload["prompt_cache"] = cache
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

func TestAfterACompactionTheCacheSegmentShowsTimeSinceAndWhen(t *testing.T) {
	dir := t.TempDir()
	compact, _ := json.Marshal(map[string]any{"type": "user", "isCompactSummary": true, "timestamp": "2026-07-26T16:20:00.000Z", "message": map[string]any{"content": "s"}})
	path := writeLines(t, dir, "compacted.jsonl",
		turnRecord("claude-opus-5", 1000, 0, 200000, "2026-07-26T16:00:00.000Z", false),
		string(compact))
	want, _ := time.Parse(time.RFC3339, "2026-07-26T16:20:00Z")
	for _, minutes := range []int{1, 47, 900} {
		out := plain(render(t, dir, path, "claude-opus-5", minutes, false))
		if !strings.Contains(out, "cache: compacted ") || !strings.Contains(out, " / since "+want.In(time.Local).Format("15:04")) {
			t.Errorf("%d min: expected compacted, an age and the compaction time, got %q", minutes, out)
		}
		if strings.Contains(out, "context") {
			t.Errorf("%d min: the cache segment alone reports a compaction: %q", minutes, out)
		}
		if !strings.HasSuffix(strings.TrimSuffix(out, "\n"), want.In(time.Local).Format("15:04")) {
			t.Errorf("%d min: the line should end at the time, with no stray spacing: %q", minutes, out)
		}
		for _, banned := range []string{"warm", "cooling", "expiring", "cold", "TTL"} {
			if strings.Contains(out, banned) {
				t.Errorf("%d min: no clock or cache state belongs here (%q): %q", minutes, banned, out)
			}
		}
	}
}

func TestACompactionWithoutATimestampFallsBackToTheLastMessage(t *testing.T) {
	dir := t.TempDir()
	compact, _ := json.Marshal(map[string]any{"type": "user", "isCompactSummary": true, "message": map[string]any{"content": "s"}})
	path := writeLines(t, dir, "compacted.jsonl", string(compact))
	before := time.Now().Add(-30 * time.Minute).In(time.Local).Format("15:04")
	out := plain(render(t, dir, path, "claude-opus-5", 30, false))
	after := time.Now().Add(-30 * time.Minute).In(time.Local).Format("15:04")
	if !strings.Contains(out, "since "+before) && !strings.Contains(out, "since "+after) {
		t.Errorf("expected %s (or %s across a minute boundary), got %q", before, after, out)
	}
}

func TestTheNextResponseBringsTheClockBack(t *testing.T) {
	dir := t.TempDir()
	compact, _ := json.Marshal(map[string]any{"type": "user", "isCompactSummary": true, "timestamp": "2026-07-26T16:20:00.000Z", "message": map[string]any{"content": "s"}})
	path := writeLines(t, dir, "compacted.jsonl",
		string(compact),
		turnRecord("claude-opus-5", 1000, 0, 20000, "2026-07-26T16:25:00.000Z", false))
	out := plain(render(t, dir, path, "claude-opus-5", 2, false))
	if strings.Contains(out, "compacted") || !strings.Contains(out, "cold by") {
		t.Errorf("a response after the compaction must restore the clock: %q", out)
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
	out := renderTTL(t, dir, path, "claude-opus-5", 1, false, map[string]any{"ttl": "5m"})
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
	out := renderTTL(t, dir, path, "claude-opus-5", 10, false, nil)
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
	if scan.LastTouch == nil {
		t.Error("expected last_touch to still be found behind the compaction")
	}
}

// --- the rewrite segment ------------------------------------------------------

func TestTheTiersAreOneDollarPer200KRoundedUp(t *testing.T) {
	cases := map[int]int{
		1: 1, 150_000: 1, 200_000: 1,
		200_001: 2, 400_000: 2,
		400_001: 3, 600_000: 3,
		600_001: 4, 800_000: 4,
		800_001: 5, 1_000_000: 5, 5_000_000: 5,
	}
	for tokens, want := range cases {
		if got := RewriteTier(tokens); got != want {
			t.Errorf("RewriteTier(%d) = %d, want %d", tokens, got, want)
		}
	}
}

func TestTheSegmentShowsDollarsAndTheAmount(t *testing.T) {
	cases := map[int]string{
		120_000:   "$ 120k",
		300_000:   "$$ 300k",
		412_000:   "$$$ 412k",
		700_000:   "$$$$ 700k",
		1_240_000: "$$$$$ 1.24M",
	}
	for tokens, want := range cases {
		if got := plain(RewriteSegment(ScanResult{TurnRewrite: tokens})); got != "  "+want {
			t.Errorf("%d: got %q, want %q", tokens, got, "  "+want)
		}
	}
}

func TestTheColourFollowsTheTier(t *testing.T) {
	cases := map[int]string{
		150_000: Green,
		300_000: Yellow, 500_000: Yellow,
		700_000: Red, 900_000: Red,
	}
	for tokens, colour := range cases {
		if got := RewriteSegment(ScanResult{TurnRewrite: tokens}); !strings.Contains(got, colour) {
			t.Errorf("%d: expected colour %q in %q", tokens, colour, got)
		}
		if strings.Contains(RewriteSegment(ScanResult{TurnRewrite: tokens}), Bold) {
			t.Errorf("%d: the segment must not use bold", tokens)
		}
	}
}

func TestNothingToReportStaysSilent(t *testing.T) {
	for _, scan := range []ScanResult{{}, {ReadTokens: 173000}, {TurnRewrite: 0}} {
		if got := RewriteSegment(scan); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	}
}

// scanRewrite scans a one-record transcript so the thresholds are exercised
// through the real scanner, not by building a ScanResult by hand.
//
// turnRecord always adds 3 input tokens, so uncached is the request's whole
// uncached figure and the cache write is 3 less than it.
func scanRewrite(t *testing.T, uncached, read int) int {
	t.Helper()
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl",
		promptRecord("2026-07-26T16:50:00.000Z"),
		turnRecord("claude-opus-5", uncached-3, 0, read, "2026-07-26T16:50:10.000Z", false),
	)
	return ScanTranscript(path, "claude-opus-5").TurnRewrite
}

func TestAnOrdinaryTurnIsNotARewrite(t *testing.T) {
	if got := scanRewrite(t, 4000, 300000); got != 0 {
		t.Errorf("a warm cache must read as no rewrite, got %d", got)
	}
}

func TestAColdRequestIsARewriteOfItsUncachedTokens(t *testing.T) {
	if got := scanRewrite(t, 300000, 0); got != 300000 {
		t.Errorf("got %d, want 300000", got)
	}
}

func TestTheReadShareThresholdIsATenth(t *testing.T) {
	// Reading exactly a tenth of the context is not a rewrite; just under is.
	if got := scanRewrite(t, 270000, 30000); got != 0 {
		t.Errorf("10%% read must not count, got %d", got)
	}
	if got := scanRewrite(t, 271000, 29000); got == 0 {
		t.Error("just under 10% read must count")
	}
}

func TestASmallContextIsNeverARewrite(t *testing.T) {
	if got := scanRewrite(t, 50000, 0); got != 0 {
		t.Errorf("a context of 50K or less is too small to mention, got %d", got)
	}
	if got := scanRewrite(t, 50001, 0); got == 0 {
		t.Error("just over 50K with nothing read must count")
	}
}

func TestAFirstRequestOrACompactionCountsToo(t *testing.T) {
	// Nothing about the rule asks whether the cache was previously warm:
	// both are real cache writes at full write price, and rare.
	dir := t.TempDir()
	compact := map[string]any{"type": "user", "isCompactSummary": true, "timestamp": "2026-07-26T16:49:00.000Z",
		"message": map[string]any{"role": "user", "content": "summary"}}
	data, _ := json.Marshal(compact)
	path := writeLines(t, dir, "t.jsonl",
		string(data),
		turnRecord("claude-opus-5", 119997, 0, 0, "2026-07-26T16:50:10.000Z", false),
	)
	if got := ScanTranscript(path, "claude-opus-5").TurnRewrite; got != 120000 {
		t.Errorf("got %d, want 120000", got)
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
		turnRecord("claude-opus-5", 199997, 0, 0, "2026-07-26T16:50:10.000Z", false),
		toolResultRecord("2026-07-26T16:50:20.000Z"),
		turnRecord("claude-opus-5", 0, 0, 200000, "2026-07-26T16:50:30.000Z", false),
		toolResultRecord("2026-07-26T16:50:40.000Z"),
		turnRecord("claude-opus-5", 0, 0, 200000, "2026-07-26T16:50:50.000Z", false),
	)
	scan := ScanTranscript(path, "claude-opus-5")
	if scan.TurnRewrite != 200000 {
		t.Errorf("the rewrite must outlast the cheap steps after it, got %d", scan.TurnRewrite)
	}
	if got := plain(RewriteSegment(scan)); got != "  $ 200k" {
		t.Errorf("got %q", got)
	}
}

func TestTheLargestRewriteInTheTurnWins(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl",
		promptRecord("2026-07-26T16:50:00.000Z"),
		turnRecord("claude-opus-5", 99997, 0, 0, "2026-07-26T16:50:10.000Z", false),
		toolResultRecord("2026-07-26T16:50:20.000Z"),
		turnRecord("claude-opus-5", 499997, 0, 0, "2026-07-26T16:50:30.000Z", false),
	)
	if got := ScanTranscript(path, "claude-opus-5").TurnRewrite; got != 500000 {
		t.Errorf("got %d, want 500000", got)
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
	if scan.TurnRewrite != 0 {
		t.Errorf("the previous turn must not bleed into this one: %d", scan.TurnRewrite)
	}
	if got := RewriteSegment(scan); got != "" {
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
	if got := ScanTranscript(path, "claude-opus-5").TurnRewrite; got != 0 {
		t.Errorf("a sidechain must not count: %d", got)
	}
}

func TestItAppearsOnARenderedLineWhenItShould(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 299997, 0, 0, "", false))
	out := plain(render(t, dir, path, "claude-opus-5", 1, false))
	if !strings.Contains(out, "$$ 300k") {
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

func intPtr(n int) *int { return &n }

// --- 200K windows ---------------------------------------------------------------

func TestA200KWindowShowsItsOwnCeilingAndRewriteTier(t *testing.T) {
	// A 200K-window model (Haiku 4.5, for example): the gauge follows the
	// reported window, and a rewrite of the whole window can reach one dollar
	// sign and no more.
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl",
		promptRecord("2026-07-26T16:50:00.000Z"),
		turnRecord("claude-haiku-4-5", 169997, 0, 0, "2026-07-26T16:50:10.000Z", false),
	)
	payload := windowPayload(200_000)
	payload.TranscriptPath = path
	scan := ScanTranscript(path, "claude-haiku-4-5")
	if seg := plain(ContextSegment(payload, scan)); !strings.Contains(seg, "170k/200k") || !strings.Contains(seg, "85%") {
		t.Errorf("got %q", seg)
	}
	if got := plain(RewriteSegment(scan)); got != "  $ 170k" {
		t.Errorf("a whole-window rewrite on 200K must read $, got %q", got)
	}
	if RewriteTier(200_000) != 1 {
		t.Error("the top of a 200K window must be one dollar sign")
	}
}

func TestAOneMillionWindowOnAnyModelNameShowsItsOwnCeiling(t *testing.T) {
	// The window comes from the payload, never from the model name: Haiku 5.5
	// has a 1M window, so a model id containing "haiku" must not imply 200K.
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-haiku-5-5", 0, 0, 300000, "", false))
	payload := windowPayload(1_000_000)
	payload.TranscriptPath = path
	payload.Model = &ModelInfo{ID: "claude-haiku-5-5"}
	if seg := plain(ContextSegment(payload, ScanTranscript(path, "claude-haiku-5-5"))); !strings.Contains(seg, "/1M") {
		t.Errorf("got %q", seg)
	}
}

// --- prompt_cache.ttl from the payload ----------------------------------------

// renderWithCache is render with a prompt_cache object in the payload.
func renderWithCache(t *testing.T, home, transcript, model string, elapsedMin int, cache any) string {
	t.Helper()
	session := "cachetest"
	stateDir := state.Dir(home)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	last := time.Now().UTC().Add(-time.Duration(elapsedMin) * time.Minute)
	data, _ := json.Marshal(state.CacheState{LastMessageUTC: state.FormatTimestamp(last)})
	if err := os.WriteFile(filepath.Join(stateDir, session+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"session_id":      session,
		"transcript_path": transcript,
		"model":           map[string]any{"id": model},
		"version":         "2.1.301",
	}
	if cache != nil {
		payload["prompt_cache"] = cache
	}
	data, _ = json.Marshal(payload)
	var out bytes.Buffer
	Run(bytes.NewReader(data), &out, home)
	return out.String()
}

func TestThePayloadCanReportFiveMinutesOverAnHourInTheTranscript(t *testing.T) {
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1334, 0, 50000, "", false))
	out := renderWithCache(t, dir, path, "claude-opus-5", 10, map[string]any{"ttl": "5m"})
	if !strings.Contains(out, "5m TTL)") || !strings.Contains(out, "since") {
		t.Errorf("a 5m payload TTL ten minutes on must read as lapsed: %q", out)
	}
}

func TestWithoutAReportedTTLSinceAppearsOnlyOncePastTheHour(t *testing.T) {
	dir := t.TempDir()
	// The transcript says a five-minute write. It is not consulted.
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 0, 1334, 50000, "", false))
	for name, cache := range map[string]any{
		"absent":      nil,
		"empty":       map[string]any{},
		"null ttl":    map[string]any{"ttl": nil},
		"unknown ttl": map[string]any{"ttl": "24h"},
	} {
		out := renderWithCache(t, dir, path, "claude-opus-5", 10, cache)
		if strings.Contains(out, "TTL") || strings.Contains(out, "cold by") || strings.Contains(out, "since") {
			t.Errorf("%s: no TTL is known, so none is named and no expiry forecast: %q", name, out)
		}
		if strings.Contains(plain(out), " / ") {
			t.Errorf("%s: inside the hour no time is shown: %q", name, out)
		}
		old := plain(renderWithCache(t, dir, path, "claude-opus-5", 200*60, cache))
		if !strings.Contains(old, "cold ") || !strings.Contains(old, " / since ") || strings.Contains(old, "TTL") {
			t.Errorf("%s: past the hour, since with no TTL named: %q", name, old)
		}
		if !strings.Contains(plain(out), "cache: warm 10:00") {
			t.Errorf("%s: the clock should still run on the 1h default: %q", name, out)
		}
		if strings.Contains(out, "5m") {
			t.Errorf("%s: the transcript's write must not set the TTL: %q", name, out)
		}
	}
}

func TestThePayloadTTLFeedsTheIdleCompactionGate(t *testing.T) {
	// Idle compaction is armed only on a one-hour TTL. A five-minute payload
	// TTL must keep it off even where the transcript says one hour.
	dir := t.TempDir()
	path := writeLines(t, dir, "t.jsonl", turnRecord("claude-opus-5", 1334, 0, 300000, "", false))
	armed := renderWithCache(t, dir, path, "claude-opus-5", 30, map[string]any{"ttl": "1h"})
	if !strings.Contains(armed, "idle compaction") {
		t.Fatalf("fixture must arm idle compaction on a 1h TTL: %q", armed)
	}
	out := renderWithCache(t, dir, path, "claude-opus-5", 30, map[string]any{"ttl": "5m"})
	if strings.Contains(out, "idle compaction") {
		t.Errorf("a 5m TTL must not arm idle compaction: %q", out)
	}
}

func TestCompactedAgoSpansMinutesToYears(t *testing.T) {
	for _, c := range []struct {
		secs float64
		want string
	}{
		{-5, "0m"}, {0, "0m"}, {59, "0m"}, {60, "1m"}, {59*60 + 59, "59m"},
		{3600, "1h00m"}, {5*3600 + 5*60, "5h05m"}, {24*3600 - 1, "23h59m"},
		{24 * 3600, "1d0h00m"}, {6*86400 + 23*3600, "6d23h00m"},
		{7 * 86400, "1w0d0h00m"}, {200*3600 + 5*60, "1w1d8h05m"},
		{30 * 86400, "1mo0w0d0h00m"}, {75*86400 + 4*3600 + 6*60, "2mo2w1d4h06m"},
		{365 * 86400, "1y0mo0w0d0h00m"}, {400*86400 + 5*3600 + 6*60, "1y1mo0w5d5h06m"},
	} {
		if got := CompactedAgo(c.secs); got != c.want {
			t.Errorf("CompactedAgo(%v) = %q, want %q", c.secs, got, c.want)
		}
	}
}

func TestCompactedIsBoldGreenAndTheAgeAndTimeAreGreenNotBold(t *testing.T) {
	dir := t.TempDir()
	compact, _ := json.Marshal(map[string]any{"type": "user", "isCompactSummary": true, "timestamp": "2026-07-26T16:20:00.000Z", "message": map[string]any{"content": "s"}})
	path := writeLines(t, dir, "compacted.jsonl",
		turnRecord("claude-opus-5", 1000, 0, 200000, "2026-07-26T16:00:00.000Z", false), string(compact))
	raw := render(t, dir, path, "claude-opus-5", 5, false)
	if !strings.Contains(raw, Green+Bold+"cache: compacted"+Reset+" "+Green) {
		t.Errorf("heading must be bold green, then green: %q", raw)
	}
	tail := raw[strings.Index(raw, "cache: compacted"):]
	if strings.Contains(tail[strings.Index(tail, Reset):], Bold) {
		t.Errorf("only the heading is bold: %q", raw)
	}
}
