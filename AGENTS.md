# AGENTS.md

Guidance for coding agents and contributors working on cache-ttl-tracker.

## What this is

A Claude Code plugin written in Go (standard library only). Hooks record when
a session's prompt cache was last touched; a status line command renders how
warm it is. It has no settings, no network access and no telemetry. Keep it
that way: a new option needs a strong reason, and anything that reads or
writes outside the paths listed in [SECURITY.md](SECURITY.md) must update that
file in the same change.

## Layout

- `cmd/cache-ttl-tracker/` is the single entry point: `track`, `statusline`,
  `session-start`, `install`.
- `internal/statusline/` is the renderer and the transcript scanner. The
  scanner reads the transcript tail once per refresh; do not add a second read.
- `internal/tracker/`, `internal/state/` are the hooks and the per-session state.
- `internal/installer/` writes the user's `statusLine` entry.
- `hooks/hooks.json` wires the hooks; `bin/cache-ttl-tracker` is the
  dispatcher that downloads the binary for the machine.

## Commands

```sh
make check      # go vet, gofmt check, go test ./...
make build      # a binary for this machine, into bin/
claude plugin validate --strict .
```

Run `make check` before every commit. A change in behaviour needs a test that
fails without it, including the boundary values for any threshold.

## Conventions

- Never commit binaries. Releases build them in CI when a `v*` tag is pushed.
- The statusline runs every second. No work that scales with transcript size.
- Colour carries a second signal at most; the text must say the same thing.
- Update the README and `CHANGELOG.md` in the same change as the behaviour.
- Bump `version` in `.claude-plugin/plugin.json` for any released change;
  Claude Code uses it to decide whether an installed plugin is out of date.
- British English in prose.
