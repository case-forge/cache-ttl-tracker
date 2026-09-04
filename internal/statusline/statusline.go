// Package statusline approximates prompt-cache freshness for the
// statusLine command, per session.
//
// No API exposes live prompt-cache state, so this estimates it from
// elapsed wall-clock time since the last assistant turn ended (written
// per-session by the tracker hook, "turn end" being the closest available
// proxy for "cache last touched") against the known TTLs:
//   - 1 hour, while on included subscription usage
//   - 5 minutes, once usage has fallen into paid overage credits
//
// Overage monitoring is off by default (most sessions stay on-plan and
// never hit overage); toggle it on by creating state.OverageFlagPath;
// remove it to go back to plan-only. The statusline re-reads the flag on
// every refresh, so toggling takes effect within one refresh interval, no
// restart needed.
//
//	Overage monitoring OFF (default): one estimate, assumes the 1h plan TTL.
//	  green (<1h elapsed): warm.  red (>=1h elapsed): cold.
//	Overage monitoring ON: both TTLs shown.
//	  green  (<5m elapsed):   warm under either TTL.
//	  amber  (5m-1h elapsed): warm on the 1h TTL, already cold if in overage.
//	  red    (>=1h elapsed):  cold under either TTL.
//
// Best-effort throughout; nothing here may panic or block the status line
// on error.
//
// The "cache: <label>" text is always bold. The trailing detail's weight
// toggles by fraction of the applicable TTL elapsed:
//
//	0-25%    bold    55-91.7% unbold
//	25-50%   unbold  91.7-100% bold
//	50-75%   bold    100%+    unbold
//
// For the default 1h TTL that's green 0-15m/15-30m, yellow 30-45m/45-55m,
// red 55-60m/60m+. Bold marks the fresher half of each colour's span.
//
// Red-zone is a state report, not advice: three imperatives ("send message
// then compact", "compact now", "compact first") were removed over time
// after the owner's 2026-08-07 instruction to stop giving value judgements
// on whether/when to compact and just report the state of the cache. Bold
// red ("expiring") is the closing stretch *before* the TTL lapses:
// elapsed < ttl, the cache is still valid, the colour alone carries the
// urgency. Unbold red ("cold") means the TTL has genuinely lapsed; the
// trailing clock switches from "cold by HH:MM" to "since HH:MM" at that
// point, because an expiry that has already passed is a fact, not a
// forecast.
package statusline

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/case-forge/cache-ttl-tracker/internal/state"
)

// ANSI escapes. Colour is state; bold/dim is emphasis within that state.
const (
	Green  = "\033[32m"
	Yellow = "\033[33m"
	Red    = "\033[31m"
	Reset  = "\033[0m"
	Bold   = "\033[1m"
	Dim    = "\033[2m"
)

const (
	FiveMinSeconds = state.FiveMinSeconds
	OneHourSeconds = state.OneHourSeconds
)

// PostCompact is a ContextTokens sentinel: compaction has happened but no
// turn has run since, so no usage block yet describes the new window.
// Distinct from a nil pointer ("nothing readable at all") because the right
// thing to show differs; see ContextSegment.
const PostCompact = -1

// tailBytes bounds every transcript read. The statusline runs every second
// (refreshInterval: 1) and transcripts reach tens of MB, so this must never
// scan a whole file: 256 KiB comfortably spans the last few messages; if
// no usage block is found in it, the counter simply hides rather than
// escalating to a full read.
const tailBytes = 256 * 1024

// The three usage keys that make up what the model was *sent*.
// output_tokens is deliberately excluded: it is what came back, and it is
// already counted in the next turn's input.
func inputKeysSum(usage map[string]any) int {
	return asInt(usage["input_tokens"]) + asInt(usage["cache_creation_input_tokens"]) + asInt(usage["cache_read_input_tokens"])
}

// ContextLimits maps a needle found in the model id to its context window.
// Opus 5 and Fable 5 are 1M; Haiku 4.5 is 200k, the one entry not 1M, so a
// flat default would render a Haiku session at a fifth of its true
// fullness. The statusline payload carries only a boolean
// (exceeds_200k_tokens), not a number or a limit, so the limit is inferred
// from the model id and can be overridden via userConfig.
var ContextLimits = []struct {
	Needle string
	Limit  int
}{
	{"haiku", 200_000},
	{"sonnet", 1_000_000},
	{"opus", 1_000_000},
	{"fable", 1_000_000},
}

