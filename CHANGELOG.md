# Changelog

## 1.4.0

- **No TTL named until Claude Code reports one.** Without `prompt_cache.ttl`
  the status line no longer says `(1h TTL assumed)` or forecasts a `cold by`
  time from the guess. The clock and colours still run on the
  one-hour default, and `since HH:MM` still appears once the hour has passed;
  the TTL and the forecast return with the next response.
- **After a compaction, how long ago.** The cache segment now reads
  `cache: compacted 12:34 / since 14:32`, in green with a bold heading, and the
  age keeps running instead of showing only the time. It shares its clock with
  the cold state, which now also moves to days, weeks, months and years
  (`1d0h00m` after 23h59m, `1w1d8h05m`, up to `1y2mo3w4d5h06m`) in place of
  `200h05m`.

## 1.3.0

The TTL comes from Claude Code, and a compaction no longer starts a clock.

- **The TTL is what Claude Code reports.** The status line reads
  `prompt_cache.ttl` from the payload (Claude Code 2.1.251 and later). The
  transcript's `cache_creation` figures are no longer used to work it out. A
  payload without one shows the one-hour default, labelled `(1h TTL assumed)`.
  The `cache-ttl-overage-monitor.on` flag still decides when there is none.
- **A 5-minute scale of its own.** On a 5-minute TTL the colours change by the
  minute: green to 2:00, yellow to 4:00, red after. The fractional scale
  gave only the last 25 seconds as red. See [The clock](README.md#the-clock).
- **After a compaction, the time instead of a clock.** Until your next message
  the cache segment reads `cache: compacted at 14:32` and nothing else: the
  separate `context: just compacted` note is gone. The clock used to restart
  at the compaction and run through yellow and red over a cache that held only
  the replaced conversation. See [After a compaction](README.md#after-a-compaction).
- **The binary follows the plugin version.** The dispatcher fetches the
  release named by `version` in `plugin.json` and records it, so a `git pull`
  that moves the version on now brings the matching binary instead of leaving
  the first one downloaded. A failed download keeps the existing binary and is
  retried at most hourly. `make build` records the version so local builds are
  left alone.
- **Merging publishes the release.** The release workflow now runs on a push
  to `main` as well as on a `v*` tag: if the version in `plugin.json` has no
  release, it builds the six binaries and publishes `v<version>`. A tag
  pushed by hand still works, and must match the version.
- SECURITY.md now describes the dispatcher's downloads, which it did not.

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

Also in 1.2.0, following Claude Code's new behaviour: idle compaction (2.1.299), and the
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
