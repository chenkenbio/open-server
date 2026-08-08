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

func TestGroupedApplicationActions(t *testing.T) {
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
		`>Actions</th>`, `colspan="2" class="app-group-heading"`,
		`aria-label="JupyterLab" title="JupyterLab"`, `aria-label="TensorBoard" title="TensorBoard"`,
		`colspan="3" rowspan="2" aria-label="LaTeX tools"`,
		`src="/assets/apps/jupyter.svg"`, `src="/assets/apps/tensorboard.png"`,
		`<th scope="col" class="app-subheading">Start</th>`,
		`<th scope="col" class="app-subheading">Stop</th>`,
		`aria-label="Start JupyterLab"`, `aria-label="Start TensorBoard"`,
		`class="app-stop icon-button"`, `data-state="stopped"`,
		`class="open-entry icon-button"`, `target="_blank" rel="noopener"`,
		`aria-label="Open current directory in a new tab"`, `aria-label="Open work in a new tab"`,
		`class="download-file icon-button"`, `class="open-live icon-button"`,
		`class="column-file-action"`, `colspan="3" rowspan="2">Actions</th>`,
		`aria-label="Copy path" title="Copy path"`,
		`.app-group-heading { border-bottom: 0; }`,
		`.app-subheading { padding-right: 0.4em; padding-left: 0.4em; font-size: 0.78em;`,
		`.app-loading-icon { transform-origin: center; animation: app-icon-spin 0.8s linear infinite; }`,
		`@media (prefers-reduced-motion: reduce)`,
		`role="separator"`, `data-resize-column="name"`,
		`open-server-directory-column-widths:v1`,
		`setPointerCapture`, `e.key === 'Home'`, `addEventListener('dblclick'`,
		`requestError.code = body.code || '';`, `error.code === 'not_tensorboard_folder'`,
		`window.alert(error.message);`,
		`text-overflow: ellipsis`, `title="report.pdf"`,
		`viewBox="0 0 16 16"`, `<circle cx="12" cy="12" r="9.25"></circle>`,
		`<path d="m9.75 8.25 6.5 3.75-6.5 3.75z" fill="currentColor" stroke="none"></path>`,
		`<rect x="8.5" y="8.5" width="7" height="7" rx="1" fill="currentColor" stroke="none"></rect>`,
		`M1.5 3C1.5 2.72421 1.72421 2.5 2 2.5H14`,
		`class="copy-snippet icon-button"`, `value="/opt/default python"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("listing is missing %q", want)
		}
	}
	for _, unwanted := range []string{">Path</th>", `colspan="4">Actions</th>`, ">Apps</th>", ">Download</th>", ">Launch</button>", ">JupyterLab</button>", ">TensorBoard</button>", "JupyterLab</th>", "TensorBoard</th>", ">Preview</button>", ">Preview</th>", ">Table</button>", ">Figure</button>", "M6 2h8l4 4v16H6z", "M8 5v14l11-7z", `<rect x="6" y="6" width="12" height="12" rx="1"></rect>`, `class="file-actions"`, `class="file-action-subheading"`, `aria-label="Download file"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("listing contains visible legacy label %q", unwanted)
		}
	}
	if !strings.Contains(body, `>.</a>`) {
		t.Fatal("listing is missing the current-directory row")
	}
	if got := strings.Count(body, `<col class="column-file-action">`); got != 3 {
		t.Fatalf("file action subcolumn count = %d, want 3", got)
	}
	if got := strings.Count(body, `<th scope="col" class="app-subheading">Start</th>`); got != 2 {
		t.Fatalf("application Start subheading count = %d, want 2", got)
	}
	if got := strings.Count(body, `<th scope="col" class="app-subheading">Stop</th>`); got != 2 {
		t.Fatalf("application Stop subheading count = %d, want 2", got)
	}
	if got := strings.Count(body, `<td class="file-action-column">`); got != 9 {
		t.Fatalf("file action cell count = %d, want 9 (three fixed cells for each of three rows)", got)
	}
	if got := strings.Count(body, `class="open-entry icon-button"`); got != 3 {
		t.Fatalf("open-in-new-tab control count = %d, want 3", got)
	}
	if got := strings.Count(body, `class="copy-path icon-button"`); got != 3 {
		t.Fatalf("copy-path control count = %d, want 3", got)
	}
	if got := strings.Count(body, `class="download-file icon-button"`); got != 1 {
		t.Fatalf("download control count = %d, want 1", got)
	}
	reportOpen := strings.Index(body, `aria-label="Open report.pdf in a new tab"`)
	if reportOpen == -1 {
		t.Fatal("report row is missing its open-in-new-tab action")
	}
	reportRowEnd := strings.Index(body[reportOpen:], `</tr>`)
	if reportRowEnd == -1 {
		t.Fatal("report row has no closing tag")
	}
	reportActions := body[reportOpen : reportOpen+reportRowEnd]
	reportCopy := strings.Index(reportActions, `class="copy-path icon-button"`)
	reportDownload := strings.Index(reportActions, `class="download-file icon-button"`)
	if reportCopy == -1 || reportDownload == -1 || reportCopy >= reportDownload {
		t.Fatalf("report actions are not ordered open, copy, download: copy=%d download=%d", reportCopy, reportDownload)
	}
	if got := strings.Count(body, `class="app-start icon-button" aria-label="Start JupyterLab"`); got != 2 {
		t.Fatalf("current and child Jupyter start count = %d, want 2", got)
	}
	if got := strings.Count(body, `class="app-loading-icon"`); got != 2 {
		t.Fatalf("Jupyter starting-spinner count = %d, want 2", got)
	}
	if got := strings.Count(body, `class="app-start icon-button" aria-label="Start TensorBoard"`); got != 2 {
		t.Fatalf("current and child TensorBoard start count = %d, want 2", got)
	}
	if got := strings.Count(body, `class="app-stop icon-button"`); got != 4 {
		t.Fatalf("hidden Jupyter and TensorBoard stop control count = %d, want 4", got)
	}
	if !strings.Contains(body, `title="Stop JupyterLab" hidden`) || !strings.Contains(body, `title="Stop TensorBoard" hidden`) {
		t.Fatal("stopped application rows do not hide their Stop controls")
	}
	if got := strings.Count(body, `class="open-live icon-button"`); got != 1 {
		t.Fatalf("PDF preview icon count = %d, want 1", got)
	}

	childListing := request(app, http.MethodGet, "/?path="+url.QueryEscape(filepath.ToSlash(directory)), nil)
	childBody := childListing.Body.String()
	if !strings.Contains(childBody, `>.</a>`) {
		t.Fatal("child listing is missing the current-directory row")
	}
	if got := strings.Count(childBody, `class="open-entry icon-button"`); got != 3 {
		t.Fatalf("parent, current, and child-entry open-in-new-tab count = %d, want 3", got)
	}
	if !strings.Contains(childBody, `aria-label="Open parent directory in a new tab"`) {
		t.Fatal("child listing is missing the parent-directory open-in-new-tab action")
	}
	if got := strings.Count(childBody, `class="app-start icon-button" aria-label="Start JupyterLab"`); got != 2 {
		t.Fatalf("current and parent Jupyter start count = %d, want 2", got)
	}
	if got := strings.Count(childBody, `class="app-start icon-button" aria-label="Start TensorBoard"`); got != 2 {
		t.Fatalf("current and parent TensorBoard start count = %d, want 2", got)
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
	runningListing := request(app, http.MethodGet, "/", nil).Body.String()
	if !strings.Contains(runningListing, `href="`+first.Header().Get("Location")+`"`) ||
		!strings.Contains(runningListing, `title="Start JupyterLab" hidden`) ||
		strings.Contains(runningListing, `title="Stop JupyterLab" hidden`) {
		t.Fatalf("running Jupyter controls do not render as Open plus Stop: %s", runningListing)
	}
	third := launch("/custom env/bin/python")
	if third.Code != http.StatusConflict || !strings.Contains(third.Body.String(), "another Python executable") || len(launcher.starts) != 1 {
		t.Fatalf("conflicting custom launch = %d body %q, starts %#v", third.Code, third.Body.String(), launcher.starts)
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

	statusTarget := "/jupyter?path=" + url.QueryEscape(root)
	running := request(app, http.MethodGet, statusTarget, nil)
	if running.Code != http.StatusOK || !strings.Contains(running.Body.String(), `"state":"running"`) ||
		!strings.Contains(running.Body.String(), `"available":true`) ||
		!strings.Contains(running.Body.String(), `"open_url":"`+first.Header().Get("Location")+`"`) {
		t.Fatalf("running Jupyter status = %d %q", running.Code, running.Body.String())
	}

	wrongStop := httptest.NewRequest(http.MethodDelete, statusTarget, nil)
	wrongStop.Host = testHost
	wrongStop.Header.Set("Origin", "http://evil.example")
	wrongStopResponse := httptest.NewRecorder()
	app.ServeHTTP(wrongStopResponse, wrongStop)
	if wrongStopResponse.Code != http.StatusForbidden || launcher.stops != 0 {
		t.Fatalf("cross-origin stop = %d, stops %d", wrongStopResponse.Code, launcher.stops)
	}

	stopRequest := httptest.NewRequest(http.MethodDelete, statusTarget, nil)
	stopRequest.Host = testHost
	stopRequest.Header.Set("Origin", "http://"+testHost)
	stopResponse := httptest.NewRecorder()
	app.ServeHTTP(stopResponse, stopRequest)
	if stopResponse.Code != http.StatusOK || !strings.Contains(stopResponse.Body.String(), `"state":"stopped"`) || launcher.stops != 1 {
		t.Fatalf("Jupyter stop = %d body %q, stops %d", stopResponse.Code, stopResponse.Body.String(), launcher.stops)
	}
	stopped := request(app, http.MethodGet, statusTarget, nil)
	if stopped.Code != http.StatusOK || !strings.Contains(stopped.Body.String(), `"state":"stopped"`) ||
		strings.Contains(stopped.Body.String(), "open_url") {
		t.Fatalf("stopped Jupyter status = %d %q", stopped.Code, stopped.Body.String())
	}
	oldProxy := request(app, http.MethodGet, first.Header().Get("Location"), nil)
	if oldProxy.Code != http.StatusNotFound {
		t.Fatalf("stopped Jupyter proxy status = %d, want %d", oldProxy.Code, http.StatusNotFound)
	}

	repeatedStop := httptest.NewRequest(http.MethodDelete, statusTarget, nil)
	repeatedStop.Host = testHost
	repeatedStop.Header.Set("Origin", "http://"+testHost)
	repeatedStopResponse := httptest.NewRecorder()
	app.ServeHTTP(repeatedStopResponse, repeatedStop)
	if repeatedStopResponse.Code != http.StatusOK || launcher.stops != 1 {
		t.Fatalf("repeated Jupyter stop = %d, stops %d", repeatedStopResponse.Code, launcher.stops)
	}

	restartForm := url.Values{"csrf": {app.csrfToken}, "python": {"/custom env/bin/python"}}
	restartRequest := httptest.NewRequest(http.MethodPost, statusTarget, strings.NewReader(restartForm.Encode()))
	restartRequest.Host = testHost
	restartRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	restartRequest.Header.Set("Accept", "application/json")
	restarted := httptest.NewRecorder()
	app.ServeHTTP(restarted, restartRequest)
	if restarted.Code != http.StatusCreated || !strings.Contains(restarted.Body.String(), `"state":"running"`) ||
		!strings.Contains(restarted.Body.String(), `"available":true`) ||
		strings.Contains(restarted.Body.String(), first.Header().Get("Location")) || len(launcher.starts) != 2 {
		t.Fatalf("Jupyter JSON restart = %d body %q, starts %#v", restarted.Code, restarted.Body.String(), launcher.starts)
	}
	if got := launcher.starts[1].kernelPython; got != "/custom env/bin/python" {
		t.Fatalf("restarted custom Python = %q", got)
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
	stops  int
}

func (l *recordingJupyterLauncher) Start(directory, kernelPython, prefix string) (*jupyter.Instance, error) {
	l.starts = append(l.starts, jupyterStart{directory: directory, kernelPython: kernelPython, prefix: prefix})
	return &jupyter.Instance{Target: l.target, Token: l.token}, nil
}

func (l *recordingJupyterLauncher) Stop(*jupyter.Instance) error {
	l.stops++
	return nil
}