const DefaultContextLimit = 1_000_000

// Payload is the statusLine command's stdin JSON.
type Payload struct {
	SessionID         string     `json:"session_id"`
	TranscriptPath    string     `json:"transcript_path"`
	Model             *ModelInfo `json:"model"`
	Exceeds200kTokens *bool      `json:"exceeds_200k_tokens"`
}

type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

func (p Payload) modelID() string {
	if p.Model == nil {
		return ""
	}
	return p.Model.ID
}

// ScanResult is every transcript-derived fact the status line needs, from
// ONE pass over the tail. This used to be four separate re-reads/re-parses
// of the same 256 KiB tail (one per segment); at refreshInterval 1 that was
// four reads a second forever, worse with every segment added. Callers must
// call ScanTranscript once per refresh and pass the result down.
//
// Subagent turns are skipped throughout (isSidechain): a Task-tool subagent
// runs in its own context and writes its own usage blocks into this same
// transcript. Its window is not this session's window and its cache is not
// this thread's cache, so a small subagent must not read as "plenty of
// room" moments before the main context fills.
type ScanResult struct {
	ContextTokens *int       // newest window size, any model; PostCompact or nil
	NewestModel   string     // model on the newest usage block, whichever model that is
	LastTouch     *time.Time // when CurrentModel last had a turn here
	TTLSeconds    *int       // the TTL CurrentModel last wrote with
	ReadTokens    int        // cache_read_input_tokens on CurrentModel's last turn
	TurnUncached  int        // summed across every step of the turn in progress
}

func readTail(transcriptPath string) (string, bool) {
	if transcriptPath == "" {
		return "", false
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return "", false
	}
	size := info.Size()
	start := int64(0)
	if size > tailBytes {
		start = size - tailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", false
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return "", false
	}
	return strings.ToValidUTF8(string(buf), "�"), true
}

// splitLines mirrors Python's str.splitlines(): no trailing empty element
// for a file ending in "\n", CRLF tolerated.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}

var modelDateSuffix = regexp.MustCompile(`-\d{8}$`)

// normalizeModelID reduces a model id to the part both the statusline
// payload and the transcript agree on, for comparison only, never for
// display (ModelSegment shows the full display_name because "warm" is only
// meaningful alongside *which* model it is warm for).
//
// The payload carries the id the user *selected*, which can be decorated: a
// context variant (claude-opus-5[1m]), a pinned date
// (claude-opus-5-20260101), or a provider prefix
// (us.anthropic.claude-opus-5). message.model in the transcript is the bare
// id the API answered with. Comparing the two literally fails for every
// record, which silently reports a permanent model switch: observed live
// with a 1M-context variant, where every usage block got skipped and the
// line read "new model: full write next turn" on every turn of a session
// actually serving hundreds of thousands of cached-read tokens.
func normalizeModelID(id string) string {
	text := strings.TrimSpace(strings.ToLower(id))
	if i := strings.Index(text, "["); i >= 0 {
		text = text[:i]
	}
	if i := strings.LastIndex(text, "."); i >= 0 {
		text = text[i+1:]
	}
	text = modelDateSuffix.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

func asInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case int:
		return t
	default:
		return 0
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// ScanTranscript is the single pass described on ScanResult.
func ScanTranscript(transcriptPath, currentModel string) ScanResult {
	var facts ScanResult
	tail, ok := readTail(transcriptPath)
	if !ok {
		return facts
	}

	wanted := normalizeModelID(currentModel)
	// Whether the usage blocks being read still belong to the turn in
	// progress. Flips false at the first prompt older than this turn,
	// which is what stops TurnUncached swallowing the whole session.
	withinTurn := true

	lines := splitLines(tail)
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !strings.Contains(line, `"usage"`) &&
			!strings.Contains(line, "isCompactSummary") &&
			!strings.Contains(line, `"promptId"`) {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if asBool(record["isSidechain"]) {
			continue
		}

		// A compaction newer than the newest usage block means the last
		// reading describes a window that no longer exists. The scan
		// continues past it: the cache facts predate the compaction and
		// are still true of the cache, which compaction does not
		// invalidate.
		if asBool(record["isCompactSummary"]) {
			if facts.ContextTokens == nil {
				v := PostCompact
				facts.ContextTokens = &v
			}
			continue
		}

		// Checked *after* isCompactSummary: a compact summary is itself a
		// "user" record. Every tool result is a "user" record too, so the
		// turn boundary is one WITHOUT toolUseResult: an actual prompt.
		if t, _ := record["type"].(string); t == "user" {
			if _, hasToolResult := record["toolUseResult"]; !hasToolResult {
				withinTurn = false
			}
			continue
		}

		message, _ := record["message"].(map[string]any)
		if message == nil {
			continue
		}
		usage, _ := message["usage"].(map[string]any)
		if len(usage) == 0 {
			continue
		}

		total := inputKeysSum(usage)
		model, _ := message["model"].(string)
		if facts.NewestModel == "" {
			facts.NewestModel = model
		}
		if facts.ContextTokens == nil && total != 0 {
			v := total
			facts.ContextTokens = &v
		}

		// Summed across every step of this turn, and across models,
		// because the question is what the turn cost rather than which
		// model spent it.
		if withinTurn {
			uncached := total - asInt(usage["cache_read_input_tokens"])
			if uncached > 0 {
				facts.TurnUncached += uncached
			}
		}

		if wanted != "" && normalizeModelID(model) != wanted {
			continue
		}

		if facts.LastTouch == nil {
			stamp, _ := record["timestamp"].(string)
			if ts, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
				facts.LastTouch = &ts
			}
			facts.ReadTokens = asInt(usage["cache_read_input_tokens"])
		}

		if facts.TTLSeconds == nil {
			if creation, ok := usage["cache_creation"].(map[string]any); ok {
				if asInt(creation["ephemeral_1h_input_tokens"]) > 0 {
					v := OneHourSeconds
					facts.TTLSeconds = &v
				} else if asInt(creation["ephemeral_5m_input_tokens"]) > 0 {
					v := FiveMinSeconds
					facts.TTLSeconds = &v
				}
			}
		}

		// `!withinTurn` joins the condition deliberately: the other three
		// facts come from the newest matching block and are settled
		// almost immediately, so breaking on them alone would stop the
		// scan a step or two into the turn and TurnUncached would report
		// only the tail of it.
		if facts.ContextTokens != nil && facts.LastTouch != nil && facts.TTLSeconds != nil && !withinTurn {
			break
		}
	}

	return facts
}

