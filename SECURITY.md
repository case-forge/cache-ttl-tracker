# Security

## Reporting a vulnerability

Please report security issues privately rather than opening a public issue:
use GitHub's [private vulnerability
reporting](https://github.com/case-forge/cache-ttl-tracker/security/advisories/new)
on this repository.

Expect an acknowledgement within a few days. This is a small project
maintained alongside other work, so please allow reasonable time for a fix
before disclosing publicly.

## What this plugin touches

Useful context when judging whether something is a security issue.

**It makes no network connections of any kind.** There is no telemetry, no
update check, and no outbound request anywhere in the code. It has zero
third-party dependencies: the Go standard library only.

**Files it reads**

| Path | Why |
| --- | --- |
| The session transcript named in the hook payload | Reads the last 256 KiB only, for token counts and cache metadata. Contents are never copied, stored, or transmitted. |
| `~/.claude/settings.json` | To detect whether the status line is configured, and to write that entry when you run `install`. |
| `~/.claude/cache-ttl-track-core/<session>.json` | Its own cache-touch timestamps. |
| `~/.claude/cache-ttl-overage-monitor.on`, `~/.claude/cache-ttl-tracker-quiet` | Presence-only flag files. |

**Files it writes**

- Its own state under `~/.claude/cache-ttl-track-core/`, created `0755`,
  files `0644`, containing only a timestamp and a boolean.
- `~/.claude/settings.json`, **only** when you explicitly run
  `cache-ttl-tracker install`. It backs the file up to `settings.json.bak`
  first, re-parses the result before writing, and refuses to touch a file it
  cannot parse. If it creates the file it uses `0600`.

**What it deliberately does not do:** read your prompts or responses beyond
the token accounting above, execute anything from the transcript, write
outside `~/.claude/`, or run any background process.

## Notes for reviewers

- Session ids from hook payloads are sanitised (`[^A-Za-z0-9_-]` stripped)
  before being used in a filename, so a hostile id cannot escape the state
  directory. Covered by a test.
- Hook entrypoints are best-effort and never fail a turn, which means errors
  are swallowed by design. That is deliberate, not an oversight.
- `settings.json` may hold API tokens. If you find any path where this
  project logs, copies, or transmits its contents, that is a bug worth
  reporting privately.

## Scope

Reports about the security of Claude Code itself, rather than this plugin,
should go to Anthropic.
