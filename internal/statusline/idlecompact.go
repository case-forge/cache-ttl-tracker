package statusline

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Idle compaction, as Claude Code 2.1.299 describes it: "a 200K+ token
// conversation left idle for about 54 minutes is compacted before its
// one-hour prompt cache expires". It can be switched off with
// "idleCompaction": false in settings.json, and its 200K floor moved with
// CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS.
//
// This is a Claude Code behaviour, not an API guarantee, and Anthropic can
// change or drop it. So nothing here assumes it: the notice appears only
// while all of these hold, and every one of them is re-read on each refresh:
//
//   - the running Claude Code is new enough to have it (version known and
//     >= IdleCompactMinVersion; an unknown version counts as "not there");
//   - settings do not turn it off (absent means on, which is the default);
//   - the TTL in force is the one-hour one (on a 5-minute TTL the cache is
//     long gone before 54 minutes);
//   - the context is at or over the floor (under it, the cache simply goes
//     cold at the TTL as it always did).
//
// If any fails, the clock behaves exactly as it did before this existed.
const (
	IdleCompactMinVersion = "2.1.299"
	// IdleCompactAfterSeconds is the idle time at which compaction fires.
	IdleCompactAfterSeconds   = 54 * 60
	IdleCompactDefaultMinToks = 200_000
	idleCompactMinTokensEnv   = "CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS"
)

// IdleCompaction is what could be established about the feature.
type IdleCompaction struct {
	Available bool // the running Claude Code has it
	Enabled   bool // settings do not turn it off
	MinTokens int  // context floor below which it does nothing
}

// versionAtLeast compares dotted numeric versions. Anything after a
// non-numeric part ("2.1.299-beta") is ignored; an unparseable version is
// never "at least" anything.
func versionAtLeast(have, want string) bool {
	parse := func(v string) ([]int, bool) {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		if v == "" {
			return nil, false
		}
		var out []int
		for _, part := range strings.Split(v, ".") {
			digits := part
			if i := strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
				digits = part[:i]
			}
			n, err := strconv.Atoi(digits)
			if err != nil {
				return nil, false
			}
			out = append(out, n)
		}
		return out, true
	}
	h, ok := parse(have)
	if !ok {
		return false
	}
	w, _ := parse(want)
	for i := 0; i < len(w); i++ {
		hv := 0
		if i < len(h) {
			hv = h[i]
		}
		if hv != w[i] {
			return hv > w[i]
		}
	}
	return true
}

// settingsFiles lists every settings file that can set the options, lowest
// precedence first: user, project, project-local, then the machine-wide
// managed file, which wins over all of them.
func settingsFiles(home, cwd string) []string {
	files := []string{filepath.Join(home, ".claude", "settings.json")}
	if cwd != "" {
		files = append(files,
			filepath.Join(cwd, ".claude", "settings.json"),
			filepath.Join(cwd, ".claude", "settings.local.json"))
	}
	switch runtime.GOOS {
	case "darwin":
		files = append(files, "/Library/Application Support/ClaudeCode/managed-settings.json")
	case "windows":
		files = append(files, `C:\Program Files\ClaudeCode\managed-settings.json`)
	default:
		files = append(files, "/etc/claude-code/managed-settings.json")
	}
	return files
}

// DetectIdleCompaction answers "is it there, and is it on". Best-effort: an
// unreadable or malformed settings file is skipped, never an error.
func DetectIdleCompaction(version, home, cwd string) IdleCompaction {
	ic := IdleCompaction{
		Available: versionAtLeast(version, IdleCompactMinVersion),
		Enabled:   true,
		MinTokens: IdleCompactDefaultMinToks,
	}
	envMin := strings.TrimSpace(os.Getenv(idleCompactMinTokensEnv))
	if n, err := strconv.Atoi(envMin); err != nil || n <= 0 {
		envMin = "" // unusable: let the settings env block have its say
	}
	for _, path := range settingsFiles(home, cwd) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// Decodes only these two keys. settings.json can hold tokens in its
		// env block; naming the one field wanted means nothing else is ever
		// held, let alone logged.
		var settings struct {
			IdleCompaction *bool `json:"idleCompaction"`
			Env            struct {
				MinTokens json.RawMessage `json:"CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS"`
			} `json:"env"`
		}
		if json.Unmarshal(data, &settings) != nil {
			continue
		}
		if settings.IdleCompaction != nil {
			ic.Enabled = *settings.IdleCompaction
		}
		// The settings env block is applied to Claude Code's own process, so
		// it normally reaches us through the real environment too; reading
		// it here covers a statusline started some other way. The real
		// environment wins when both are set.
		// Accepts "100000" or 100000: either is how a person would write it.
		if v := strings.Trim(strings.TrimSpace(string(settings.Env.MinTokens)), `"`); v != "" && envMin == "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				ic.MinTokens = n
			}
		}
	}
	if envMin != "" {
		ic.MinTokens, _ = strconv.Atoi(envMin)
	}
	return ic
}

// Armed reports whether an idle compaction is expected for this session
// right now. contextTokens is nil when the window size is unknown, which
// counts as "not armed": better to keep the plain cache clock than to
// announce a compaction that may not happen.
func (ic IdleCompaction) Armed(contextTokens *int, ttlSeconds int) bool {
	if !ic.Available || !ic.Enabled || ttlSeconds < OneHourSeconds {
		return false
	}
	if contextTokens == nil || *contextTokens == PostCompact {
		return false
	}
	return *contextTokens >= ic.MinTokens
}

// IdleCompactView is the clock while an idle compaction is expected. It is
// the ordinary bracket scaled to the 54 minutes the cache actually has left
// to live, so every colour arrives 10% sooner than on the plain one-hour
// clock, and red (49m30s onward) reads "compacting in Nm" in place of
// "expiring". Red here is a compaction, not an expiry: the next message
// after it lands on a small, freshly summarised window.
//
// Only valid while elapsed < IdleCompactAfterSeconds. Past that, either the
// compaction happened (the hook or transcript restarts the clock) or it did
// not, and the plain one-hour clock tells the truth.
func IdleCompactView(elapsed float64, last time.Time, tz *time.Location, quiet bool) (colour string, bold bool, label, eta string) {
	colour, bold = Bracket(elapsed, IdleCompactAfterSeconds)
	switch {
	case quiet:
		label = StateWord(colour, bold)
	case colour == Red && bold:
		label = compactingIn(IdleCompactAfterSeconds - elapsed)
	default:
		label = Label(colour, bold, true)
	}
	at := last.Add(IdleCompactAfterSeconds * time.Second).In(tz)
	return colour, bold, label, fmt.Sprintf("compacts %s (idle compaction)", at.Format("15:04"))
}

func compactingIn(remaining float64) string {
	mins := int(math.Ceil(remaining / 60))
	if mins < 1 {
		return "compacting now"
	}
	return fmt.Sprintf("compacting in %dm", mins)
}
