package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"open-server/internal/filesystem"
	"open-server/internal/jupyter"
)

func TestAppAssets(t *testing.T) {
	root := t.TempDir()
	app, err := New(Options{
		Backend: filesystem.Local{}, Root: filepath.ToSlash(root), SSHHost: "lab",
		Title: "Files", AllowedHost: testHost,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		contentType string
	}{
		{name: "jupyter.svg", contentType: "image/svg+xml"},
		{name: "tensorboard.png", contentType: "image/png"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				response := request(app, method, appAssetRoutePrefix+test.name, nil)
				if response.Code != http.StatusOK {
					t.Fatalf("%s status = %d", method, response.Code)
				}
				if got := response.Header().Get("Content-Type"); got != test.contentType {
					t.Fatalf("Content-Type = %q, want %q", got, test.contentType)
				}
				if !strings.Contains(response.Header().Get("Cache-Control"), "immutable") || response.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("asset security headers = %#v", response.Header())
				}
				if method == http.MethodGet && response.Body.Len() == 0 {
					t.Fatal("GET returned an empty asset")
				}
				if method == http.MethodHead && response.Body.Len() != 0 {
					t.Fatal("HEAD returned an asset body")
				}
			}
		})
	}

	unknown := request(app, http.MethodGet, appAssetRoutePrefix+"unknown.svg", nil)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown asset status = %d", unknown.Code)
	}
	postRequest := httptest.NewRequest(http.MethodPost, appAssetRoutePrefix+"jupyter.svg", nil)
	postRequest.Host = testHost
	postRequest.Header.Set("Origin", "http://"+testHost)
	postResponse := httptest.NewRecorder()
	app.ServeHTTP(postResponse, postRequest)
	if postResponse.Code != http.StatusMethodNotAllowed || postResponse.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST asset = %d Allow %q", postResponse.Code, postResponse.Header().Get("Allow"))
	}
}

