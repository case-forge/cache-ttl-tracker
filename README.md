# cache-ttl-tracker

A Claude Code plugin that shows, live in your status line, whether your next
message will hit a warm prompt cache or trigger an expensive rebuild.

![warm](docs/statusline-warm.png)
![cooling](docs/statusline-cooling.png)
![expiring](docs/statusline-expiring.png)

Left to right: which model you are on, the cache state and its countdown,
and how full the context window is. The three shots above are real output on
Sonnet 5, Opus 5 and Haiku 4.5, showing the cache warm, cooling and about to
expire. Note the last one reads `33k/200k` rather than `/1M`, because Haiku's
window is smaller and the gauge follows the window Claude Code reports.

> [!IMPORTANT]
> **The status line is not shown by default. One command turns it on:**
>
> ```sh
> ~/.claude/skills/cache-ttl-tracker/bin/cache-ttl-tracker install
> ```
>
> Or ask Claude: *"set up the cache-ttl-tracker status line"*.
>
> Windows PowerShell, and why a plugin cannot do this itself:
> [the setup step](#the-status-line-setup-step).

## Why it matters

A cache hit costs 0.1x the base input price. Letting the cache go cold costs
more than full price, because rebuilding it is billed as a cache *write*:
1.25x base on a 5-minute TTL, 2x base on a 1-hour TTL. Staying warm is not a
nice-to-have; going cold is the expensive outcome.

No API reports whether a conversation's cache is currently warm, so this
infers it from elapsed time since the cache was last touched, against the
known TTLs. Where the transcript states the real `cache_creation` figures
outright, those override the inference.

## What it does

- **Hooks** (`UserPromptSubmit`, `PostToolUse`, `Stop`, `PreCompact`) record
  when the cache was last plausibly touched, per session. A prompt counts the
  moment it is submitted, because that is when the request that refreshes the
  cache goes out, whether or not an answer ever arrives.
- **A status line** renders that as `warm` / `cooling` / `expiring` / `cold`
  with a countdown, alongside a context-window gauge and a `$` marker when a
  request re-wrote most of the context (see [Rewrites](#rewrites)).
- **A `SessionStart` notice** tells you the status line needs setting up,
  until it is. Then it goes quiet for good.

No background processes, no network connections of any kind, no telemetry.
See [SECURITY.md](SECURITY.md) for exactly what it reads and writes.

## Install

```sh
git clone https://github.com/case-forge/cache-ttl-tracker.git ~/.claude/skills/cache-ttl-tracker
```

> [!WARNING]
> **On Windows, use PowerShell's `$HOME`, not `~`.** PowerShell does not
> expand `~` when passing an argument to an external program such as
> `git.exe`, so the tilde arrives literally and git creates a directory
> actually *named* `~`. The clone appears to succeed, the plugin never
> loads, and the giveaway is buried in git's own output:
> `Cloning into '~/.claude/skills/cache-ttl-tracker'...` with the tilde
> unexpanded.
>
> ```powershell
> git clone https://github.com/case-forge/cache-ttl-tracker.git "$HOME\.claude\skills\cache-ttl-tracker"
> ```
>
> If you have already done it the other way, the clone is sitting in
> `C:\Users\YOU\~\.claude\skills\cache-ttl-tracker`. Move it rather than
> re-cloning:
>
> ```powershell
> New-Item -ItemType Directory -Force -Path "$HOME\.claude\skills" | Out-Null
> Move-Item "$HOME\~\.claude\skills\cache-ttl-tracker" "$HOME\.claude\skills\cache-ttl-tracker"
> Remove-Item -Recurse "$HOME\~"   # check it is empty first
> ```

That is the whole install, and updating is `git pull`. **Nothing to build:
no Go toolchain, no interpreter.** `bin/cache-ttl-tracker` is a small `sh`
dispatcher that, on first run, downloads the prebuilt binary matching your
machine from this repo's [GitHub Releases](https://github.com/case-forge/cache-ttl-tracker/releases)
and caches it beside itself — every run after the first is a plain local
exec, no network involved. Shipped: Linux amd64/arm64, macOS Intel/Apple
Silicon, Windows amd64/arm64. On anything else, or if the download fails, the
dispatcher exits naming the exact binary it looked for and the `go build`
line that produces it, rather than failing silently.

A newly cloned skills-directory plugin loads on your **next** session, so
start Claude Code again after installing. It then appears as
`cache-ttl-tracker@skills-dir`, and `/reload-plugins` picks up later changes
without a restart. Confirm with `claude plugin list`.

<details>
<summary>Why Go, given it needs no runtime</summary>

The status line runs at `refreshInterval: 1`, once per second per open
session, so its startup cost is paid continuously rather than once. Measured
on one machine with identical input against the Python implementation this
replaced:

| | per invocation |
| --- | --- |
| Python | 89ms |
| Go | 7ms |

That is roughly 8.9% of a CPU core held permanently per session, versus
0.7%. It also removes the `python3 || python || py` fallback the old build
needed to find an interpreter that might not exist, and which failed
silently when none did.
</details>

<details>
<summary>Building it yourself</summary>

Only needed if you are changing the code, or are on a platform we do not
ship. `make build` for your own machine, `make release` to cross-compile all
five from any one machine. Without `make`:

```sh
go build -ldflags="-s -w" -o "bin/cache-ttl-tracker-$(go env GOOS)-$(go env GOARCH)" ./cmd/cache-ttl-tracker
```

Never build to `bin/cache-ttl-tracker` itself. That path is the committed
dispatcher, and overwriting it breaks every other platform.
</details>

## The status line setup step

Run this once:

```sh
~/.claude/skills/cache-ttl-tracker/bin/cache-ttl-tracker install
```

```powershell
# Windows PowerShell, where ~ is not expanded for external programs
& "$HOME\.claude\skills\cache-ttl-tracker\bin\cache-ttl-tracker-windows-amd64.exe" install
```

It adds the `statusLine` entry to your `~/.claude/settings.json`, backing
the file up first, preserving every other key and its ordering, and choosing
the right command for your platform. Run it twice and it reports "already
installed". If you already have a *different* status line configured,
possibly another plugin's, it leaves it alone and prints what to change by
hand, because that is your call rather than this plugin's.

You do not have to find this page to know about it: a `SessionStart` hook
says the same thing once per session until it is configured, telling both
you and Claude how to fix it. It recognises any spelling of a working setup
and goes silent as soon as one exists. If you want the hooks but no status
line, `touch ~/.claude/cache-ttl-tracker-quiet` and it never mentions it
again.

<details>
<summary>Why a plugin cannot register this itself</summary>

Not an oversight here. Claude Code has no such capability. There is no
`statusLine` key in the `plugin.json` manifest, and `claude plugin validate
--strict` reports it as *"Unknown field 'statusLine'. Claude Code ignores it
at load time"* (checked against 2.1.245). A plugin may ship its own root
`settings.json`, but only the `agent` and `subagentStatusLine` keys are
honoured there, and `subagentStatusLine` governs *subagent* status lines
rather than the main session one.

To do it by hand instead:

```json
"statusLine": {
  "type": "command",
  "command": "~/.claude/skills/cache-ttl-tracker/bin/cache-ttl-tracker statusline",
  "refreshInterval": 1
}
```

On Windows, point at the `.exe` rather than the dispatcher, which relies on
Git Bash being the shell:

```json
"statusLine": {
  "type": "command",
  "command": "C:/Users/YOU/.claude/skills/cache-ttl-tracker/bin/cache-ttl-tracker-windows-amd64.exe statusline",
  "refreshInterval": 1
}
```
</details>

## Settings

There are none to set. The window comes from Claude Code, the clock is shown
in your machine's own time zone (run with `TZ=Europe/London`, for example, to
show another; the freshness maths is in UTC regardless), and the idle
compaction behaviour follows Claude Code's own `idleCompaction` switch and
floor. Installing asks you nothing.

Two optional flag files exist, and are checked fresh on every call:
`~/.claude/cache-ttl-tracker-quiet` (stop the setup notice) and
`~/.claude/cache-ttl-overage-monitor.on` (see [Which TTL you are on](#which-ttl-you-are-on)).

## Rewrites

When a request found under a tenth of its context in the cache and the context
is over 50K tokens, most of the window was billed as a cache *write* instead
of being read at a tenth of the price. That happens on a cold start, after a
compaction, on a model switch, and when the cache went cold. The status line
then shows dollar signs and the number of tokens re-written, for the rest of
that turn:

| Shown | Re-written | Colour |
| --- | --- | --- |
| `$ 120k` | up to 200K | green |
| `$$ 300k` | 200K to 400K | yellow |
| `$$$ 412k` | 400K to 600K | yellow |
| `$$$$ 700k` | 600K to 800K | red |
| `$$$$$ 1.24M` | over 800K | red |

Only one rewrite is shown at a time: the largest one in the current turn. A
bigger one later in the same turn replaces it, a smaller one does not, and the
next prompt starts again from nothing. Earlier turns are not kept, so scroll
back through the conversation, not the status line, to see them.

One `$` per 200K tokens, rounded up. The signs show relative size, not a
billed amount: a write costs 1.25x the base input price on a 5-minute TTL and
2x on a 1-hour TTL, and a hit costs 0.1x, so the same tokens cost the same
multiple whatever the model. The colour is never the only signal.

## Idle compaction

Claude Code 2.1.299 added idle compaction: a conversation of 200K+ tokens left
idle for about 54 minutes is compacted before its one-hour cache expires. When
that is going to happen, "expiring" is the wrong word, so the status line
follows it:

- **The clock counts to 54 minutes, not 60.** Every colour change arrives 10%
  sooner. The end of the clock reads `compacts HH:MM (idle compaction)` instead
  of `cold by HH:MM`.
- **From about 49 minutes** the red label reads `compacting in 5m`, counting
  down, in place of `expiring`.
- **After the compaction** the clock restarts green: the window is small and
  freshly summarised.

This is Claude Code behaviour, not an API guarantee, so the notice appears only
while all of these hold, each re-checked on every refresh:

- the running Claude Code is 2.1.299 or newer (read from the status line
  payload, or the transcript if the payload has none; an unknown version counts
  as absent);
- `"idleCompaction": false` is not set in user, project, project-local or
  managed `settings.json`;
- the cache is on the one-hour TTL;
- the context is at or over `CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS` (default
  200000, from the environment or the `env` block of settings). Below it
  nothing is compacted, so the cache still goes cold at the TTL on the
  ordinary `warm` / `cooling` / `expiring` / `cold` clock.

If Anthropic changes or removes the feature, the notice stops appearing and the
ordinary clock is what remains. If the 54 minutes pass and no compaction
follows, the ordinary clock resumes too, rather than guessing.

## Which TTL you are on

Getting this wrong is a trap in exactly one direction: assume 1 hour while
actually on 5 minutes, and the status line reports "warm" for up to 55
minutes after the cache has genuinely gone cold, precisely when a message
would trigger an expensive rebuild.

Three states, of which only two need anything from you:

- **On included usage** (1-hour TTL). The default, nothing to do.
- **In paid overage** (5-minute TTL). Create the flag file below.
- **Measured.** Always on underneath both. The instant the transcript's own
  `cache_creation` figures show a real write, that ground truth overrides
  whichever assumption is set.

```sh
touch ~/.claude/cache-ttl-overage-monitor.on   # 5-minute TTL
```

Delete it to go back to 1-hour estimates. It stays a flag file rather than a
setting on purpose: it is checked fresh on every call, so flipping it
applies to your very next message.

## Status

Treat this as private beta.

The tracking and status-line logic is a Go rewrite of a Python
implementation that saw real daily use, ported behaviour for behaviour, with
an equivalent Go test for every case the old suite covered. The screenshots
above are live output from the current build.

Submitted to the official plugin marketplace and awaiting review.

**Known limitation, and the direction of travel.** Because no API reports
live cache state, this is a heuristic wherever the transcript does not state
the real TTL and cache-creation numbers outright. Leaning further on what
the transcript actually reports, and less on elapsed-time inference, is the
active goal rather than a finished feature.

## Licence

MIT. See [LICENSE](LICENSE). Copyright CaseForge.

Zero third-party dependencies: the Go standard library only, so there is
nothing else to attribute.
