// Command cache-ttl-tracker is the whole plugin:
//
//	track          the PostToolUse/Stop/PreCompact hooks
//	statusline     the statusLine command
//	session-start  the SessionStart hook: the one-time setup notice
//	install        wires the statusline into the user's own settings.json
//
// One binary per platform, all committed, behind the bin/cache-ttl-tracker
// dispatcher, so hooks.json and settings.json each name a single fixed
// path, with no interpreter-name detection (python3/python/py) and no build
// step on the machine that runs it.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/case-forge/cache-ttl-tracker/internal/installer"
	"github.com/case-forge/cache-ttl-tracker/internal/state"
	"github.com/case-forge/cache-ttl-tracker/internal/statusline"
	"github.com/case-forge/cache-ttl-tracker/internal/tracker"
)

const usage = "usage: cache-ttl-tracker <track|statusline|session-start|install>"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		// track and statusline are best-effort by design (a hook must never
		// fail the turn it's attached to, and a statusline must never block
		// on error): an unreadable $HOME degrades to "no state", not a
		// crash. install is the exception: it has nowhere to write, so it
		// reports properly below.
		home = ""
	}

	switch os.Args[1] {
	case "track":
		sessionID, eventName := tracker.ParsePayload(os.Stdin)
		tracker.Run(state.Dir(home), state.OverageFlagPath(home), time.Now().UTC(), sessionID, eventName)

	case "statusline":
		statusline.Run(os.Stdin, os.Stdout, home)

	case "session-start":
		// Best-effort like every other hook entrypoint: it prints the setup
		// notice or, far more often, nothing at all.
		installer.SessionStart(home, binDir(), os.Stdout)

	case "install":
		if home == "" {
			fmt.Fprintln(os.Stderr, "cache-ttl-tracker: cannot locate your home directory, so cannot find settings.json")
			os.Exit(1)
		}
		if err := installer.Run(home, binDir(), os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "cache-ttl-tracker: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "%s, got %q\n", usage, os.Args[1])
		os.Exit(2)
	}
}

// binDir is the directory holding this binary. os.Executable resolves to the
// real per-platform binary even when reached through the dispatcher, because
// the dispatcher execs it, so this is bin/ either way.
func binDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}
