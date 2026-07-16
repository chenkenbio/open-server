package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"open-server/internal/sessions"
)

func TestEditorInvocationPrecedenceAndArguments(t *testing.T) {
	values := map[string]string{
		"VISUAL": `"/Applications/Visual Studio Code.app/Contents/MacOS/Electron" --wait "two words"`,
		"EDITOR": "nano",
	}
	getenv := func(name string) string { return values[name] }

	executable, arguments, err := editorInvocation(getenv, "/tmp/saved sessions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if executable != "/Applications/Visual Studio Code.app/Contents/MacOS/Electron" {
		t.Fatalf("VISUAL executable = %q", executable)
	}
	wantArguments := []string{"--wait", "two words", "/tmp/saved sessions.yaml"}
	if !reflect.DeepEqual(arguments, wantArguments) {
		t.Fatalf("VISUAL arguments = %#v, want %#v", arguments, wantArguments)
	}

	delete(values, "VISUAL")
	executable, arguments, err = editorInvocation(getenv, "/tmp/config.yaml")
	if err != nil || executable != "nano" || !reflect.DeepEqual(arguments, []string{"/tmp/config.yaml"}) {
		t.Fatalf("EDITOR invocation = %q %#v, %v", executable, arguments, err)
	}

	delete(values, "EDITOR")
	executable, arguments, err = editorInvocation(getenv, "/tmp/config.yaml")
	if err != nil || executable != "vim" || !reflect.DeepEqual(arguments, []string{"/tmp/config.yaml"}) {
		t.Fatalf("default invocation = %q %#v, %v", executable, arguments, err)
	}
}

func TestEditorInvocationRejectsMalformedCommand(t *testing.T) {
	_, _, err := editorInvocation(func(name string) string {
		if name == "VISUAL" {
			return `code --wait "unterminated`
		}
		return ""
	}, "/tmp/config.yaml")
	if err == nil || !strings.Contains(err.Error(), "parse $VISUAL") || !strings.Contains(err.Error(), "unterminated quote") {
		t.Fatalf("malformed editor error = %v", err)
	}
}

func TestSplitEditorCommandPreservesWindowsPathSeparators(t *testing.T) {
	got, err := splitEditorCommand(`"C:\Program Files\Vim\vim.exe" --nofork`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`C:\Program Files\Vim\vim.exe`, "--nofork"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("split editor command = %#v, want %#v", got, want)
	}
}

func TestRunEditLaunchesConfiguredEditor(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HOME", directory)
	t.Setenv("XDG_CONFIG_HOME", directory)
	t.Setenv("AppData", directory)
	store, err := sessions.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	configPath := store.Path
	capturePath := filepath.Join(directory, "editor-arguments")
	t.Setenv("GO_WANT_EDITOR_HELPER", "1")
	t.Setenv("EDITOR_CAPTURE_PATH", capturePath)
	t.Setenv("VISUAL", quoteEditorArgument(os.Args[0])+` -test.run=^TestEditorHelperProcess$ -- --wait`)
	t.Setenv("EDITOR", "must-not-run")

	if err := run([]string{"-edit"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	arguments, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	want := "--wait\n" + configPath
	if got := strings.TrimSuffix(string(arguments), "\n"); got != want {
		t.Fatalf("editor arguments = %q, want %q", got, want)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "version: 1\nsessions: {}\n"; got != want {
		t.Fatalf("initialized config = %q, want %q", got, want)
	}
}

func TestEditorHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_EDITOR_HELPER") != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		t.Fatal("editor helper did not receive an argument separator")
	}
	contents := strings.Join(os.Args[separator+1:], "\n") + "\n"
	if err := os.WriteFile(os.Getenv("EDITOR_CAPTURE_PATH"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEditSavedSessionsReportsLaunchFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "saved-sessions.yaml")
	t.Setenv("VISUAL", "open-server-editor-that-does-not-exist")
	t.Setenv("EDITOR", "")
	err := editSavedSessions(sessions.Store{Path: path}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "open-server-editor-that-does-not-exist") {
		t.Fatalf("editor launch error = %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("config was not initialized before editor launch: %v", statErr)
	}
}

func quoteEditorArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
