# AGENTS.md

Guidance for coding agents and contributors working on cache-ttl-tracker.

## What this is

A Claude Code plugin written in Go (standard library only). Hooks record when
a session's prompt cache was last touched; a status line command renders how
warm it is. It has no settings and no telemetry, and the Go program makes no
network connections (only the dispatcher script downloads release binaries).
Keep it that way: a new option needs a strong reason, and anything that reads or
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
  dispatcher that downloads the binary for the machine, and re-fetches it
  when the `version` in `plugin.json` moves on. `scripts/test-dispatcher.sh`
  tests it against a folder of fake releases.

## Commands

```sh
make check      # go vet, gofmt check, go test ./..., the dispatcher test
make build      # a binary for this machine, into bin/
claude plugin validate --strict .
```

Run `make check` before every commit. A change in behaviour needs a test that
fails without it, including the boundary values for any threshold.

## Conventions

- Never commit binaries. CI builds them and publishes the release when the
  version in `plugin.json` has no release yet: the push to `main` that merges a
  version bump does it, so merging a version bump is publishing. A `v*` tag
  pushed by hand does the same, and must match the version.
- The statusline runs every second. No work that scales with transcript size.
- Colour carries a second signal at most; the text must say the same thing.
- Update the README and `CHANGELOG.md` in the same change as the behaviour.
- Bump `version` in `.claude-plugin/plugin.json` for any released change;
  Claude Code uses it to decide whether an installed plugin is out of date.
  The dispatcher fetches the release named `v<version>`, so the tag and the
  version must match.
- The TTL comes only from the payload's `prompt_cache.ttl`. Do not infer it
  from the transcript.
- British English in prose.
