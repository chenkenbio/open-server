package sessions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreAddResolveListAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "sessions", "saved-sessions.yaml")
	store := Store{Path: path}

	if err := store.Add("production", "prod:/srv/app", Options{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add("development", "dev:~/project", Options{}); err != nil {
		t.Fatal(err)
	}
	localTarget := filepath.Join(t.TempDir(), "paper")
	if err := store.Add("local-paper", localTarget, Options{}); err != nil {
		t.Fatal(err)
	}
	tensorBoard := true
	latex := true
	python := "/env/bin/python"
	title := "Project dashboard"
	if err := store.Add("production", "prod:/srv/new-app", Options{TensorBoard: &tensorBoard, Python: &python, LaTeX: &latex, Title: &title}); err != nil {
		t.Fatal(err)
	}

	resolved, err := store.Resolve("production")
	if err != nil || resolved.Target != "prod:/srv/new-app" || resolved.Options.TensorBoard == nil || !*resolved.Options.TensorBoard || resolved.Options.Python == nil || *resolved.Options.Python != python || resolved.Options.LaTeX == nil || !*resolved.Options.LaTeX || resolved.Options.Title == nil || *resolved.Options.Title != title {
		t.Fatalf("Resolve(production) = %#v, %v", resolved, err)
	}
	if err := store.UpdatePort("production", 61234); err != nil {
		t.Fatal(err)
	}
	resolved, err = store.Resolve("production")
	if err != nil || resolved.Options.Port == nil || *resolved.Options.Port != 61234 || resolved.Options.TensorBoard == nil || !*resolved.Options.TensorBoard || resolved.Options.Python == nil || *resolved.Options.Python != python || resolved.Options.LaTeX == nil || !*resolved.Options.LaTeX || resolved.Options.Title == nil || *resolved.Options.Title != title {
		t.Fatalf("Resolve(production) after UpdatePort = %#v, %v", resolved, err)
	}
	ports, err := store.ReservedPorts("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ports[61234]; !ok || len(ports) != 1 {
		t.Fatalf("ReservedPorts() = %#v, want only 61234", ports)
	}
	ports, err = store.ReservedPorts("production")
	if err != nil || len(ports) != 0 {
		t.Fatalf("ReservedPorts(production) = %#v, %v; want empty", ports, err)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Name != "development" || entries[1].Name != "local-paper" || entries[2].Name != "production" {
		t.Fatalf("List() = %#v", entries)
	}
	localResolved, err := store.Resolve("local-paper")
	if err != nil || localResolved.Target != localTarget {
		t.Fatalf("Resolve(local-paper) = %#v, %v", localResolved, err)
	}

	if err := store.Delete("development"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve("development"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve(development) after delete = %v, want ErrNotFound", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if !strings.Contains(text, "version: 1") || !strings.Contains(text, "production:") || !strings.Contains(text, "target: prod:/srv/new-app") || !strings.Contains(text, "port: 61234") || !strings.Contains(text, "tensorboard: true") || !strings.Contains(text, "python-interpreter: /env/bin/python") || !strings.Contains(text, "latex: true") || !strings.Contains(text, "title: Project dashboard") || strings.Contains(text, "development:") {
		t.Fatalf("saved YAML = %q", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("saved sessions permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestStoreEnsureExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "sessions", "saved-sessions.yaml")
	store := Store{Path: path}

	if err := store.EnsureExists(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, []byte("version: 1\nsessions: {}\n")) {
		t.Fatalf("initialized YAML = %q", contents)
	}
	if entries, err := store.List(); err != nil || len(entries) != 0 {
		t.Fatalf("List() after EnsureExists = %#v, %v", entries, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("initialized permissions = %o, want 600", info.Mode().Perm())
	}

	malformed := []byte("this is intentionally invalid: [\n")
	if err := os.WriteFile(path, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureExists(); err != nil {
		t.Fatal(err)
	}
	contents, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, malformed) {
		t.Fatalf("EnsureExists changed existing config to %q", contents)
	}
}

func TestStoreMissingAndInvalidData(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "saved-sessions.yaml")}
	entries, err := store.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty List() = %#v, %v", entries, err)
	}
	if _, err := store.Resolve("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve(missing) error = %v, want ErrNotFound", err)
	}
	if err := store.Delete("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrNotFound", err)
	}
	if err := store.UpdatePort("missing", 61000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdatePort(missing) error = %v, want ErrNotFound", err)
	}
	for _, port := range []int{0, 65536} {
		if err := store.UpdatePort("missing", port); err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
			t.Errorf("UpdatePort(missing, %d) error = %v", port, err)
		}
	}
	for _, name := range []string{"", "two words", "bad:name", "-option"} {
		if err := store.Add(name, "lab:/tmp", Options{}); err == nil {
			t.Errorf("Add(%q) succeeded, want error", name)
		}
	}
	if err := store.Add("valid", "not-a-target", Options{}); err == nil {
		t.Fatal("Add with invalid target succeeded")
	}
}

func TestStoreRejectsMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "saved-sessions.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nsessions:\n  bad:\n    unknown: value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Path: path}).List(); err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("List() error = %v", err)
	}
}
