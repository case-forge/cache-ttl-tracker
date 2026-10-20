# Changelog

## 1.2.0

The status line now needs no settings.

- **Rewrites replace the full-price segment.** When a request re-wrote most of
  its context (under a tenth read from cache, over 50K tokens), the status
  line shows `$` to `$$$$$`, one per 200K tokens, with the amount: `$$$ 412k`.
  It stays up for the turn it happened in, and a first request or a
  compaction counts too. It is on for everyone and has no setting, replacing
  the opt-in `show_full_price` segment, which fired on a large share of ordinary turns.
  See [Rewrites](README.md#rewrites).
- **No options.** `display_tz`, `context_limit`, `show_full_price` and
  `show_200k_check` are gone, and so is the install-time prompt for them. The
  clock follows the machine's time zone (set `TZ` to show another), and the
  window comes from Claude Code. A leftover `pluginConfigs` entry for any of
  them is ignored.
- **The 200k calibration check is removed.** It compared our input-side count
  with a flag that includes output tokens, so the two could disagree without
  either being wrong.
- **The per-model context table is removed.** The window is the size Claude
  Code reports. If none is reported, the segment shows the token count with
  no percentage instead of a guessed one.

## Unreleased

Follows Claude Code's new behaviour: idle compaction (2.1.299), and the
cache being refreshed the moment a prompt is sent.

- **Idle compaction notice.** When Claude Code 2.1.299 or newer is running,
  `idleCompaction` is not switched off, the cache is on the one-hour TTL and
  the context is at or over the floor (default 200K tokens), the clock counts
  to 54 minutes instead of 60, every colour arrives 10% sooner, and the red
  stretch reads `compacting in Nm` instead of `expiring`. Below the floor, or
  with the feature off or absent, the ordinary clock is unchanged. See
  [Idle compaction](README.md#idle-compaction).
- **The clock starts when a prompt is sent.** A new `UserPromptSubmit` hook
  records the submit time. Previously the clock started at the first tool
  call or turn end, which is later than the request that refreshes the cache,
  and an interrupted turn (which fires no `Stop`) never restarted it at all.
- **A compaction restarts the clock even if no hook saw it.** The status line
  now also reads the compaction summary's own timestamp from the transcript.
- The status line reads `idleCompaction` and the idle-compaction floor from
  settings files (see SECURITY.md).
- Removed the Roadmap section (Zellij tab titling, Telegram alerting and
  manual-wait detection), which is no longer planned.
