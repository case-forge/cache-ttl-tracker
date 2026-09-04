package installer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSettings(t *testing.T, home, content string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestItInsertsWithoutDisturbingAnythingElse(t *testing.T) {
	home := t.TempDir()
	original := `{
  "env": {
    "SECRET_TOKEN": "keep-me"
  },
  "model": "sonnet",
  "enabledPlugins": {
    "something@else": true
  }
}
`
	path := writeSettings(t, home, original)

	var out bytes.Buffer
	if err := Run(home, "/plugins/cache-ttl-tracker/bin", &out); err != nil {
		t.Fatal(err)
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(updated, &got); err != nil {
		t.Fatalf("wrote invalid JSON: %v\n%s", err, updated)
	}

	// The point of the textual insert: every pre-existing key survives, with
	// its value intact.
	env, _ := got["env"].(map[string]any)
	if env["SECRET_TOKEN"] != "keep-me" {
		t.Errorf("clobbered an unrelated key: %v", got["env"])
	}
	if got["model"] != "sonnet" {
		t.Errorf("clobbered model: %v", got["model"])
	}
	if _, ok := got["enabledPlugins"]; !ok {
		t.Error("dropped enabledPlugins")
	}
	if _, ok := got["statusLine"]; !ok {
		t.Fatal("did not add statusLine")
	}
}

func TestItBacksUpBeforeWriting(t *testing.T) {
	home := t.TempDir()
	original := `{"model": "sonnet"}`
	path := writeSettings(t, home, original)

	var out bytes.Buffer
	if err := Run(home, "/bin", &out); err != nil {
		t.Fatal(err)
	}

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup written: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup is not the original content: %q", backup)
	}
}

func TestRunningItTwiceIsANoOp(t *testing.T) {
	home := t.TempDir()
	path := writeSettings(t, home, `{"model": "sonnet"}`)

	var first bytes.Buffer
	if err := Run(home, "/bin", &first); err != nil {
		t.Fatal(err)
	}
	afterFirst, _ := os.ReadFile(path)

	var second bytes.Buffer
	if err := Run(home, "/bin", &second); err != nil {
		t.Fatal(err)
	}
	afterSecond, _ := os.ReadFile(path)

	if string(afterFirst) != string(afterSecond) {
		t.Error("second run modified the file again")
	}
	if !strings.Contains(second.String(), "Already installed") {
		t.Errorf("second run should report already-installed, got: %s", second.String())
	}
}

func TestAnExistingDifferentStatusLineIsLeftAlone(t *testing.T) {
	// Somebody else's deliberate choice, possibly another plugin's. Report
	// it, never silently take it over.
	home := t.TempDir()
	original := `{
  "statusLine": {
    "type": "command",
    "command": "some-other-tool --render",
    "refreshInterval": 5
  }
}
`
	path := writeSettings(t, home, original)

	var out bytes.Buffer
	if err := Run(home, "/bin", &out); err != nil {
		t.Fatal(err)
	}

	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Errorf("overwrote an existing statusLine:\n%s", after)
	}
	if !strings.Contains(out.String(), "Left unchanged") {
		t.Errorf("should say it left it alone, got: %s", out.String())
	}
	if !strings.Contains(out.String(), "some-other-tool") {
		t.Error("should show the user what is currently set")
	}
}

func TestMalformedSettingsAreRefusedNotRewritten(t *testing.T) {
	home := t.TempDir()
	original := `{"model": "sonnet",,,}`
	path := writeSettings(t, home, original)

	var out bytes.Buffer
	err := Run(home, "/bin", &out)
	if err == nil {
		t.Fatal("expected an error on malformed settings")
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("unhelpful error: %v", err)
	}

	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Error("modified a file it could not parse")
	}
}

func TestAMissingSettingsFileIsCreated(t *testing.T) {
	home := t.TempDir()

	var out bytes.Buffer
	if err := Run(home, "/bin", &out); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(SettingsPath(home))
	if err != nil {
		t.Fatalf("did not create settings.json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("created invalid JSON: %v", err)
	}
	if _, ok := got["statusLine"]; !ok {
		t.Error("created file without statusLine")
	}
}

func TestTheCreatedFileIsNotWorldReadable(t *testing.T) {
	// This file accumulates tokens and API keys in real use.
	home := t.TempDir()
	var out bytes.Buffer
	if err := Run(home, "/bin", &out); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(SettingsPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions are %o, want 600", perm)
	}
}

func TestTheCommandUsesForwardSlashes(t *testing.T) {
	// It is written into JSON and run through a shell; backslashes would
	// need escaping and read badly on Windows.
	got := StatusLineCommand(`C:\Users\me\.claude\skills\cache-ttl-tracker\bin`)
	if strings.Contains(got, `\`) {
		t.Errorf("command contains backslashes: %s", got)
	}
	if !strings.HasSuffix(got, "statusline") {
		t.Errorf("command should end with the subcommand: %s", got)
	}
}