// ContextTokens is a single-fact adapter over ScanTranscript, for tests and
// any caller wanting one fact. Rendering must call ScanTranscript ONCE and
// pass the result down; reaching for this from Run is how the
// four-reads-a-second problem comes back.
func ContextTokens(transcriptPath string) *int {
	return ScanTranscript(transcriptPath, "").ContextTokens
}

func envBool(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes"
}

func showFullPrice() bool { return envBool("CLAUDE_PLUGIN_OPTION_SHOW_FULL_PRICE") }
func show200kCheck() bool { return envBool("CLAUDE_PLUGIN_OPTION_SHOW_200K_CHECK") }

func displayTZ() *time.Location {
	name := strings.TrimSpace(os.Getenv("CLAUDE_PLUGIN_OPTION_DISPLAY_TZ"))
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func ContextLimit(payload Payload) int {
	if override := strings.TrimSpace(os.Getenv("CLAUDE_PLUGIN_OPTION_CONTEXT_LIMIT")); override != "" && isDigits(override) {
		if n, err := strconv.Atoi(override); err == nil && n > 0 {
			return n
		}
	}
	model := strings.ToLower(payload.modelID())
	for _, cl := range ContextLimits {
		if strings.Contains(model, cl.Needle) {
			return cl.Limit
		}
	}
	return DefaultContextLimit
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Bracket returns (colour, bold) for elapsed time as a fraction of
// ttlSeconds.
func Bracket(elapsed, ttlSeconds float64) (string, bool) {
	frac := 1.0
	if ttlSeconds != 0 {
		frac = elapsed / ttlSeconds
	}
	switch {
	case frac < 0.25:
		return Green, true
	case frac < 0.50:
		return Green, false
	case frac < 0.75:
		return Yellow, true
	case frac < 55.0/60.0:
		return Yellow, false
	case frac < 1.0:
		return Red, true
	default:
		return Red, false
	}
}

// Label is the heading text: the state of the cache, and nothing else. No
// advice, by design; see the package doc.
//
// coldKnown is false only where the reading cannot settle the question:
// with overage monitoring on and the gap past 5m but inside the hour, the
// cache is cold if billing has tipped into overage and warm if it has not,
// so no state word is asserted in that case.
func Label(colour string, bold bool, coldKnown bool) string {
	switch colour {
	case Green:
		return "warm"
	case Yellow:
		return "cooling"
	}
	if bold {
		// The closing stretch before the TTL lapses: elapsed < ttl, so the
		// cache is still valid: "expiring" is what is true, not "cold".
		return "expiring"
	}
	if coldKnown {
		return "cold"
	}
	return ""
}

// StateWord is state only, no advice: what a suppressed label still
// shows (just compacted, or the post-red-reminder latch): the clock and
// colour already report the truth, so blanking the label too would leave a
// bare "cache:" reading as "nothing to report" rather than "cold, and
// deliberately not telling you what to do about it".
//
// Red-bold is not actually expired (elapsed < ttl), so it is still "warm"
// once the urgency cue is suppressed: only unbold red has genuinely
// lapsed.
func StateWord(colour string, bold bool) string {
	switch colour {
	case Green:
		return "warm"
	case Yellow:
		return "cooling"
	}
	if bold {
		return "warm"
	}
	return "cold"
}

func formatDecimalMillions(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
	return fmt.Sprintf("%dk", n/1000)
}

func formatWholeMillions(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%dM", n/1_000_000)
	}
	return fmt.Sprintf("%dk", n/1000)
}

func ContextSegment(payload Payload, scan ScanResult) string {
	if scan.ContextTokens == nil {
		return ""
	}
	if *scan.ContextTokens == PostCompact {
		// Just compacted: the window is small by definition, so green is
		// accurate, and a real figure arrives on the next turn.
		return fmt.Sprintf("  %scontext: just compacted%s", Green, Reset)
	}
	tokens := *scan.ContextTokens
	limit := ContextLimit(payload)
	pct := int(math.Round(100 * float64(tokens) / float64(limit)))
	if pct > 100 {
		pct = 100
	}
	// Green while there is room, amber from 60%, red from 80%, the same
	// direction as the cache clock beside it, where green is "warm" and
	// red is the urgent state. Both go green -> red as the moment to act
	// approaches.
	var colour string
	switch {
	case pct < 60:
		colour = Green
	case pct < 80:
		colour = Yellow
	default:
		colour = Red
	}
	shown := formatDecimalMillions(tokens)
	ceiling := formatWholeMillions(limit)
	return fmt.Sprintf("  %scontext %s/%s %d%%%s", colour, shown, ceiling, pct, Reset)
}

// fullPriceNoticeTokens: below it the uncached remainder is just "your
// message plus my last reply" and saying so every turn is noise. Above it,
// something re-sent a body of text at full price and that is worth a word.
const fullPriceNoticeTokens = 10_000

// FullPriceSegment reports tokens on the last turn that were NOT served
// from cache, when notable. Opt-in via the show_full_price userConfig
// option, off by default, useful to someone watching their own cache
// efficiency, not to everyone this status line ships to.
//
// This began as "cached <n> <pct>%", which was a mistake worth recording.
// In the steady state each turn caches the previous turn's content, so the
// only uncached input is the new message and the previous reply, about 1k
// against a window of 170k. That made the segment a restatement of context
// minus a rounding error, reading 99% for an entire session, while going
// *silent* on a cold start, a model switch and the turn after a
// compaction, because all three have ReadTokens of 0 and the old shape
// returned "" for that: it hid on precisely the three turns that cost
// money. Inverted, it is the complement of everything else on the line.
func FullPriceSegment(scan ScanResult) string {
	if !showFullPrice() {
		return ""
	}
	if scan.TurnUncached < fullPriceNoticeTokens {
		return ""
	}
	shown := formatDecimalMillions(scan.TurnUncached)
	return fmt.Sprintf("  %sturn: %s full price%s", Yellow, shown, Reset)
}

// ElapsedClock renders MM:SS while the TTL is still counting down, h+m once
// it is long past. Under an hour the clock is counting toward something and
// the red zone is scored in minutes, so the seconds earn their place. Past
// an hour the cache is cold on either TTL and plain MM:SS would keep
// counting into "300:00", asking the reader to divide to learn a session
// has been idle five hours.
func ElapsedClock(elapsed float64) string {
	minutes := int(elapsed) / 60
	if minutes >= 60 {
		return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
	}
	seconds := int(elapsed) % 60
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

// show200kCheck gates a temporary calibration segment: cross-checking our
// own token count against the harness's own exceeds_200k_tokens boolean.
// Rendering only on *disagreement* makes this a silent audit: no output
// means our arithmetic agrees with the harness. Opt-in and off by default:
// this only proves anything while someone is deliberately parked near the
// 200k boundary watching for it.
func LimitCheckSegment(payload Payload, scan ScanResult) string {
	if !show200kCheck() {
		return ""
	}
	if payload.Exceeds200kTokens == nil {
		return ""
	}
	if scan.ContextTokens == nil || *scan.ContextTokens == PostCompact {
		return ""
	}
	tokens := *scan.ContextTokens
	if *payload.Exceeds200kTokens == (tokens > 200_000) {
		return ""
	}
	return fmt.Sprintf("  %s200k flag disagrees%s", Red, Reset)
}

// ModelSwitched reports whether the model has changed to one with no cache
// in the visible window. Requires a newest block to compare against: an
// empty tail means "nothing known", which must keep the existing wall-clock
// behaviour rather than assert a switch. A block for the current model
// anywhere in the tail means that model *does* have a cache here, and
// since switching away does not destroy the old model's cache, switching
// back inside the TTL is genuinely cheap and must not read as cold.
func ModelSwitched(facts ScanResult, currentModel string) bool {
	if currentModel == "" || facts.NewestModel == "" {
		return false
	}
	if facts.LastTouch != nil {
		return false
	}
	// Ground truth beats inference. Everything above reasons about cache
	// identity from model *names*; this fact comes from the API's own
	// accounting. A turn that read cache_read_input_tokens > 0
	// demonstrably hit a warm cache, so "full write next turn" is false
	// regardless of what the ids look like.
	if facts.ReadTokens > 0 {
		return false
	}
	return normalizeModelID(facts.NewestModel) != normalizeModelID(currentModel)
}

// LabelSuppressedByCompaction is true while the context counter still
// reads "just compacted": the cache clock has no advice worth giving in
// that state: the elapsed time and expiry still show, only the imperative
// half of Label is dropped.
func LabelSuppressedByCompaction(scan ScanResult) bool {
	return scan.ContextTokens != nil && *scan.ContextTokens == PostCompact
}

// ShortModel is the family and version without the vendor prefix:
// "opus-5", not "claude-opus-5". A trailing-dash slice would give the
// version alone, which names nothing: "5" is true of Opus 5, Sonnet 5 and
// Fable 5 alike, and telling those apart is the entire point.
func ShortModel(modelID string) string {
	if modelID == "" {
		return ""
	}
	if strings.HasPrefix(modelID, "claude-") {
		return modelID[len("claude-"):]
	}
	return modelID
}

// ModelSegment names which model this session is on, shown permanently
// rather than only on a switch turn. Since caches are per-model, "warm" is
// only meaningful alongside the model it is warm *for*, and a session
// resumed hours later gives no other on-screen clue which model is
// answering. Prefers model.display_name (already formatted by the harness)
// over slicing the id.
func ModelSegment(payload Payload) string {
	if payload.Model == nil {
		return ""
	}
	name := strings.TrimSpace(payload.Model.DisplayName)
	if name == "" {
		name = ShortModel(payload.Model.ID)
	}
	if name == "" {
		return ""
	}
	return fmt.Sprintf("%s%s%s  ", Dim, name, Reset)
}

func readPayload(r io.Reader) Payload {
	data, _ := io.ReadAll(r)
	var p Payload
	_ = json.Unmarshal(data, &p)
	return p
}

// Run renders one statusline refresh. home lets tests point STATE_DIR
// somewhere private instead of a real $HOME.
func Run(stdin io.Reader, stdout io.Writer, home string) {
	payload := readPayload(stdin)
	sessionID := state.SanitizeSessionID(payload.SessionID)
	currentModel := payload.modelID()

	// The single read. Everything transcript-derived below comes off this
	// one scan; nothing after this line may re-read the transcript.
	scan := ScanTranscript(payload.TranscriptPath, currentModel)

	// Resolved before the state-file read so the "no messages yet" line
	// carries them too: that is the one moment a fresh session has
	// nothing else on screen.
	model := ModelSegment(payload)
	context := ContextSegment(payload, scan)
	trailer := FullPriceSegment(scan) + context + LimitCheckSegment(payload, scan)

	stateDir := state.Dir(home)
	statePath := filepath.Join(stateDir, sessionID+".json")
	cs, err := state.Read(statePath)
	if err != nil {
		fmt.Fprintf(stdout, "%s%s%scache: no messages yet%s%s\n", model, Yellow, Bold, Reset, trailer)
		return
	}
	last, err := state.ParseTimestamp(cs.LastMessageUTC)
	if err != nil {
		fmt.Fprintf(stdout, "%s%s%scache: no messages yet%s%s\n", model, Yellow, Bold, Reset, trailer)
		return
	}

	now := time.Now().UTC()
	elapsed := now.Sub(last).Seconds()
	elapsedClock := ElapsedClock(elapsed)
	tz := displayTZ()
	planExpiryLocal := last.Add(OneHourSeconds * time.Second).In(tz)
	postRedReminder := cs.PostRedReminder

	// A switch is reported before any clock arithmetic, because the clock
	// cannot describe it: elapsed time since the *old* model's cache was
	// touched says nothing about the new model, which has no cache for
	// this prefix at all. Bold is deliberately withheld: bold red means
	// the urgent closing stretch, and compacting a window with no warm
	// cache behind it saves nothing.
	if ModelSwitched(scan, currentModel) {
		fmt.Fprintf(stdout, "%s%s%scache: cold%s %snew model: full write next turn%s%s\n",
			model, Red, Bold, Reset, Red, Reset, trailer)
		return
	}

	var colour string
	var detailBold bool
	var label, eta string

	if scan.TTLSeconds != nil {
		measuredTTL := *scan.TTLSeconds
		// Measured beats both the flag file and the ambiguity it forces:
		// with the real TTL there is no "warm on plan / cold if overage"
		// to hedge about.
		expiryLocal := last.Add(time.Duration(measuredTTL) * time.Second).In(tz)
		colour, detailBold = Bracket(elapsed, float64(measuredTTL))
		if postRedReminder {
			label = StateWord(colour, detailBold)
		} else {
			label = Label(colour, detailBold, true)
		}
		ttlName := "5m"
		if measuredTTL >= OneHourSeconds {
			ttlName = "1h"
		}
		lead := "cold by"
		if elapsed >= float64(measuredTTL) {
			lead = "since"
		}
		eta = fmt.Sprintf("%s %s (%s TTL)", lead, expiryLocal.Format("15:04"), ttlName)
	} else if state.OverageMonitorOn(home) {
		overageExpiryLocal := last.Add(FiveMinSeconds * time.Second).In(tz)
		colour, detailBold = Bracket(elapsed, FiveMinSeconds)
		bothLapsed := elapsed >= OneHourSeconds
		if colour == Yellow {
			label = "warm on plan / cold if overage"
		} else if postRedReminder {
			label = StateWord(colour, detailBold)
		} else {
			// Cold under the 5m overage TTL says nothing about the plan
			// TTL, so "cold" is only asserted once the gap is past the
			// hour and both have lapsed.
			label = Label(colour, detailBold, bothLapsed)
		}
		lead := "cold by"
		if elapsed >= FiveMinSeconds {
			lead = "since"
		}
		eta = fmt.Sprintf("%s %s ovg / %s plan", lead, overageExpiryLocal.Format("15:04"), planExpiryLocal.Format("15:04"))
	} else {
		colour, detailBold = Bracket(elapsed, OneHourSeconds)
		if postRedReminder {
			label = StateWord(colour, detailBold)
		} else {
			label = Label(colour, detailBold, true)
		}
		lead := "cold by"
		if elapsed >= OneHourSeconds {
			lead = "since"
		}
		eta = fmt.Sprintf("%s %s", lead, planExpiryLocal.Format("15:04"))
	}

	// Drop the imperative, but not the state word, while "just
	// compacted" is still up. Both suppressors do the same thing for the
	// same reason: the cache was just written (or a message was just sent
	// instead of compacting), so no advice about what to do next applies,
	// but the reader can still see whether it's warm.
	if LabelSuppressedByCompaction(scan) {
		label = StateWord(colour, detailBold)
	}

	detailWeight := ""
	if detailBold {
		detailWeight = Bold
	}
	heading := "cache:"
	if label != "" {
		heading = "cache: " + label
	}
	fmt.Fprintf(stdout, "%s%s%s%s%s %s%s%s / %s%s%s\n",
		model, colour, Bold, heading, Reset,
		colour, detailWeight, elapsedClock, eta, Reset, trailer)
}
