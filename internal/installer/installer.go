// Package installer wires this plugin's statusline into the user's own
// settings.json, because Claude Code gives a plugin no way to do it itself.
//
// Verified against Claude Code 2.1.245 and the plugin manifest reference:
// there is no `statusLine` top-level key in plugin.json (`claude plugin
// validate --strict` reports "Unknown field 'statusLine'. Claude Code
// ignores it at load time"), and the only settings a plugin may ship at its
// own root are `agent` and `subagentStatusLine`, the latter being the
// status line for *subagents*, not the main session one. So the main status
// line is a user-settings concern by design, and every plugin that renders
// one has to ask for the same edit. This command exists to make that edit a
// single command rather than hand-edited JSON.
//
// Two rules govern writing to somebody's central config, which also holds
// their credentials and permission rules:
//
//  1. Never write JSON we haven't re-parsed and checked. Every path below
//     validates the result before it touches the real file, and aborts with
//     paste-able instructions rather than writing something questionable.
//  2. Never silently rewrite a key that is already set to something else.
//     A statusline that is already configured is somebody's deliberate
//     choice, possibly another plugin's, so that case prints and stops.
package installer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// StatusLineCommand is the command string to install, resolved for the
// machine this is running on.
//
// On Windows this deliberately points at the .exe rather than the `sh`
// dispatcher beside it: the dispatcher only works if Claude Code invokes
// statusline commands through Git Bash, which is claimed but not something
// this code can verify, and a native executable sidesteps the question
// entirely. Everywhere else the dispatcher is correct and keeps the path
// stable across architectures.
func StatusLineCommand(binDir string) string {
	return BinaryPath(binDir) + " statusline"
}

// BinaryPath is the executable alone, with no subcommand. Kept separate from
// StatusLineCommand because both are shown to people: appending " install" to
// the statusline command produced the nonsense "...cache-ttl-tracker
// statusline install" in the setup notice, which is exactly the sort of thing
// that only shows up by reading the rendered output rather than the code.
func BinaryPath(binDir string) string {
	// Not filepath.ToSlash: that only rewrites separators on Windows, so on
	// any other platform it silently leaves a Windows path alone. The value
	// goes into JSON and then through a shell, where a backslash is an
	// escape character, so normalise it unconditionally.
	binDir = strings.ReplaceAll(binDir, `\`, "/")
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("%s/cache-ttl-tracker-windows-%s.exe", binDir, runtime.GOARCH)
	}
	return binDir + "/cache-ttl-tracker"
}

func desiredBlock(command string) map[string]any {
	return map[string]any{
		"type":            "command",
		"command":         command,
		"refreshInterval": 1,
	}
}

// SettingsPath is the user-scope settings file. Deliberately user scope and
// not project: plugin option storage (`pluginConfigs`) is read only from
// user, --settings and managed settings, so a project-scope write would be
// silently ignored for half of what this plugin needs.
func SettingsPath(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

// Run installs (or reports on) the statusLine entry. binDir is where this
// binary lives; home is the user's home directory.
func Run(home, binDir string, stdout io.Writer) error {
	path := SettingsPath(home)
	command := StatusLineCommand(binDir)
	want := desiredBlock(command)

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return createFresh(path, want, command, stdout)
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	// Parse before touching anything: if their settings file is already
	// malformed, that is theirs to fix and not ours to rewrite over.
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return fmt.Errorf("%s is not valid JSON (%w); fix it first, this command will not rewrite it", path, err)
	}

	if existing, ok := settings["statusLine"]; ok {
		return handleExisting(existing, want, command, path, stdout)
	}

	return insert(path, raw, want, command, stdout)
}

func createFresh(path string, want map[string]any, command string, stdout io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(map[string]any{"statusLine": want}, "", "  ")
	if err != nil {
		return err
	}
	// 0600, not 0644: this file routinely accumulates tokens and API keys.
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Created %s with:\n  %s\n", path, command)
	return nil
}

func handleExisting(existing any, want map[string]any, command, path string, stdout io.Writer) error {
	if sameBlock(existing, want) {
		fmt.Fprintf(stdout, "Already installed: %s statusLine already points at:\n  %s\n", path, command)
		return nil
	}
	// Somebody's existing choice. Report, don't overwrite.
	current, _ := json.Marshal(existing)
	fmt.Fprintf(stdout, "%s already has a different statusLine:\n  %s\n\n", path, current)
	fmt.Fprintf(stdout, "Left unchanged. To use this plugin's statusline instead, set it to:\n\n")
	fmt.Fprint(stdout, blockSnippet(command))
	return nil
}

// insert adds the key textually rather than re-marshalling the whole file,
// so every other key keeps its original order and formatting. Re-marshalling
// a Go map sorts keys alphabetically, which would scramble a file the user
// (and Claude Code itself) has reason to read.
func insert(path string, raw []byte, want map[string]any, command string, stdout io.Writer) error {
	text := string(raw)
	open := strings.Index(text, "{")
	if open < 0 {
		return fmt.Errorf("%s has no JSON object to edit", path)
	}

	value, err := json.MarshalIndent(want, "  ", "  ")
	if err != nil {
		return err
	}
	insertion := fmt.Sprintf("\n  \"statusLine\": %s,", value)
	updated := text[:open+1] + insertion + text[open+1:]

	// The whole safety story: never write something we have not re-parsed
	// and confirmed says what we intended.
	var check map[string]any
	if err := json.Unmarshal([]byte(updated), &check); err != nil {
		fmt.Fprintf(stdout, "Could not safely edit %s automatically.\n\n", path)
		fmt.Fprintf(stdout, "Add this to it by hand instead:\n\n")
		fmt.Fprint(stdout, blockSnippet(command))
		return nil
	}
	if !sameBlock(check["statusLine"], want) {
		fmt.Fprintf(stdout, "Edit to %s did not verify. Nothing written.\n\n", path)
		fmt.Fprint(stdout, blockSnippet(command))
		return nil
	}

	backup := path + ".bak"
	if err := os.WriteFile(backup, raw, 0o600); err != nil {
		return fmt.Errorf("could not write backup %s: %w", backup, err)
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Installed statusLine into %s\n", path)
	fmt.Fprintf(stdout, "  command: %s\n", command)
	fmt.Fprintf(stdout, "  backup:  %s\n", backup)
	fmt.Fprintf(stdout, "\nIt appears within one refresh tick, no restart needed.\n")
	return nil
}

func blockSnippet(command string) string {
	return fmt.Sprintf("  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": %q,\n    \"refreshInterval\": 1\n  }\n", command)
}

// sameBlock compares through JSON rather than field by field, so a value
// decoded from a file (numbers as float64) compares equal to one built in
// Go (int), which a naive reflect.DeepEqual would not.
func sameBlock(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	var av, bv map[string]any
	if json.Unmarshal(x, &av) != nil || json.Unmarshal(y, &bv) != nil {
		return false
	}
	if len(av) != len(bv) {
		return false
	}
	for k, want := range bv {
		got, ok := av[k]
		if !ok || fmt.Sprint(got) != fmt.Sprint(want) {
			return false
		}
	}
	return true
}
