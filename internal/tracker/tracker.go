// Package tracker is the PostToolUse/Stop/PreCompact hook: it records the
// wall-clock time the cache was last plausibly touched, per session, for
// the statusline's freshness estimate.
//
// Anthropic's prompt-cache state lives entirely server-side: no API
// exposes whether a conversation's cache is currently warm or cold. This
// can only approximate freshness from elapsed time since the cache was
// last touched. The cache is actually touched on every API request,
// including each tool-call round-trip within a single turn, not just once
// when the turn finally ends. Wiring this onto PostToolUse too (in addition
// to Stop) fixes a long tool-heavy turn showing a misleadingly large
// elapsed/cold-by figure even though the cache was being refreshed
// continuously throughout it.
//
// Also latches the one-message "compact now" reminder: if the gap between
// the last two *turns* (Stop-to-Stop, not tool-call-to-tool-call) sat in
// the red zone and nobody ran /compact, the next Stop sets PostRedReminder
// so the statusline keeps nudging through the next message too, clearing on
// the Stop after that. This latch logic only runs on Stop; a PostToolUse
// firing mid-turn just refreshes the timestamp and leaves the latch
// untouched: re-evaluating it on every tool call would collapse "once per
// turn" into "once per tool call" and the reminder would flash and clear
// before the user ever saw it between turns.
//
// PreCompact also touches the timestamp, unconditionally clearing the
// reminder rather than evaluating it. Compaction sends the whole transcript
// back to the model: it is itself a cache write, arguably the single
// biggest one a session has. PreCompact fires before the summarisation call
// actually runs, not after, so the reset is an approximation by a few
// seconds, far closer than leaving the pre-compaction timestamp in place,
// which would otherwise let an old red-zone gap latch "compact now" for one
// message immediately after the cache had just been freshly rewritten.
package tracker

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/case-forge/cache-ttl-tracker/internal/state"
)

// HookPayload is the subset of Claude Code's hook stdin JSON this cares
// about: {session_id, transcript_path, hook_event_name, ...}.
type HookPayload struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
}

// ParsePayload is best-effort: malformed or empty stdin yields a "default"
// session id and an empty event name rather than an error, matching a hook
// that must never fail the turn it's attached to.
func ParsePayload(r io.Reader) (sessionID, eventName string) {
	data, _ := io.ReadAll(r)
	var payload HookPayload
	_ = json.Unmarshal(data, &payload)
	return state.SanitizeSessionID(payload.SessionID), payload.HookEventName
}

// Run records the cache touch for one hook invocation. stateDir/home come
// from the caller so tests never touch a real $HOME.
func Run(stateDir, overageFlagPath string, now time.Time, sessionID, eventName string) {
	statePath := filepath.Join(stateDir, sessionID+".json")

	switch eventName {
	case "PostToolUse":
		// Fires many times per turn: only refresh the timestamp (the
		// cache really was touched by this tool call's API request) and
		// leave the red-zone reminder latch exactly as it was. Only Stop
		// (once per turn) is allowed to evaluate/toggle that latch.
		next := false
		if old, err := state.Read(statePath); err == nil {
			next = old.PostRedReminder
		}
		state.Write(stateDir, statePath, state.CacheState{
			LastMessageUTC:  state.FormatTimestamp(now),
			PostRedReminder: next,
		})
		return

	case "PreCompact":
		// Compaction is a cache write in its own right, so reset the clock
		// instead of leaving the pre-compaction timestamp for the next Stop
		// to misread as still-stale. No reminder latch to evaluate:
		// compacting is the response the reminder asks for, so it always
		// clears rather than carrying over.
		state.Write(stateDir, statePath, state.CacheState{
			LastMessageUTC:  state.FormatTimestamp(now),
			PostRedReminder: false,
		})
		return
	}

	nextReminder := false
	if old, err := state.Read(statePath); err == nil {
		if oldLast, err := state.ParseTimestamp(old.LastMessageUTC); err == nil {
			elapsed := now.Sub(oldLast).Seconds()
			ttl := ttlSecondsFor(overageFlagPath)
			if old.PostRedReminder {
				nextReminder = false // this write is the 2nd message, clear it
			} else {
				nextReminder = elapsed/ttl >= state.RedZoneFraction
			}
		}
	}

	state.Write(stateDir, statePath, state.CacheState{
		LastMessageUTC:  state.FormatTimestamp(now),
		PostRedReminder: nextReminder,
	})
}

func ttlSecondsFor(overageFlagPath string) float64 {
	if _, err := os.Stat(overageFlagPath); err == nil {
		return float64(state.FiveMinSeconds)
	}
	return float64(state.OneHourSeconds)
}
