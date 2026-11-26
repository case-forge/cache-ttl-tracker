package statusline

import (
	"strings"
	"time"
)

// A usage-limit pause, as Claude Code records it in the transcript. When the
// plan's five-hour or weekly allowance runs out, every request is refused
// before it reaches the model: Claude Code writes a placeholder assistant
// record (model "<synthetic>", all-zero usage) carrying
// "apiError": "usage_limit_reached" and, under apiErrorParams.rate_limit_info,
// which limit it was and when it resets.
//
// A refused request reads nothing from the cache and writes nothing to it.
// The hooks cannot know that: UserPromptSubmit fires before the request is
// sent, and Stop fires when the refusal ends the turn, so both record a touch
// that never happened. Left alone, a message typed during a pause shows the
// cache as warm while it is in fact cooling or cold, and the first request
// after the pause re-writes the whole context at the full price.
//
// So while the newest response in the transcript is a refusal, the clock runs
// from the last response that was actually served, whatever the hooks say.
// This is read from the same single tail scan as everything else.
//
// The record's shape is Claude Code's, not an API contract. If it changes,
// no refusal is recognised and the clock behaves exactly as it did before.
const (
	usageLimitAPIError = "usage_limit_reached"
	syntheticModel     = "<synthetic>"
	// limitGrace is how long after a refusal a recorded touch is still taken
	// to be that refusal's own turn ending (the Stop hook), not a new request.
	limitGrace = 10 * time.Second
)

// LimitPause is the newest run of usage-limit refusals in the transcript
// tail, present only when no served response is newer than it.
type LimitPause struct {
	RefusedAt      time.Time  // newest refusal
	FirstRefusedAt time.Time  // oldest refusal of the same run that the tail holds
	ResetsAt       *time.Time // when the limit lifts, if the record says
	Kind           string     // rate_limit_info.rateLimitType, e.g. "five_hour"
	ServedBefore   *time.Time // newest served response older than the run
}

// limitRefusal reads one transcript record as a usage-limit refusal.
func limitRefusal(record map[string]any) (LimitPause, bool) {
	if apiError, _ := record["apiError"].(string); apiError != usageLimitAPIError {
		return LimitPause{}, false
	}
	stamp, _ := record["timestamp"].(string)
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return LimitPause{}, false
	}
	pause := LimitPause{RefusedAt: at, FirstRefusedAt: at}
	params, _ := record["apiErrorParams"].(map[string]any)
	info, _ := params["rate_limit_info"].(map[string]any)
	pause.Kind, _ = info["rateLimitType"].(string)
	if secs := asInt(info["resetsAt"]); secs > 0 {
		resets := time.Unix(int64(secs), 0).UTC()
		pause.ResetsAt = &resets
	}
	return pause, true
}

// Active returns the pause if it still governs the clock, nil once a request
// newer than the refusal is taken to have been served. touch is the newest
// touch the hooks recorded.
//
// A touch within limitGrace of the refusal is the refused turn itself. A
// later one is a new request: with a known reset time it counts once that
// time has passed (before it, the request is about to be refused too, and the
// refusal lands in the transcript a moment later); with none, once it has
// stood for limitGrace without a refusal following it.
func (p *LimitPause) Active(touch, now time.Time) *LimitPause {
	if p == nil {
		return nil
	}
	if !touch.After(p.RefusedAt.Add(limitGrace)) {
		return p
	}
	if p.ResetsAt != nil {
		if now.Before(*p.ResetsAt) {
			return p
		}
		return nil
	}
	if now.Sub(touch) < limitGrace {
		return p
	}
	return nil
}

// LastServed is when the cache was last really touched: the newest served
// response before the refusals. When the tail no longer holds one, the first
// refusal is the latest it can have been, unless the hooks' own figure is
// older still.
func (p *LimitPause) LastServed(touch time.Time) time.Time {
	if p.ServedBefore != nil {
		return *p.ServedBefore
	}
	if p.FirstRefusedAt.Before(touch) {
		return p.FirstRefusedAt
	}
	return touch
}

// Lifted reports whether the limit's own reset time has passed. An unknown
// reset time is never "lifted": only a served response ends that pause.
func (p *LimitPause) Lifted(now time.Time) bool {
	return p.ResetsAt != nil && !now.Before(*p.ResetsAt)
}

// limitName is the limit in the words Claude Code uses for it.
func limitName(kind string) string {
	switch {
	case kind == "five_hour":
		return "5-hour limit"
	case strings.HasPrefix(kind, "seven_day"):
		return "weekly limit"
	}
	return "usage limit"
}

// Note is the status line's account of the pause: which limit, until when,
// and what became of idle compaction. Empty once the limit has lifted, when
// there is nothing left to say but the true clock.
//
// Idle compaction needs a request of its own to write the summary, and that
// request is refused like any other, so none happens while the limit is in
// force. idleArmed says one would otherwise have been expected; before the
// 54 minutes it is "on hold", after them "skipped".
func (p *LimitPause) Note(now time.Time, tz *time.Location, idleArmed bool, elapsed float64) string {
	if p.Lifted(now) {
		return ""
	}
	note := limitName(p.Kind)
	if p.ResetsAt != nil {
		note += " until " + p.ResetsAt.In(tz).Format("15:04")
	}
	if idleArmed {
		if elapsed < IdleCompactAfterSeconds {
			note += ", idle compaction on hold"
		} else {
			note += ", idle compaction skipped"
		}
	}
	return note
}
