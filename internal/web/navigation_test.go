package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"open-server/internal/filesystem"
)

func TestDirectoryNavigationFragments(t *testing.T) {
	for _, fixture := range fixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			defer fixture.close()
			app := newTestApp(t, fixture, nil)
			response := request(app, http.MethodGet, "/browse?path="+url.QueryEscape(fixture.root)+"&sort=size&order=desc&hidden=1", nil)
			if response.Code != http.StatusOK {
				t.Fatalf("fragment = %d %s", response.Code, response.Body.String())
			}
			body := response.Body.String()
			for _, id := range []string{"directory-header", "btn-refresh", "regex-filter-controls", "directory-content"} {
				if !strings.Contains(body, `id="`+id+`"`) {
					t.Errorf("fragment missing %s", id)
				}
			}
			for _, forbidden := range []string{"<script", "<html", `id="drop-zone"`, `id="folder-modal"`} {
				if strings.Contains(body, forbidden) {
					t.Errorf("fragment includes persistent shell content %q", forbidden)
				}
			}
			if !strings.Contains(body, "sort=size") || !strings.Contains(body, "order=desc") || !strings.Contains(body, "hidden=1") {
				t.Fatal("navigation state was dropped")
			}
			if response.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(response.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
				t.Fatal("fragment security headers missing")
			}
			for _, test := range []struct {
				path   string
				status int
			}{
				{fixture.root + "/alpha.txt", http.StatusConflict},
				{fixture.root + "/missing", http.StatusNotFound},
				{"/outside", http.StatusForbidden},
			} {
				response := request(app, http.MethodGet, "/browse?path="+url.QueryEscape(test.path), nil)
				if response.Code != test.status {
					t.Errorf("%s = %d, want %d", test.path, response.Code, test.status)
				}
			}
			post := httptest.NewRequest(http.MethodPost, "/browse", nil)
			post.Host = testHost
			post.Header.Set("Origin", "http://"+testHost)
			postResponse := httptest.NewRecorder()
			app.ServeHTTP(postResponse, post)
			if postResponse.Code != http.StatusMethodNotAllowed {
				t.Fatalf("POST fragment = %d", postResponse.Code)
			}
		})
	}
}

func TestDisconnectedInitialPageKeepsBrowserShell(t *testing.T) {
	root := createFixture(t)
	app, err := New(Options{Backend: filesystem.Local{}, Root: root, AllowedHost: testHost})
	if err != nil {
		t.Fatal(err)
	}
	app.SetDisconnected()
	response := request(app, http.MethodGet, "/?path="+url.QueryEscape(root), nil)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "5" {
		t.Fatalf("initial outage = %d %v", response.Code, response.Header())
	}
	for _, want := range []string{`id="btn-refresh"`, `id="navigation-retry"`, `data-load-error="Connection`, `Waiting for the remote connection`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("outage shell missing %q", want)
		}
	}
	fragment := request(app, http.MethodGet, "/browse?path="+url.QueryEscape(root), nil)
	if fragment.Code != http.StatusServiceUnavailable || strings.Contains(fragment.Body.String(), "<html") {
		t.Fatal("outage fragment must retain its retryable error status")
	}
}

func TestBrowserAssetRequiresAuthentication(t *testing.T) {
	root := createFixture(t)
	app, err := New(Options{Backend: filesystem.Local{}, Root: root, AllowedHost: testHost, AccessToken: "private-token"})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/assets/browser-v2.js", "/browse"} {
		response := request(app, http.MethodGet, route, nil)
		if response.Code != http.StatusForbidden {
			t.Fatalf("unauthenticated %s = %d", route, response.Code)
		}
	}
	app.accessToken = ""
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := request(app, method, "/assets/browser-v2.js", nil)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || !strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("asset = %d %v", response.Code, response.Header())
		}
		if method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatal("HEAD asset has a body")
		}
	}
}
