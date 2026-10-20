package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"open-server/internal/filesystem"
)

// Opt in with OPEN_SERVER_BROWSER_TESTS=1. The driver uses Node's built-in
// WebSocket and an installed Chrome, with an isolated temporary browser profile.
func TestBrowserNavigation(t *testing.T) {
	if os.Getenv("OPEN_SERVER_BROWSER_TESTS") != "1" {
		t.Skip("set OPEN_SERVER_BROWSER_TESTS=1 for Chrome integration checks")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal(err)
	}
	root := filepath.ToSlash(t.TempDir())
	for _, name := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "inside.txt"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 400 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("entry-%04d.txt", i)), []byte("contents"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".hidden.txt", "README.md", `special <&" 日本語.txt`} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("# Preview\nSafe content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backend := &browserTestBackend{Backend: filesystem.Local{}}
	var app *App
	var forbidden atomic.Bool
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__test/control" {
			var command struct {
				Action string `json:"action"`
				Path   string `json:"path"`
				Delay  int    `json:"delay"`
			}
			if r.Method == http.MethodPost {
				if err := json.NewDecoder(r.Body).Decode(&command); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				switch command.Action {
				case "disconnect":
					app.SetDisconnected()
				case "reconnect":
					app.SetBackend(backend)
					forbidden.Store(false)
				case "forbid":
					forbidden.Store(true)
				case "delay":
					backend.mu.Lock()
					backend.delayPath = command.Path
					backend.delay = time.Duration(command.Delay) * time.Millisecond
					backend.mu.Unlock()
				case "block-upload":
					backend.mu.Lock()
					backend.gate = make(chan struct{})
					backend.mu.Unlock()
				case "release-upload":
					backend.release()
				case "write":
					if err := os.WriteFile(filepath.Join(root, "entry-0000-new.txt"), []byte("new"), 0o600); err != nil {
						t.Error(err)
					}
				}
			}
			writeJSON(w, 200, map[string]any{"reads": backend.reads.Load(), "canceled": backend.canceled.Load(), "uploads": backend.uploads.Load()})
			return
		}
		if r.URL.Path == "/__test/imported.txt" {
			w.Header().Set("Content-Disposition", `attachment; filename="imported.txt"`)
			_, _ = io.WriteString(w, "imported")
			return
		}
		if r.URL.Path == "/browse" && forbidden.Load() && r.URL.Query().Get("path") == root+"/a" {
			http.Error(w, "permission was denied by the remote server", 403)
			return
		}
		app.ServeHTTP(w, r)
	}))
	target, _ := url.Parse("http://127.0.0.1:1")
	var err error
	app, err = New(Options{Backend: backend, Root: root, SSHHost: "test", Title: "Browser tests", AllowedHost: server.Listener.Addr().String(), LaTeX: true,
		Jupyter: &recordingJupyterLauncher{target: target}, TensorBoard: &recordingTensorBoardLauncher{handler: http.NotFoundHandler()}})
	if err != nil {
		t.Fatal(err)
	}
	server.Start()
	defer server.Close()
	defer backend.release()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "browser_test.mjs", server.URL, root)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	t.Log(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("browser checks: %v", err)
	}
}

type browserTestBackend struct {
	filesystem.Backend
	mu        sync.Mutex
	delayPath string
	delay     time.Duration
	gate      chan struct{}
	reads     atomic.Int64
	canceled  atomic.Int64
	uploads   atomic.Int64
}

func (b *browserTestBackend) ReadDir(ctx context.Context, name string) ([]filesystem.Entry, error) {
	b.reads.Add(1)
	b.mu.Lock()
	delayPath, delay := b.delayPath, b.delay
	b.mu.Unlock()
	if name == delayPath && delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			b.canceled.Add(1)
			return nil, ctx.Err()
		}
	}
	return b.Backend.ReadDir(ctx, name)
}

func (b *browserTestBackend) Upload(ctx context.Context, name string, src io.Reader, overwrite bool) (int64, error) {
	b.uploads.Add(1)
	b.mu.Lock()
	gate := b.gate
	b.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return b.Backend.Upload(ctx, name, src, overwrite)
}

func (b *browserTestBackend) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gate != nil {
		close(b.gate)
		b.gate = nil
	}
}
