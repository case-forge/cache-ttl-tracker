package installer

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The notice's whole design goal is silence. These prove it stays quiet in
// every state except the one it exists for.

func TestItSaysNothingOnceTheStatusLineIsConfigured(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, `{
  "statusLine": {
    "type": "command",
    "command": "/anywhere/bin/cache-ttl-tracker statusline",
    "refreshInterval": 1
  }
}`)

	var out bytes.Buffer
	SessionStart(home, "/bin", &out)

	if out.Len() != 0 {
		t.Errorf("should be silent once configured, got: %s", out.String())
	}
}

func TestAnySpellingOfTheCommandCountsAsConfigured(t *testing.T) {
	// The same working setup is written several ways. Treating any of them
	// as unconfigured would nag someone who has already done the work.
	for _, command := range []string{
		"/home/me/.claude/skills/cache-ttl-tracker/bin/cache-ttl-tracker statusline",
		"~/.claude/skills/cache-ttl-tracker/bin/cache-ttl-tracker statusline",
		"C:/Users/junio/.claude/skills/cache-ttl-tracker/bin/cache-ttl-tracker-windows-amd64.exe statusline",
		"/opt/plugins/cache-ttl-tracker/bin/cache-ttl-tracker-linux-arm64 statusline",
	} {
		home := t.TempDir()
		blob, _ := json.Marshal(map[string]any{
			"statusLine": map[string]any{"type": "command", "command": command},
		})
		writeSettings(t, home, string(blob))

		if !StatusLineConfigured(home) {
			t.Errorf("not recognised as configured: %s", command)
		}
	}
}

func TestSomebodyElsesStatusLineIsNotOurs(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, `{
  "statusLine": {"type": "command", "command": "some-other-tool --render"}
}`)

	if StatusLineConfigured(home) {
		t.Error("another tool's statusline must not count as ours")
	}

	var out bytes.Buffer
	SessionStart(home, "/bin", &out)
	if out.Len() == 0 {
		t.Error("should still offer, since our own output is invisible")
	}
}

func TestTheQuietFlagSilencesItPermanently(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, `{"model": "sonnet"}`)
	if err := os.WriteFile(QuietFlagPath(home), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	SessionStart(home, "/bin", &out)

	if out.Len() != 0 {
		t.Errorf("quiet flag must silence it, got: %s", out.String())
	}
}

func TestUnparseableSettingsAreNotTreatedAsUnconfigured(t *testing.T) {
	// A broken settings.json is a different problem, and this is not the
	// code that should complain about it.
	home := t.TempDir()
	writeSettings(t, home, `{"model": "sonnet",,,}`)

	if !StatusLineConfigured(home) {
		t.Error("unparseable settings should not trigger the notice")
	}
}

func TestItSpeaksWhenGenuinelyUnconfigured(t *testing.T) {
	home := t.TempDir()
	writeSettings(t, home, `{"model": "sonnet"}`)

	var out bytes.Buffer
	SessionStart(home, "/plugins/bin", &out)

	if out.Len() == 0 {
		t.Fatal("expected a notice")
	}

	// Presence proof: without this, every assertion above would pass on a
	// function that always returned silently.
	var got sessionStartOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("hook output is not valid JSON: %v\n%s", err, out.String())
	}
	if got.SystemMessage == "" {
		t.Error("no user-facing systemMessage")
	}
	if got.HookSpecificOutput == nil || got.HookSpecificOutput.AdditionalContext == "" {
		t.Fatal("no model-facing additionalContext")
	}
	if got.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("wrong hookEventName: %q", got.HookSpecificOutput.HookEventName)
	}

	// Both audiences need the actual fix, not a vague complaint.
	if !strings.Contains(got.SystemMessage, "install") {
		t.Error("systemMessage should name the install command")
	}
	if !strings.Contains(got.HookSpecificOutput.AdditionalContext, "install") {
		t.Error("additionalContext should tell Claude how to fix it")
	}
	// And a way out, so it can never become a permanent nag.
	if !strings.Contains(got.SystemMessage, QuietFlagPath(home)) {
		t.Error("systemMessage should say how to silence it")
	}
}

func TestTheInstallHintIsARunnableCommand(t *testing.T) {
	// Regression: the hint was built from StatusLineCommand, which already
	// ends in "statusline", producing "...cache-ttl-tracker statusline
	// install", a command that does not exist. Only visible by reading the
	// rendered text, which is why it is asserted here.
	home := t.TempDir()
	writeSettings(t, home, `{"model": "sonnet"}`)

	var out bytes.Buffer
	SessionStart(home, "/plugins/bin", &out)

	var got sessionStartOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{got.SystemMessage, got.HookSpecificOutput.AdditionalContext} {
		if strings.Contains(text, "statusline install") {
			t.Errorf("hint stacks two subcommands into one command:\n%s", text)
		}
		if !strings.Contains(text, "/plugins/bin/cache-ttl-tracker install") {
			t.Errorf("hint is not a runnable install command:\n%s", text)
		}
	}
}

func TestTheOutputIsASingleJSONObjectOnOneLine(t *testing.T) {
	// Claude Code decides how to read hook stdout by its first character:
	// '{' means JSON. Leading whitespace or a second line would break that.
	home := t.TempDir()
	writeSettings(t, home, `{"model": "sonnet"}`)

	var out bytes.Buffer
	SessionStart(home, "/bin", &out)

	text := out.String()
	if !strings.HasPrefix(text, "{") {
		t.Errorf("output must start with '{', got: %q", text[:min(20, len(text))])
	}
	if strings.Count(strings.TrimRight(text, "\n"), "\n") != 0 {
		t.Errorf("output must be one line, got:\n%s", text)
	}
}

func TestNoHomeMeansNoOutput(t *testing.T) {
	var out bytes.Buffer
	SessionStart("", "/bin", &out)
	if out.Len() != 0 {
		t.Errorf("should be silent with no home, got: %s", out.String())
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
