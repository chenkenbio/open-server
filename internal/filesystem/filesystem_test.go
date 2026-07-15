package filesystem

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanRemotePathRejectsLexicalTraversal(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"../secret", "/safe/../secret", `safe\..\secret`} {
		if _, err := CleanRemotePath(name); !errors.Is(err, ErrTraversal) {
			t.Errorf("CleanRemotePath(%q) error = %v, want ErrTraversal", name, err)
		}
	}
	for _, name := range []string{"/", "/safe/.../file", "relative/path", "/space and 日本語"} {
		if _, err := CleanRemotePath(name); err != nil {
			t.Errorf("CleanRemotePath(%q) unexpected error: %v", name, err)
		}
	}
}

func TestLocalUploadConflictOverwriteAndCleanup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	destination := filepath.ToSlash(filepath.Join(dir, "result.txt"))
	if err := os.WriteFile(filepath.FromSlash(destination), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := Local{}
	if _, err := backend.Upload(context.Background(), destination, strings.NewReader("new"), false); !errors.Is(err, ErrExists) {
		t.Fatalf("non-overwrite upload error = %v, want ErrExists", err)
	}
	assertFileContents(t, destination, "original")
	if _, err := backend.Upload(context.Background(), destination, strings.NewReader("replacement"), true); err != nil {
		t.Fatal(err)
	}
	assertFileContents(t, destination, "replacement")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	partial := filepath.ToSlash(filepath.Join(dir, "partial.txt"))
	if _, err := backend.Upload(canceled, partial, strings.NewReader(strings.Repeat("x", 1024)), false); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled upload error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.FromSlash(partial)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial upload was exposed: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".open-server-upload-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary upload files remain: %v", matches)
	}
}

func TestLocalReadDirFollowsLinksOutsideStart(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	start := filepath.Join(parent, "start")
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(start, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(start, "outside-link")); err != nil {
		t.Fatal(err)
	}
	entries, err := (Local{}).ReadDir(context.Background(), filepath.ToSlash(start))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsLink() || !entries[0].IsDir() {
		t.Fatalf("symlink entry = %#v, want followed directory link", entries)
	}
}

func TestLocalMkdir(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "new-folder")
	if err := (Local{}).Mkdir(context.Background(), filepath.ToSlash(directory)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		t.Fatalf("created directory = %#v, %v", info, err)
	}
	if err := (Local{}).Mkdir(context.Background(), filepath.ToSlash(directory)); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate Mkdir error = %v, want fs.ErrExist", err)
	}
}

func assertFileContents(t *testing.T, name, want string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.FromSlash(name))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != want {
		t.Fatalf("%s contents = %q, want %q", name, contents, want)
	}
}