func TestUnifiedActionsColumn(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "work")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "events.out.tfevents.test"), []byte("events"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "report.pdf"), []byte("%PDF-1.4\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := New(Options{
		Backend: filesystem.Local{}, Root: filepath.ToSlash(root), SSHHost: "lab",
		Title: "Files", AllowedHost: testHost, LaTeX: true,
		TensorBoard: &recordingTensorBoardLauncher{handler: http.NotFoundHandler()},
		Jupyter:     &recordingJupyterLauncher{}, DefaultPython: "/opt/default python",
	})
	if err != nil {
		t.Fatal(err)
	}

	listing := request(app, http.MethodGet, "/", nil)
	body := listing.Body.String()
	for _, want := range []string{
		`colspan="3">Actions</th>`, `colspan="3" aria-label="LaTeX tools"`,
		`src="/assets/apps/jupyter.svg"`, `src="/assets/apps/tensorboard.png"`,
		`aria-label="Launch JupyterLab"`, `aria-label="Launch TensorBoard"`,
		`class="download-file icon-button"`, `class="open-live icon-button"`,
		`viewBox="0 0 16 16"`, `fill="currentColor"`,
		`M1.5 3C1.5 2.72421 1.72421 2.5 2 2.5H14`,
		`class="copy-snippet icon-button"`, `value="/opt/default python"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("listing is missing %q", want)
		}
	}
	for _, unwanted := range []string{">Apps</th>", ">Download</th>", ">Launch</button>", ">JupyterLab</button>", ">TensorBoard</button>", ">Preview</button>", ">Preview</th>", ">Table</button>", ">Figure</button>", "M6 2h8l4 4v16H6z"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("listing contains visible legacy label %q", unwanted)
		}
	}
	if !strings.Contains(body, `>.</a>`) {
		t.Fatal("listing is missing the current-directory row")
	}
	if got := strings.Count(body, `aria-label="Launch JupyterLab"`); got != 2 {
		t.Fatalf("current and child Jupyter icon count = %d, want 2", got)
	}
	if got := strings.Count(body, `aria-label="Launch TensorBoard"`); got != 1 {
		t.Fatalf("TensorBoard event-directory icon count = %d, want 1", got)
	}
	if got := strings.Count(body, `class="open-live icon-button"`); got != 1 {
		t.Fatalf("PDF preview icon count = %d, want 1", got)
	}

	childListing := request(app, http.MethodGet, "/?path="+url.QueryEscape(filepath.ToSlash(directory)), nil)
	childBody := childListing.Body.String()
	if !strings.Contains(childBody, `>.</a>`) {
		t.Fatal("child listing is missing the current-directory row")
	}
	if got := strings.Count(childBody, `aria-label="Launch JupyterLab"`); got != 2 {
		t.Fatalf("current and parent Jupyter icon count = %d, want 2", got)
	}
	if got := strings.Count(childBody, `aria-label="Launch TensorBoard"`); got != 1 {
		t.Fatalf("current-directory TensorBoard icon count = %d, want 1", got)
	}
}

func TestJupyterLaunchReuseAndAuthenticatedProxy(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	type backendRequest struct {
		path          string
		host          string
		authorization string
		origin        string
	}
	requests := make(chan backendRequest, 4)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- backendRequest{
			path: r.URL.Path, host: r.Host,
			authorization: r.Header.Get("Authorization"), origin: r.Header.Get("Origin"),
		}
		_, _ = io.WriteString(w, "jupyter backend")
	}))
	defer backend.Close()
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	launcher := &recordingJupyterLauncher{target: target, token: "secret-token"}
	app, err := New(Options{
		Backend: filesystem.Local{}, Root: root, SSHHost: "lab", Title: "Files",
		AllowedHost: testHost, Jupyter: launcher, DefaultPython: "/default/python",
	})
	if err != nil {
		t.Fatal(err)
	}

	wrongForm := url.Values{"csrf": {"wrong"}, "python": {"/default/python"}}
	wrongRequest := httptest.NewRequest(http.MethodPost, "/jupyter?path="+url.QueryEscape(root), strings.NewReader(wrongForm.Encode()))
	wrongRequest.Host = testHost
	wrongRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wrongResponse := httptest.NewRecorder()
	app.ServeHTTP(wrongResponse, wrongRequest)
	if wrongResponse.Code != http.StatusForbidden {
		t.Fatalf("wrong form token status = %d", wrongResponse.Code)
	}

	launch := func(python string) *httptest.ResponseRecorder {
		form := url.Values{"csrf": {app.csrfToken}, "python": {python}}
		launchRequest := httptest.NewRequest(http.MethodPost, "/jupyter?path="+url.QueryEscape(root), strings.NewReader(form.Encode()))
		launchRequest.Host = testHost
		launchRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		app.ServeHTTP(response, launchRequest)
		return response
	}
	first := launch("")
	if first.Code != http.StatusSeeOther || !strings.HasPrefix(first.Header().Get("Location"), "/jupyter/") || !strings.HasSuffix(first.Header().Get("Location"), "/lab") {
		t.Fatalf("first launch = %d Location %q body %q", first.Code, first.Header().Get("Location"), first.Body.String())
	}
	second := launch("")
	if second.Header().Get("Location") != first.Header().Get("Location") || len(launcher.starts) != 1 {
		t.Fatalf("same launch was not reused: locations %q %q, starts %#v", first.Header().Get("Location"), second.Header().Get("Location"), launcher.starts)
	}
	if got := launcher.starts[0].kernelPython; got != "/default/python" {
		t.Fatalf("blank Python = %q, want configured default", got)
	}
	third := launch("/custom env/bin/python")
	if third.Code != http.StatusSeeOther || third.Header().Get("Location") == first.Header().Get("Location") || len(launcher.starts) != 2 {
		t.Fatalf("custom launch = %d Location %q, starts %#v", third.Code, third.Header().Get("Location"), launcher.starts)
	}
	if got := launcher.starts[1].kernelPython; got != "/custom env/bin/python" {
		t.Fatalf("custom Python = %q", got)
	}

	proxied := request(app, http.MethodGet, first.Header().Get("Location"), nil)
	if proxied.Code != http.StatusOK || proxied.Body.String() != "jupyter backend" {
		t.Fatalf("proxy response = %d %q", proxied.Code, proxied.Body.String())
	}
	if got := proxied.Header().Get("Content-Security-Policy"); got != "" {
		t.Fatalf("proxy received outer CSP %q", got)
	}
	backendCall := <-requests
	if backendCall.path != first.Header().Get("Location") || backendCall.host != target.Host || backendCall.authorization != "token secret-token" {
		t.Fatalf("backend request = %#v", backendCall)
	}

	websocketRequest := httptest.NewRequest(http.MethodGet, first.Header().Get("Location")+"/api/kernels/1/channels", nil)
	websocketRequest.Host = testHost
	websocketRequest.Header.Set("Connection", "Upgrade")
	websocketRequest.Header.Set("Upgrade", "websocket")
	websocketRequest.Header.Set("Origin", "http://evil.example")
	websocketResponse := httptest.NewRecorder()
	app.ServeHTTP(websocketResponse, websocketRequest)
	if websocketResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin WebSocket status = %d", websocketResponse.Code)
	}
}

func TestJupyterReverseProxyRewritesBackendHeaders(t *testing.T) {
	target, err := url.Parse("http://127.0.0.1:54321")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newJupyterReverseProxy(target, "secret-token")
	request := httptest.NewRequest(http.MethodPost, "http://"+testHost+"/jupyter/session/api/sessions", nil)
	request.Host = testHost
	request.Header.Set("Origin", "http://"+testHost)
	proxy.Director(request)
	if request.URL.Scheme != "http" || request.URL.Host != target.Host || request.Host != target.Host {
		t.Fatalf("proxied destination = URL %s Host %q", request.URL.String(), request.Host)
	}
	if got, want := request.Header.Get("Origin"), "http://"+target.Host; got != want {
		t.Fatalf("proxied Origin = %q, want %q", got, want)
	}
	if got := request.Header.Get("Authorization"); got != "token secret-token" {
		t.Fatalf("proxied Authorization = %q", got)
	}
}

type jupyterStart struct {
	directory    string
	kernelPython string
	prefix       string
}

type recordingJupyterLauncher struct {
	target *url.URL
	token  string
	starts []jupyterStart
}

func (l *recordingJupyterLauncher) Start(directory, kernelPython, prefix string) (*jupyter.Instance, error) {
	l.starts = append(l.starts, jupyterStart{directory: directory, kernelPython: kernelPython, prefix: prefix})
	return &jupyter.Instance{Target: l.target, Token: l.token}, nil
}
