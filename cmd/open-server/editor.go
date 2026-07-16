package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode"

	"open-server/internal/sessions"
)

func editSavedSessions(store sessions.Store, stdin io.Reader, stdout, stderr io.Writer) error {
	if err := store.EnsureExists(); err != nil {
		return err
	}
	executable, arguments, err := editorInvocation(os.Getenv, store.Path)
	if err != nil {
		return err
	}
	command := exec.Command(executable, arguments...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("edit saved sessions %q with %q: %w", store.Path, executable, err)
	}
	return nil
}

func editorInvocation(getenv func(string) string, configPath string) (string, []string, error) {
	for _, variable := range []string{"VISUAL", "EDITOR"} {
		value := getenv(variable)
		if strings.TrimSpace(value) == "" {
			continue
		}
		command, err := splitEditorCommand(value)
		if err != nil {
			return "", nil, fmt.Errorf("parse $%s: %w", variable, err)
		}
		arguments := append([]string(nil), command[1:]...)
		return command[0], append(arguments, configPath), nil
	}
	return "vim", []string{configPath}, nil
}

// splitEditorCommand parses the quoting and escaping needed for editor
// commands such as `code --wait` without executing the value through a shell.
func splitEditorCommand(value string) ([]string, error) {
	var command []string
	var argument strings.Builder
	var quote rune
	started := false
	flush := func() {
		command = append(command, argument.String())
		argument.Reset()
		started = false
	}

	characters := []rune(value)
	for index := 0; index < len(characters); index++ {
		character := characters[index]
		if quote == '\'' {
			if character == quote {
				quote = 0
			} else {
				argument.WriteRune(character)
			}
			continue
		}
		if character == '\\' {
			if index+1 == len(characters) {
				return nil, errors.New("editor command ends with an incomplete escape")
			}
			next := characters[index+1]
			escapable := next == '\\' || next == '\'' || next == '"' || unicode.IsSpace(next)
			if quote == '"' {
				escapable = next == '\\' || next == '"'
			}
			if escapable {
				argument.WriteRune(next)
				index++
			} else {
				argument.WriteRune(character)
			}
			started = true
			continue
		}
		switch {
		case quote != 0:
			if character == quote {
				quote = 0
			} else {
				argument.WriteRune(character)
			}
		case character == '\'' || character == '"':
			quote = character
			started = true
		case unicode.IsSpace(character):
			if started {
				flush()
			}
		default:
			argument.WriteRune(character)
			started = true
		}
	}
	if quote != 0 {
		return nil, errors.New("editor command contains an unterminated quote")
	}
	if started {
		flush()
	}
	if len(command) == 0 || command[0] == "" {
		return nil, errors.New("editor command is empty")
	}
	return command, nil
}
