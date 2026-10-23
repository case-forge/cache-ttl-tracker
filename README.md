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

A cache hit costs a fraction of the base input price: 0.1x on most models,
and less on some of the newest (0.05x on Opus 5.5 and Sonnet 5.5, 0.025x on
Fable 5.1). Letting the cache go cold costs more than full price, because
rebuilding it is billed as a cache *write*: 1.25x base on a 5-minute TTL, 2x
base on a 1-hour TTL, on every model. Staying warm is not a nice-to-have;
going cold is the expensive outcome. See Anthropic's
[pricing page](https://platform.claude.com/docs/en/about-claude/pricing).

Claude Code reports the cache's TTL (`prompt_cache.ttl` in the status line
payload, 2.1.251 and later) but not a countdown that starts at the moment a
prompt is sent, and not the idle compaction deadline. So this takes the TTL
from Claude Code and infers freshness from the time since the cache was last
touched. Where the payload carries no TTL (an older Claude Code, or before the
first response) it shows the one-hour default and says so: `(1h TTL assumed)`.

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

No background processes and no telemetry. The tracker itself makes no network
connections; only the install dispatcher downloads release binaries, from this
repo's GitHub Releases. See [SECURITY.md](SECURITY.md) for exactly what it
reads, writes and fetches.

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
dispatcher that downloads the prebuilt binary matching your machine from this
repo's [GitHub Releases](https://github.com/case-forge/cache-ttl-tracker/releases)
and caches it beside itself, so every run after that is a plain local exec
with no network involved. It fetches the release named by the `version` in
`.claude-plugin/plugin.json`, so after a `git pull` or a plugin update that
moves the version on, the next run fetches the matching binary once and
records which version it is. If that download fails, the binary you already
have keeps running and the fetch is tried again at most hourly. Shipped: Linux
amd64/arm64, macOS Intel/Apple Silicon, Windows amd64/arm64. On anything else,
or if there is no binary and the download fails, the dispatcher exits naming
the exact binary it looked for and the `go build` line that produces it,
rather than failing silently.

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
six from any one machine. `make build` also records the plugin version beside
the binary, so the dispatcher treats your build as current and does not
replace it with a release download. Without `make` (the dispatcher will then
fetch the release for the plugin version over your build, unless you write
the version to `bin/<binary>.version` yourself):

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

## The clock

The colour follows how much of the TTL has passed. Bold marks the fresher
half of each colour. The words are `warm`, `cooling`, `expiring` and `cold`.

| Colour | 1-hour TTL | 5-minute TTL |
| --- | --- | --- |
| Green | 0 to 30 min | 0 to 2 min |
| Yellow | 30 to 55 min | 2 to 4 min |
| Red, bold (`expiring`) | 55 to 60 min | 4 to 5 min |
| Red (`cold`) | 60 min and over | 5 min and over |

The 5-minute TTL has its own scale, one band per minute, because the same
fractions of five minutes would leave only a few seconds of warning.

## After a compaction

Until your next message the cache segment reads `cache: compacted at 14:32`
(your machine's time) with no clock. A compaction replaces the conversation
with a short summary, so the window is small and cheap to rebuild; the cache
still warm behind it holds the conversation that was just replaced, so a clock
counting it down would only look like a warning with nothing to act on. The
next response brings the ordinary clock back.

## Rewrites

When a request found under a tenth of its context in the cache and the context
is over 50K tokens, most of the window was billed as a cache *write* instead
of being read at a fraction of the price. That happens on a cold start, after a
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

One `$` per 200K tokens, rounded up. The signs show how many tokens were
re-written, not a billed amount: what a token costs depends on the model and
whether the cache is on the 5-minute or 1-hour TTL (writes cost 1.25x or 2x
base input on every model), so the same marker is worth more on a pricier
model. It works on any window: a 200K-window model can reach at most `$`, and
a 1M-window model up to `$$$$$`. The colour is never the only signal.

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
- **After the compaction** the clock gives way to `compacted at HH:MM` until
  your next message; see [After a compaction](#after-a-compaction).

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
- **Measured.** Always on underneath both. The TTL Claude Code reports in
  `prompt_cache.ttl` overrides whichever assumption is set. Without it the
  flag file below decides, and with no flag file the one-hour default is
  shown, labelled `(1h TTL assumed)`.

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

**Known limitation.** The TTL is measured, but the clock is still elapsed
time since the last prompt, tool call or turn end. Claude Code's
`prompt_cache.expires_at` and `warm` are not used: `expires_at` is computed
from the last response rather than from the prompt that refreshed the cache,
and neither accounts for idle compaction.

## Licence

MIT. See [LICENSE](LICENSE). Copyright CaseForge.

Zero third-party dependencies: the Go standard library only, so there is
nothing else to attribute.
