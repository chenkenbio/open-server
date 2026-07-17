package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"open-server/internal/filesystem"
)

func TestLivePDFAssets(t *testing.T) {
	root := createFixture(t)
	app, err := New(Options{
		Backend:     filesystem.Local{},
		Root:        root,
		SSHHost:     "local",
		AllowedHost: testHost,
		LaTeX:       true,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		path        string
		contentType string
	}{
		{name: "controller", path: liveControllerURL, contentType: "text/javascript; charset=utf-8"},
		{name: "styles", path: liveStylesURL, contentType: "text/css; charset=utf-8"},
		{name: "PDF core", path: pdfJSRoutePrefix + "build/pdf.min.mjs", contentType: "text/javascript; charset=utf-8"},
		{name: "viewer CSS", path: pdfJSRoutePrefix + "web/pdf_viewer.css", contentType: "text/css; charset=utf-8"},
		{name: "WASM decoder", path: pdfJSRoutePrefix + "wasm/qcms_bg.wasm", contentType: "application/wasm"},
		{name: "CMap", path: pdfJSRoutePrefix + "cmaps/78-H.bcmap", contentType: "application/octet-stream"},
		{name: "font", path: pdfJSRoutePrefix + "standard_fonts/LiberationSans-Regular.ttf", contentType: "font/ttf"},
		{name: "viewer image", path: pdfJSRoutePrefix + "web/images/annotation-check.svg", contentType: "image/svg+xml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := request(app, http.MethodGet, test.path, nil)
			if response.Code != http.StatusOK || response.Body.Len() == 0 {
				t.Fatalf("asset response = %d with %d bytes", response.Code, response.Body.Len())
			}
			if got := response.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, test.contentType)
			}
			if !strings.Contains(response.Header().Get("Cache-Control"), "immutable") || response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("asset security headers = %#v", response.Header())
			}
			assetCSP := response.Header().Get("Content-Security-Policy")
			for _, directive := range []string{"default-src 'none'", "script-src 'self' 'wasm-unsafe-eval'", "connect-src 'self'"} {
				if !strings.Contains(assetCSP, directive) {
					t.Errorf("asset CSP %q is missing %q", assetCSP, directive)
				}
			}
		})
	}

	getResponse := request(app, http.MethodGet, liveControllerURL, nil)
	headResponse := request(app, http.MethodHead, liveControllerURL, nil)
	if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 || headResponse.Header().Get("Content-Length") != strconv.Itoa(getResponse.Body.Len()) {
		t.Fatalf("asset HEAD = %d headers %#v body length %d", headResponse.Code, headResponse.Header(), headResponse.Body.Len())
	}

	postRequest := httptest.NewRequest(http.MethodPost, liveControllerURL, nil)
	postRequest.Host = testHost
	postRequest.Header.Set("Origin", "http://"+testHost)
	postResponse := httptest.NewRecorder()
	app.ServeHTTP(postResponse, postRequest)
	if postResponse.Code != http.StatusMethodNotAllowed || postResponse.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("asset POST = %d Allow %q", postResponse.Code, postResponse.Header().Get("Allow"))
	}
	for _, invalidPath := range []string{
		liveAssetRoutePrefix,
		pdfJSRoutePrefix + "wasm/",
		pdfJSRoutePrefix + "../live-v2.mjs",
		liveAssetRoutePrefix + "live-v1.mjs",
		liveAssetRoutePrefix + "live-v1.css",
		liveAssetRoutePrefix + "unknown.js",
	} {
		if response := request(app, http.MethodGet, invalidPath, nil); response.Code != http.StatusNotFound {
			t.Errorf("GET %q = %d, want %d", invalidPath, response.Code, http.StatusNotFound)
		}
	}

	plain, err := New(Options{
		Backend:     filesystem.Local{},
		Root:        root,
		SSHHost:     "local",
		AllowedHost: testHost,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response := request(plain, http.MethodGet, liveControllerURL, nil); response.Code != http.StatusNotFound {
		t.Fatalf("asset without LaTeX mode = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestLivePDFPageAndSecurityContract(t *testing.T) {
	root := createFixture(t)
	app, err := New(Options{
		Backend:     filesystem.Local{},
		Root:        root,
		SSHHost:     "local",
		AllowedHost: testHost,
		LaTeX:       true,
	})
	if err != nil {
		t.Fatal(err)
	}

	name := filepath.ToSlash(filepath.Join(filepath.FromSlash(root), "preview.pdf"))
	response := request(app, http.MethodGet, "/live?path="+url.QueryEscape(name), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("live response = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{
		`data-initial-ready="true"`,
		`data-download-url="/download?`,
		`id="pageNumber"`,
		`id="pageCount"`,
		`<select id="zoomPreset" aria-label="Zoom level" title="Zoom level">`,
		`<option id="zoomCustom" value="" disabled>Custom</option>`,
		`<option value="page-width" selected>Fit width</option>`,
		`<option value="page-fit">Fit page</option>`,
		`<option value="page-actual">Actual size</option>`,
		`<option value="0.5">50%</option>`,
		`<option value="0.75">75%</option>`,
		`<option value="1">100%</option>`,
		`<option value="1.25">125%</option>`,
		`<option value="1.5">150%</option>`,
		`<option value="2">200%</option>`,
		`<option value="3">300%</option>`,
		`<option value="4">400%</option>`,
		`id="download"`,
		`<button id="download" type="button" aria-label="Download PDF" title="Download PDF"><svg`,
		`class="control-group"`,
		`class="control-divider"`,
		`id="viewer" class="pdfViewer"`,
		`src="` + liveControllerURL + `"`,
		`href="` + liveStylesURL + `"`,
		`href="` + pdfJSRoutePrefix + `web/pdf_viewer.css"`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("live response is missing %q", expected)
		}
	}
	if strings.Contains(body, "<iframe") || strings.Contains(body, "contentWindow.location.reload") {
		t.Fatal("live response still delegates state to a native PDF iframe")
	}
	if strings.Contains(body, "<datalist") || strings.Contains(body, `list="zoomPresets"`) {
		t.Fatal("live response still uses the unreliable zoom datalist")
	}
	if got := strings.Count(body, `id="zoomPreset"`); got != 1 || strings.Contains(body, `id="zoomValue"`) {
		t.Fatalf("live response has duplicate zoom controls: zoomPreset count = %d", got)
	}

	csp := response.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"script-src 'self' 'wasm-unsafe-eval'",
		"worker-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"connect-src 'self'",
		"img-src 'self' data: blob:",
		"font-src 'self' data: blob:",
		"object-src 'none'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("live CSP %q is missing %q", csp, directive)
		}
	}
	if response.Header().Get("X-Frame-Options") != "DENY" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("live security headers = %#v", response.Header())
	}

	controller := request(app, http.MethodGet, liveControllerURL, nil).Body.String()
	for _, behavior := range []string{
		"enableScripting: false",
		"enableXfa: false",
		"isEvalSupported: false",
		"const pageForCandidate = clampPage(",
		"currentPage = pageForCandidate",
		"await documentProxy.getPage(1)",
		"const pagesPromise = pdfViewer.pagesPromise",
		"await installViewerDocument(",
		"if (!installing)",
		"generation === loadGeneration",
		"version === displayedVersion",
		"version === loadingVersion",
		"function matchingZoomPreset(",
		"function updateZoomControl(",
		"zoomCustom.textContent = formatZoomPercent(pdfViewer.currentScale)",
		"currentScaleValue = scaleValue;\n  pdfViewer.currentScaleValue = scaleValue;",
		"zoomPreset.addEventListener('change'",
		"window.location.href = downloadURL",
	} {
		if !strings.Contains(controller, behavior) {
			t.Errorf("live controller is missing behavior %q", behavior)
		}
	}
}
