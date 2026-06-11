package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseDurationSpec(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want time.Duration
	}{
		{name: "days", spec: "7d", want: 7 * 24 * time.Hour},
		{name: "hours", spec: "12h", want: 12 * time.Hour},
		{name: "minutes", spec: "30m", want: 30 * time.Minute},
		{name: "trim and uppercase", spec: " 2D ", want: 2 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDurationSpec(tt.spec)
			if err != nil {
				t.Fatalf("parseDurationSpec(%q) returned error: %v", tt.spec, err)
			}
			if got != tt.want {
				t.Fatalf("parseDurationSpec(%q) = %v, want %v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestParseDurationSpecRejectsInvalidValues(t *testing.T) {
	tests := []string{"", "7", "1s", "0m", "-1h", "dm", "1w"}
	for _, spec := range tests {
		t.Run(spec, func(t *testing.T) {
			if _, err := parseDurationSpec(spec); err == nil {
				t.Fatalf("parseDurationSpec(%q) returned nil error", spec)
			}
		})
	}
}

func TestFormatDurationSpec(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{duration: 7 * 24 * time.Hour, want: "7d"},
		{duration: 12 * time.Hour, want: "12h"},
		{duration: 30 * time.Minute, want: "30m"},
	}
	for _, tt := range tests {
		if got := formatDurationSpec(tt.duration); got != tt.want {
			t.Fatalf("formatDurationSpec(%v) = %q, want %q", tt.duration, got, tt.want)
		}
	}
}

func TestServeFilesExitsAfterDuration(t *testing.T) {
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- serveFiles("127.0.0.1", 0, 0, dir, "", "test", "12345678", 20*time.Millisecond)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveFiles returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveFiles did not exit after duration")
	}
}

func TestDefaultPageTitleUsesProvidedPathName(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real-dir")
	linkDir := filepath.Join(root, "link-dir")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real dir: %v", err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if got := defaultPageTitle(linkDir, ""); got != linkDir {
		t.Fatalf("defaultPageTitle(%q, \"\") = %q, want %q", linkDir, got, linkDir)
	}
	if got := defaultPageTitle(filepath.Join(linkDir, "file.txt"), "file.txt"); got != linkDir {
		t.Fatalf("defaultPageTitle(file path) = %q, want %q", got, linkDir)
	}
}

func TestDefaultPageTitleUsesLogicalPWD(t *testing.T) {
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalCwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	root := t.TempDir()
	realDir := filepath.Join(root, "real-dir")
	linkDir := filepath.Join(root, "logical-dir")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real dir: %v", err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Chdir(realDir); err != nil {
		t.Fatalf("chdir real dir: %v", err)
	}
	t.Setenv("PWD", linkDir)

	if got := defaultPageTitle(".", ""); got != linkDir {
		t.Fatalf("defaultPageTitle(\".\", \"\") = %q, want %q", got, linkDir)
	}
}

func TestDefaultPageTitleExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := filepath.Join(home, "Documents", "xxx")
	if got := defaultPageTitle("~/Documents/xxx", ""); got != want {
		t.Fatalf("defaultPageTitle(\"~/Documents/xxx\", \"\") = %q, want %q", got, want)
	}
	if got := defaultPageTitle("~/Documents/xxx/file.txt", "file.txt"); got != want {
		t.Fatalf("defaultPageTitle(file under home) = %q, want %q", got, want)
	}
}

func TestParsePathExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "data")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}

	gotDir, gotFile, err := parsePath("~/data")
	if err != nil {
		t.Fatalf("parsePath(\"~/data\") returned error: %v", err)
	}
	if gotDir != dir {
		t.Fatalf("parsePath(\"~/data\") dir = %q, want %q", gotDir, dir)
	}
	if gotFile != "" {
		t.Fatalf("parsePath(\"~/data\") fileBase = %q, want empty", gotFile)
	}
}
