package installer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// QuietFlagPath suppresses the setup notice permanently. Someone who
// installs the plugin for its hooks and genuinely does not want a status
// line must be able to say so once and never hear about it again;
// otherwise a helpful notice becomes a nag, which is worse than silence.
func QuietFlagPath(home string) string {
	return filepath.Join(home, ".claude", "cache-ttl-tracker-quiet")
}

// StatusLineConfigured reports whether this plugin's statusline is wired up.
//
// Matches on the command *mentioning* this plugin rather than on an exact
// path, deliberately: the same working setup is spelled several ways:
// the sh dispatcher, a platform .exe, a `~` path, a resolved symlink, and
// treating any of those as "not configured" would nag somebody who has
// already done the work. A false negative here is a recurring annoyance; a
// false positive just means silence, which is the safer failure.
func StatusLineConfigured(home string) bool {
	raw, err := os.ReadFile(SettingsPath(home))
	if err != nil {
		return false
	}
	var settings struct {
		StatusLine struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		// Unparseable settings is not "unconfigured": it is a different
		// problem, and this is not the code that should complain about it.
		return true
	}
	return strings.Contains(settings.StatusLine.Command, "cache-ttl-tracker")
}

// sessionStartOutput is the JSON shape SessionStart honours: systemMessage
// is shown to the user, hookSpecificOutput.additionalContext is given to
// the model. Both in one object, because a hook's stdout is parsed as JSON
// or as plain text, never both.
type sessionStartOutput struct {
	SystemMessage      string `json:"systemMessage,omitempty"`
	HookSpecificOutput *struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput,omitempty"`
}

// SessionStart emits the one-time setup notice, or nothing at all.
//
// Silence is the normal state and the goal: this speaks only while the
// status line is genuinely not configured, and goes permanently quiet the
// moment it is (or the moment someone opts out). It exists because the
// plugin's entire output is invisible until a settings.json edit that
// Claude Code gives a plugin no way to perform for itself, so somebody
// installing from a marketplace, who never reads a README, would otherwise
// see nothing at all and reasonably conclude the plugin is broken.
func SessionStart(home, binDir string, stdout io.Writer) {
	if home == "" {
		return
	}
	if _, err := os.Stat(QuietFlagPath(home)); err == nil {
		return
	}
	if StatusLineConfigured(home) {
		return
	}

	// The binary alone: the install instruction takes its own subcommand,
	// and reusing StatusLineCommand here once produced "... statusline install".
	binary := BinaryPath(binDir)

	out := sessionStartOutput{
		SystemMessage: fmt.Sprintf(
			"cache-ttl-tracker is loaded, but its status line isn't set up yet, so it currently shows nothing.\n"+
				"Fix it in one command:  %s install\n"+
				"Or just ask me to set up the cache-ttl-tracker status line.\n"+
				"Don't want one? Run:  touch %s",
			binary, QuietFlagPath(home)),
	}
	out.HookSpecificOutput = &struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	}{
		HookEventName: "SessionStart",
		AdditionalContext: fmt.Sprintf(
			"The cache-ttl-tracker plugin is installed but its status line is not configured, "+
				"so its output is invisible to the user. Claude Code plugins cannot register a status "+
				"line themselves; it requires an entry in the user's own settings.json. "+
				"If the user asks you to set it up (or asks why the plugin shows nothing), run:\n"+
				"    %s install\n"+
				"That command edits ~/.claude/settings.json safely: it backs the file up first, preserves "+
				"the ordering of every other key, re-parses before writing, and refuses to overwrite a "+
				"different status line that is already configured. Do not hand-edit the file instead. "+
				"If the user says they do not want a status line, run `touch %s` to silence this notice "+
				"permanently. Do not raise this topic unprompted beyond relaying the notice once.",
			binary, QuietFlagPath(home)),
	}

	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	fmt.Fprintln(stdout, string(data))
}
