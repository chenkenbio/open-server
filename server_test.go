package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestParseSortState(t *testing.T) {
	tests := []struct {
		name string
		in   url.Values
		want sortState
	}{
		{
			name: "size desc",
			in:   url.Values{"sort": []string{"size"}, "order": []string{"desc"}},
			want: sortState{Column: sortBySize, Order: sortOrderDesc},
		},
		{
			name: "modified asc",
			in:   url.Values{"sort": []string{"modified"}, "order": []string{"asc"}},
			want: sortState{Column: sortByModified, Order: sortOrderAsc},
		},
		{
			name: "invalid falls back",
			in:   url.Values{"sort": []string{"unknown"}, "order": []string{"sideways"}},
			want: sortState{Column: sortByName, Order: sortOrderAsc},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSortState(tt.in); got != tt.want {
				t.Fatalf("parseSortState(%v) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestSortEntries(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		state sortState
		want  []string
	}{
		{
			name:  "name desc keeps directories first",
			state: sortState{Column: sortByName, Order: sortOrderDesc},
			want:  []string{"dir/", "c.txt", "b.txt", "a.txt"},
		},
		{
			name:  "size asc",
			state: sortState{Column: sortBySize, Order: sortOrderAsc},
			want:  []string{"dir/", "a.txt", "b.txt", "c.txt"},
		},
		{
			name:  "modified desc",
			state: sortState{Column: sortByModified, Order: sortOrderDesc},
			want:  []string{"dir/", "a.txt", "b.txt", "c.txt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := []Entry{
				{Name: "b.txt", SizeBytes: 20, ModTimeValue: t2},
				{Name: "dir/", IsDir: true},
				{Name: "c.txt", SizeBytes: 30, ModTimeValue: t1},
				{Name: "a.txt", SizeBytes: 10, ModTimeValue: t3},
			}
			sortEntries(entries, tt.state)
			got := make([]string, 0, len(entries))
			for _, entry := range entries {
				got = append(got, entry.Name)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("sortEntries names = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func newMultipartUploadRequest(t *testing.T, targetURL, fileName, content string) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("write multipart content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, targetURL, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestUploadHandlerUsesRequestedDirectory(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	req := newMultipartUploadRequest(t, "/upload?dir=/nested/&token=12345678", "note.txt", "hello")
	rec := httptest.NewRecorder()
	uploadHandler(root).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(nested, "note.txt"))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("uploaded content = %q, want %q", got, "hello")
	}
	if _, err := os.Stat(filepath.Join(root, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("upload should not write to served root; stat err = %v", err)
	}
}

func TestUploadHandlerRejectsParentDirectoryTarget(t *testing.T) {
	root := t.TempDir()
	req := newMultipartUploadRequest(t, "/upload?dir=/../&token=12345678", "note.txt", "hello")
	rec := httptest.NewRecorder()
	uploadHandler(root).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("upload status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestUploadHandlerRejectsExistingFileWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	dstPath := filepath.Join(root, "note.txt")
	if err := os.WriteFile(dstPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	req := newMultipartUploadRequest(t, "/upload?dir=/&token=12345678", "note.txt", "new")
	rec := httptest.NewRecorder()
	uploadHandler(root).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("upload status = %d, want %d", rec.Code, http.StatusConflict)
	}
	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read existing file: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("existing content = %q, want %q", got, "old")
	}
}

func TestUploadHandlerOverwritesExistingFileWhenRequested(t *testing.T) {
	root := t.TempDir()
	dstPath := filepath.Join(root, "note.txt")
	if err := os.WriteFile(dstPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	req := newMultipartUploadRequest(t, "/upload?dir=/&overwrite=1&token=12345678", "note.txt", "new")
	rec := httptest.NewRecorder()
	uploadHandler(root).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read overwritten file: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("overwritten content = %q, want %q", got, "new")
	}
}

func TestUploadHandlerDoesNotOverwriteExistingDirectory(t *testing.T) {
	root := t.TempDir()
	dstPath := filepath.Join(root, "note.txt")
	if err := os.Mkdir(dstPath, 0o755); err != nil {
		t.Fatalf("mkdir conflict directory: %v", err)
	}

	req := newMultipartUploadRequest(t, "/upload?dir=/&overwrite=1&token=12345678", "note.txt", "new")
	rec := httptest.NewRecorder()
	uploadHandler(root).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("upload status = %d, want %d", rec.Code, http.StatusConflict)
	}
	info, err := os.Stat(dstPath)
	if err != nil {
		t.Fatalf("stat conflict path: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("conflict path should remain a directory")
	}
}

func TestServeFilesExitsAfterDuration(t *testing.T) {
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- serveFiles("127.0.0.1", 0, 0, dir, "", "test", dir, "12345678", "", "", 20*time.Millisecond)
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

func TestDisplayPath(t *testing.T) {
	root := filepath.Join(string(os.PathSeparator), "home", "kenchen", "Documents", "xxx")
	tests := []struct {
		name        string
		requestPath string
		want        string
	}{
		{name: "root", requestPath: "/", want: root},
		{name: "subdir", requestPath: "/figures/", want: filepath.Join(root, "figures")},
		{name: "nested file", requestPath: "/figures/panel.pdf", want: filepath.Join(root, "figures", "panel.pdf")},
		{name: "parent", requestPath: "/figures/..", want: root},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayPath(root, tt.requestPath); got != tt.want {
				t.Fatalf("displayPath(%q, %q) = %q, want %q", root, tt.requestPath, got, tt.want)
			}
		})
	}
}

func TestMakeBreadcrumbs(t *testing.T) {
	querySuffix := "?order=asc&sort=name&token=12345678"
	tests := []struct {
		name        string
		requestPath string
		want        []breadcrumb
	}{
		{
			name:        "root",
			requestPath: "/",
			want: []breadcrumb{
				{Label: ".", Href: "/" + querySuffix},
			},
		},
		{
			name:        "nested path",
			requestPath: "/figures/panels/",
			want: []breadcrumb{
				{Label: ".", Href: "/" + querySuffix},
				{Label: "figures", Href: "/figures/" + querySuffix},
				{Label: "panels", Href: "/figures/panels/" + querySuffix},
			},
		},
		{
			name:        "escaped segment",
			requestPath: "/figure sets/panel a/",
			want: []breadcrumb{
				{Label: ".", Href: "/" + querySuffix},
				{Label: "figure sets", Href: "/figure%20sets/" + querySuffix},
				{Label: "panel a", Href: "/figure%20sets/panel%20a/" + querySuffix},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := makeBreadcrumbs(tt.requestPath, querySuffix); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("makeBreadcrumbs(%q, %q) = %#v, want %#v", tt.requestPath, querySuffix, got, tt.want)
			}
		})
	}
}

func TestTemplatePlacesUploadBetweenPathAndColumnHeaders(t *testing.T) {
	pathIndex := strings.Index(htmlTemplate, `Path: {{range`)
	uploadIndex := strings.Index(htmlTemplate, `id="drop-zone"`)
	headerIndex := strings.Index(htmlTemplate, `<tr><th align="left"`)
	if pathIndex == -1 || uploadIndex == -1 || headerIndex == -1 {
		t.Fatalf("template is missing path line, upload frame, or column header")
	}
	if !(pathIndex < uploadIndex && uploadIndex < headerIndex) {
		t.Fatalf("upload frame should be between path line and column headers")
	}
}

func TestTemplateIncludesUploadConflictDialog(t *testing.T) {
	for _, want := range []string{
		`id="conflict-modal"`,
		`id="conflict-overwrite"`,
		`id="conflict-skip"`,
		`Apply this choice to all remaining conflicts`,
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Fatalf("template missing %q", want)
		}
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
