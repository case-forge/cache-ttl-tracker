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
window is smaller and the gauge follows the model.

> [!IMPORTANT]
> **The status line is not shown by default. One command turns it on:**
>
> ```sh
> ~/.claude/skills/cache-ttl-tracker/bin/cache-ttl-tracker install
> ```
>
> ```powershell
> # Windows PowerShell
> & "$HOME\.claude\skills\cache-ttl-tracker\bin\cache-ttl-tracker-windows-amd64.exe" install
> ```
>
> Or simply ask Claude: *"set up the cache-ttl-tracker status line"*.
>
> This is not a missing feature. Claude Code gives plugins no way to
> register a status line, so every plugin that draws one needs an entry in
> your own `settings.json`. The command above writes it for you, safely.
> [Why, and how to do it by hand](#the-status-line-setup-step)

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

- **Hooks** (`PostToolUse`, `Stop`, `PreCompact`) record when the cache was
  last plausibly touched, per session.
- **A status line** renders that as `warm` / `cooling` / `expiring` / `cold`
  with a countdown, alongside a context-window gauge and a notice when a
  model switch or a large uncached turn means full-price tokens.
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

## Options

Display timezone, context-window override, and two optional segments are
declared as this plugin's `userConfig`.

**Interactively:** you are prompted for each option when the plugin is
*enabled*. That timing is worth knowing, because it means changing a value
later appears to require disabling and re-enabling the plugin. It does:
`/reload-plugins` re-reads hooks and servers, but the option prompt only
fires on the transition to enabled. This is how Claude Code's `userConfig`
works rather than anything specific to this plugin.

**As code, which avoids that cycle entirely:**

```json
"pluginConfigs": {
  "cache-ttl-tracker@skills-dir": {
    "options": {
      "display_tz": "Europe/London",
      "show_full_price": true
    }
  }
}
```

This plugin re-reads its options on every invocation, so an edit here takes
effect on the next refresh tick with no restart and no re-enable.

**`pluginConfigs` must go in user settings** (`~/.claude/settings.json`).
Claude Code reads it only from user, `--settings` and managed settings.
Entries in a project's `.claude/settings.json` are ignored outright, so a
project-scope edit looks correct and silently does nothing.

| Option | Default | What it does |
| --- | --- | --- |
| `display_tz` | UTC | IANA zone for the displayed clock. The freshness maths stays in UTC regardless. |
| `context_limit` | auto | Override the per-model context window, if the built-in table is wrong for you. |
| `show_full_price` | off | Show how many tokens the current turn re-sent at full price. Useful when watching cache efficiency. |
| `show_200k_check` | off | Development aid for this plugin's own accuracy. Leave off. |

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
`userConfig` option on purpose: it is checked fresh on every call, so
flipping it applies to your very next message, whereas an option is read
once at enable time.

## Status

Treat this as private beta.

The tracking and status-line logic is a Go rewrite of a Python
implementation that saw real daily use, ported behaviour for behaviour, with
an equivalent Go test for every case the old suite covered. The screenshots
above are live output from the current build.

Not yet submitted to the official plugin marketplace.

**Known limitation, and the direction of travel.** Because no API reports
live cache state, this is a heuristic wherever the transcript does not state
the real TTL and cache-creation numbers outright. Leaning further on what
the transcript actually reports, and less on elapsed-time inference, is the
active goal rather than a finished feature.

## Roadmap

Three features exist in a private sibling project and are deliberately not
here yet. Honest status on each rather than a uniform "coming soon":

- **Zellij tab auto-titling.** Solid: verified against a real multi-session
  setup, race-free. Held back only by deciding how it should be packaged.
- **Telegram alerting** when the cache is about to expire. Working and
  verified end to end.
- **Manual-wait detection**, which alerts when a permission prompt sits
  unanswered. Genuinely unproven, and lands only once it actually works.

The first two will arrive as opt-in options, off by default, never a
surprise on install.

## Licence

MIT. See [LICENSE](LICENSE). Copyright CaseForge.

Zero third-party dependencies: the Go standard library only, so there is
nothing else to attribute.
