# Changelog

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
