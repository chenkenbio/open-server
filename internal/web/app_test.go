package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"remote-browser/internal/filesystem"
)

const (
	testHost = "127.0.0.1:43210"
)

type backendFixture struct {
	name    string
	backend filesystem.Backend
	root    string
	close   func()
}

func fixtures(t *testing.T) []backendFixture {
	t.Helper()
	localRoot := createFixture(t)
	sftpRoot := createFixture(t)
	client, closeSFTP := sftpPair(t, sftpRoot)
	return []backendFixture{
		{name: "local", backend: filesystem.Local{}, root: localRoot, close: func() {}},
		{name: "sftp", backend: filesystem.SFTP{Client: client}, root: sftpRoot, close: closeSFTP},
	}
}

func createFixture(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "start")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"alpha.txt":        "0123456789",
		"active.html":      "<script>parent.pwned=true</script>",
		"active.js":        "parent.pwned=true",
		"active.svg":       "<svg xmlns=\"http://www.w3.org/2000/svg\"><script>parent.pwned=true</script></svg>",
		"weird <&\".txt":   "escaped",
		"empty.txt":        "",
		"space 日本語 #?.txt": "unicode",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "large.bin"), bytes.Repeat([]byte("L"), 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "folder", "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "folder"), filepath.Join(root, "inside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "alpha.txt"), filepath.Join(root, "inside-file-link.txt")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.txt"), filepath.Join(root, "outside-file-link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken-link")); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(resolvedRoot)
}

func sftpPair(t *testing.T, workingDirectory string) (*sftp.Client, func()) {
	t.Helper()
	clientConnection, serverConnection := net.Pipe()
	server, err := sftp.NewServer(serverConnection, sftp.WithServerWorkingDirectory(workingDirectory))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve() }()
	client, err := sftp.NewClientPipe(clientConnection, clientConnection, sftp.UseConcurrentReads(true), sftp.UseConcurrentWrites(true))
	if err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	return client, func() {
		_ = server.Close()
		_ = serverConnection.Close()
		_ = clientConnection.Close()
		_ = client.Close()
	}
}

func newTestApp(t *testing.T, fixture backendFixture, client *http.Client) *App {
	t.Helper()
	app, err := New(Options{
		Backend: fixture.backend, Root: fixture.root, SSHHost: "lab-<&\"",
		Title: "Remote <files> & \"lab\"", AllowedHost: testHost, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func request(app *App, method, target string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, body)
	r.Host = testHost
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	return w
}

func TestHTTPBehaviorAgainstLocalAndSFTP(t *testing.T) {
	for _, fixture := range fixtures(t) {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			defer fixture.close()
			app := newTestApp(t, fixture, nil)
			rootQuery := url.QueryEscape(fixture.root)

			t.Run("listing escaping breadcrumbs and symlink", func(t *testing.T) {
				response := request(app, http.MethodGet, "/?path="+rootQuery, nil)
				if response.Code != http.StatusOK {
					t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
				}
				body := response.Body.String()
				for _, want := range []string{"alpha.txt", "space 日本語 #?.txt", "inside-link", "inside-file-link.txt", "outside-link", "outside-file-link.txt", "broken-link", "Remote &lt;files&gt; &amp;", "lab-&lt;&amp;&#34;"} {
					if !strings.Contains(body, want) {
						t.Errorf("listing does not contain %q", want)
					}
				}
				if !strings.Contains(body, `var uploadURL = "/upload?path=`) || !strings.Contains(body, `var importURL = "/import?path=`) {
					t.Fatal("rendered transfer endpoints are not root-relative JavaScript strings")
				}
				if strings.Contains(body, "Parent Directory") || !strings.Contains(body, "Name ^") {
					t.Fatal("root listing exposes parent navigation or is missing the active sort marker")
				}
				if !strings.Contains(body, "Root: <strong>"+fixture.root+"</strong>") {
					t.Fatal("listing does not show the confined root")
				}
				if !strings.Contains(body, "addEventListener(&#39;paste&#39;") && !strings.Contains(body, "addEventListener('paste'") {
					t.Fatal("clipboard image upload handler was not rendered")
				}
				if strings.Contains(body, `Remote <files>`) || strings.Contains(body, `weird <&".txt`) {
					t.Fatal("unescaped remote metadata appeared in HTML")
				}
				sortLinks := regexp.MustCompile(`href="([^"]*sort=name[^"]*)"`).FindAllStringSubmatch(body, -1)
				preservesCurrentPath := false
				for _, sortLink := range sortLinks {
					parsedSortLink, parseErr := url.Parse(html.UnescapeString(sortLink[1]))
					if parseErr == nil && parsedSortLink.Query().Get("path") == fixture.root {
						preservesCurrentPath = true
						break
					}
				}
				if !preservesCurrentPath {
					t.Fatal("name-sort links do not preserve the current path")
				}
				insideLink := filepath.ToSlash(filepath.Join(fixture.root, "inside-link"))
				insideResponse := request(app, http.MethodGet, "/?path="+url.QueryEscape(insideLink), nil)
				if insideResponse.Code != http.StatusOK || !strings.Contains(insideResponse.Body.String(), "inside.txt") || !strings.Contains(insideResponse.Body.String(), "Parent Directory") {
					t.Fatalf("navigation through internal symlink = %d %s", insideResponse.Code, insideResponse.Body.String())
				}
				nestedFile := filepath.ToSlash(filepath.Join(fixture.root, "inside-link", "inside.txt"))
				nestedResponse := request(app, http.MethodGet, "/download?path="+url.QueryEscape(nestedFile), nil)
				if nestedResponse.Code != http.StatusOK || nestedResponse.Body.String() != "inside" {
					t.Fatalf("download through directory symlink = %d %q", nestedResponse.Code, nestedResponse.Body.String())
				}
				fileLink := filepath.ToSlash(filepath.Join(fixture.root, "inside-file-link.txt"))
				fileLinkResponse := request(app, http.MethodGet, "/preview?path="+url.QueryEscape(fileLink), nil)
				if fileLinkResponse.Code != http.StatusOK || fileLinkResponse.Body.String() != "0123456789" {
					t.Fatalf("preview through file symlink = %d %q", fileLinkResponse.Code, fileLinkResponse.Body.String())
				}
				outsideLink := filepath.ToSlash(filepath.Join(fixture.root, "outside-link"))
				outsideResponse := request(app, http.MethodGet, "/?path="+url.QueryEscape(outsideLink), nil)
				if outsideResponse.Code != http.StatusOK || !strings.Contains(outsideResponse.Body.String(), "outside.txt") {
					t.Fatalf("navigation through external symlink = %d %s", outsideResponse.Code, outsideResponse.Body.String())
				}
				for _, match := range regexp.MustCompile(`href="([^"]+)"`).FindAllStringSubmatch(outsideResponse.Body.String(), -1) {
					parsed, parseErr := url.Parse(html.UnescapeString(match[1]))
					linkedPath := parsed.Query().Get("path")
					if parseErr == nil && linkedPath != "" && !withinRoot(fixture.root, linkedPath) {
						t.Fatalf("symlink listing generated an outside-root navigation URL %q", linkedPath)
					}
				}
				outsideNested := filepath.ToSlash(filepath.Join(fixture.root, "outside-link", "outside.txt"))
				outsideNestedResponse := request(app, http.MethodGet, "/download?path="+url.QueryEscape(outsideNested), nil)
				if outsideNestedResponse.Code != http.StatusOK || outsideNestedResponse.Body.String() != "outside" {
					t.Fatalf("download through external directory symlink = %d %q", outsideNestedResponse.Code, outsideNestedResponse.Body.String())
				}
				outsideFileLink := filepath.ToSlash(filepath.Join(fixture.root, "outside-file-link.txt"))
				outsideFileResponse := request(app, http.MethodGet, "/preview?path="+url.QueryEscape(outsideFileLink), nil)
				if outsideFileResponse.Code != http.StatusOK || outsideFileResponse.Body.String() != "outside" {
					t.Fatalf("preview through external file symlink = %d %q", outsideFileResponse.Code, outsideFileResponse.Body.String())
				}
			})

			t.Run("full and ranged downloads", func(t *testing.T) {
				name := filepath.ToSlash(filepath.Join(fixture.root, "alpha.txt"))
				full := request(app, http.MethodGet, "/download?path="+url.QueryEscape(name), nil)
				if full.Code != http.StatusOK || full.Body.String() != "0123456789" {
					t.Fatalf("full response = %d %q", full.Code, full.Body.String())
				}
				r := httptest.NewRequest(http.MethodGet, "/download?path="+url.QueryEscape(name), nil)
				r.Host = testHost
				r.Header.Set("Range", "bytes=2-5")
				w := httptest.NewRecorder()
				app.ServeHTTP(w, r)
				if w.Code != http.StatusPartialContent || w.Body.String() != "2345" || w.Header().Get("Content-Range") != "bytes 2-5/10" {
					t.Fatalf("range response = %d %q, Content-Range %q", w.Code, w.Body.String(), w.Header().Get("Content-Range"))
				}
				emptyName := filepath.ToSlash(filepath.Join(fixture.root, "empty.txt"))
				empty := request(app, http.MethodGet, "/download?path="+url.QueryEscape(emptyName), nil)
				if empty.Code != http.StatusOK || empty.Body.Len() != 0 {
					t.Fatalf("empty download = %d with %d bytes", empty.Code, empty.Body.Len())
				}
				largeName := filepath.ToSlash(filepath.Join(fixture.root, "large.bin"))
				largeRequest := httptest.NewRequest(http.MethodGet, "/download?path="+url.QueryEscape(largeName), nil)
				largeRequest.Host = testHost
				largeRequest.Header.Set("Range", "bytes=2097148-2097151")
				largeResponse := httptest.NewRecorder()
				app.ServeHTTP(largeResponse, largeRequest)
				if largeResponse.Code != http.StatusPartialContent || largeResponse.Body.String() != "LLLL" {
					t.Fatalf("large ranged download = %d %q", largeResponse.Code, largeResponse.Body.String())
				}
				brokenName := filepath.ToSlash(filepath.Join(fixture.root, "broken-link"))
				broken := request(app, http.MethodGet, "/download?path="+url.QueryEscape(brokenName), nil)
				if broken.Code != http.StatusNotFound {
					t.Fatalf("broken-link download = %d %s", broken.Code, broken.Body.String())
				}
			})

			t.Run("sorting", func(t *testing.T) {
				response := request(app, http.MethodGet, "/?path="+rootQuery+"&sort=size&order=desc", nil)
				body := response.Body.String()
				folderPosition, largePosition, alphaPosition := strings.Index(body, "folder/</a>"), strings.Index(body, "large.bin</a>"), strings.Index(body, "alpha.txt</a>")
				if response.Code != http.StatusOK || folderPosition < 0 || largePosition < 0 || alphaPosition < 0 || folderPosition > largePosition || largePosition > alphaPosition {
					t.Fatalf("unexpected descending size order: folder=%d large=%d alpha=%d", folderPosition, largePosition, alphaPosition)
				}
			})

			t.Run("safe and active preview headers", func(t *testing.T) {
				textName := filepath.ToSlash(filepath.Join(fixture.root, "alpha.txt"))
				textResponse := request(app, http.MethodGet, "/preview?path="+url.QueryEscape(textName), nil)
				if !strings.HasPrefix(textResponse.Header().Get("Content-Disposition"), "inline") {
					t.Fatalf("text disposition = %q", textResponse.Header().Get("Content-Disposition"))
				}
				for _, activeName := range []string{"active.html", "active.svg", "active.js"} {
					activePath := filepath.ToSlash(filepath.Join(fixture.root, activeName))
					activeResponse := request(app, http.MethodGet, "/preview?path="+url.QueryEscape(activePath), nil)
					if !strings.HasPrefix(activeResponse.Header().Get("Content-Disposition"), "attachment") || activeResponse.Header().Get("X-Content-Type-Options") != "nosniff" {
						t.Fatalf("%s response headers = %#v", activeName, activeResponse.Header())
					}
				}
			})

			t.Run("upload conflict and overwrite", func(t *testing.T) {
				response := uploadRequest(t, app, fixture.root, "new file.txt", "new contents", false, true)
				if response.Code != http.StatusCreated {
					t.Fatalf("new upload = %d %s", response.Code, response.Body.String())
				}
				conflict := uploadRequest(t, app, fixture.root, "alpha.txt", "destroyed", false, true)
				if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "requires_confirmation") {
					t.Fatalf("conflict = %d %s", conflict.Code, conflict.Body.String())
				}
				contents, _ := os.ReadFile(filepath.Join(filepath.FromSlash(fixture.root), "alpha.txt"))
				if string(contents) != "0123456789" {
					t.Fatalf("conflicting upload changed destination to %q", contents)
				}
				overwrite := uploadRequest(t, app, fixture.root, "alpha.txt", "replacement", true, true)
				if overwrite.Code != http.StatusCreated {
					t.Fatalf("overwrite = %d %s", overwrite.Code, overwrite.Body.String())
				}
				contents, _ = os.ReadFile(filepath.Join(filepath.FromSlash(fixture.root), "alpha.txt"))
				if string(contents) != "replacement" {
					t.Fatalf("overwrite contents = %q", contents)
				}
				directoryOverwrite := uploadRequest(t, app, fixture.root, "folder", "must not replace a directory", true, true)
				if directoryOverwrite.Code != http.StatusBadRequest {
					t.Fatalf("directory overwrite = %d %s", directoryOverwrite.Code, directoryOverwrite.Body.String())
				}
				insideLink := filepath.ToSlash(filepath.Join(fixture.root, "inside-link"))
				linkedUpload := uploadRequest(t, app, insideLink, "linked-upload.txt", "inside root", false, true)
				if linkedUpload.Code != http.StatusCreated {
					t.Fatalf("upload through internal symlink = %d %s", linkedUpload.Code, linkedUpload.Body.String())
				}
				linkedContents, linkedErr := os.ReadFile(filepath.Join(filepath.FromSlash(fixture.root), "folder", "linked-upload.txt"))
				if linkedErr != nil || string(linkedContents) != "inside root" {
					t.Fatalf("internal symlink upload contents = %q, %v", linkedContents, linkedErr)
				}
				outsideLink := filepath.ToSlash(filepath.Join(fixture.root, "outside-link"))
				linkedOutsideUpload := uploadRequest(t, app, outsideLink, "linked-outside.txt", "followed", false, true)
				if linkedOutsideUpload.Code != http.StatusCreated {
					t.Fatalf("upload through external symlink = %d %s", linkedOutsideUpload.Code, linkedOutsideUpload.Body.String())
				}
				outsideContents, outsideErr := os.ReadFile(filepath.Join(filepath.Dir(filepath.FromSlash(fixture.root)), "outside", "linked-outside.txt"))
				if outsideErr != nil || string(outsideContents) != "followed" {
					t.Fatalf("external symlink upload contents = %q, %v", outsideContents, outsideErr)
				}
			})

			t.Run("canceled upload cleanup", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				destination := filepath.ToSlash(filepath.Join(fixture.root, "canceled.bin"))
				if _, err := fixture.backend.Upload(ctx, destination, strings.NewReader(strings.Repeat("x", 1<<20)), false); !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled upload error = %v", err)
				}
				deadline := time.Now().Add(2 * time.Second)
				for {
					_, destinationErr := os.Stat(filepath.FromSlash(destination))
					temporary, globErr := filepath.Glob(filepath.Join(filepath.FromSlash(fixture.root), ".remote-browser-upload-*"))
					if os.IsNotExist(destinationErr) && globErr == nil && len(temporary) == 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("canceled upload left destination or temporary files: destination=%v temporary=%v glob=%v", destinationErr, temporary, globErr)
					}
					time.Sleep(10 * time.Millisecond)
				}
			})

			t.Run("atomic no-clobber upload", func(t *testing.T) {
				destination := filepath.ToSlash(filepath.Join(fixture.root, "same-name.txt"))
				results := make(chan error, 8)
				var group sync.WaitGroup
				for i := 0; i < 8; i++ {
					group.Add(1)
					go func(index int) {
						defer group.Done()
						_, err := fixture.backend.Upload(context.Background(), destination, strings.NewReader(fmt.Sprintf("complete-%d", index)), false)
						results <- err
					}(i)
				}
				group.Wait()
				close(results)
				successes := 0
				for err := range results {
					if err == nil {
						successes++
					} else if !errors.Is(err, filesystem.ErrExists) {
						t.Errorf("same-name upload error = %v", err)
					}
				}
				contents, err := os.ReadFile(filepath.FromSlash(destination))
				if err != nil || successes != 1 || !strings.HasPrefix(string(contents), "complete-") {
					t.Fatalf("same-name result: successes=%d contents=%q error=%v", successes, contents, err)
				}
			})
		})
	}
}

func TestDirectoryTemplateMatchesOpenServerUploadWorkflow(t *testing.T) {
	pathIndex := strings.Index(directoryTemplate, `Path: {{range`)
	uploadIndex := strings.Index(directoryTemplate, `id="drop-zone"`)
	headerIndex := strings.Index(directoryTemplate, `<tr><th align="left"`)
	if pathIndex == -1 || uploadIndex == -1 || headerIndex == -1 || !(pathIndex < uploadIndex && uploadIndex < headerIndex) {
		t.Fatal("upload frame must appear between the path line and directory table")
	}
	for _, want := range []string{
		`action="{{.UploadURL}}" method="POST" enctype="multipart/form-data"`,
		`id="btn-upload-files"`,
		`id="btn-paste-image"`,
		`id="btn-from-url"`,
		`id="conflict-modal"`,
		`Apply this choice to all remaining conflicts`,
		`id="paste-modal"`,
		`function armPasteCapture()`,
		`document.addEventListener('paste', onPaste)`,
		`xhr.upload.addEventListener('progress'`,
		`dz.addEventListener('drop'`,
	} {
		if !strings.Contains(directoryTemplate, want) {
			t.Errorf("directory template is missing %q", want)
		}
	}
}

func TestLocalURLHasNoPathToken(t *testing.T) {
	fixture := backendFixture{backend: filesystem.Local{}, root: createFixture(t)}
	app := newTestApp(t, fixture, nil)
	if got, want := app.URL(), "http://"+testHost+"/"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestWithinRootUsesPathComponentBoundaries(t *testing.T) {
	for _, test := range []struct {
		root string
		path string
		want bool
	}{
		{root: "/data/project", path: "/data/project", want: true},
		{root: "/data/project", path: "/data/project/results", want: true},
		{root: "/data/project", path: "/data/project-sibling", want: false},
		{root: "/data/project", path: "/data", want: false},
		{root: "/", path: "/data/project", want: true},
		{root: "/", path: "relative", want: false},
	} {
		if got := withinRoot(test.root, test.path); got != test.want {
			t.Errorf("withinRoot(%q, %q) = %v, want %v", test.root, test.path, got, test.want)
		}
	}
}

func TestConfiguredSymlinkRootRemainsLogical(t *testing.T) {
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real-root")
	logicalRoot := filepath.Join(parent, "logical-root")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "note.txt"), []byte("followed root"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, logicalRoot); err != nil {
		t.Fatal(err)
	}
	app := newTestApp(t, backendFixture{backend: filesystem.Local{}, root: filepath.ToSlash(logicalRoot)}, nil)
	listing := request(app, http.MethodGet, "/", nil)
	if listing.Code != http.StatusOK || !strings.Contains(listing.Body.String(), "Root: <strong>"+filepath.ToSlash(logicalRoot)+"</strong>") || !strings.Contains(listing.Body.String(), "note.txt") {
		t.Fatalf("logical symlink root listing = %d %s", listing.Code, listing.Body.String())
	}
	logicalFile := filepath.ToSlash(filepath.Join(logicalRoot, "note.txt"))
	download := request(app, http.MethodGet, "/download?path="+url.QueryEscape(logicalFile), nil)
	if download.Code != http.StatusOK || download.Body.String() != "followed root" {
		t.Fatalf("logical symlink root download = %d %q", download.Code, download.Body.String())
	}
	physicalFile := filepath.ToSlash(filepath.Join(realRoot, "note.txt"))
	directPhysical := request(app, http.MethodGet, "/download?path="+url.QueryEscape(physicalFile), nil)
	if directPhysical.Code != http.StatusForbidden {
		t.Fatalf("direct physical target status = %d body = %s", directPhysical.Code, directPhysical.Body.String())
	}
}

func TestSecurityChecks(t *testing.T) {
	fixtureSet := fixtures(t)
	defer closeFixtures(fixtureSet)
	fixture := fixtureSet[0]
	app := newTestApp(t, fixture, nil)
	rootQuery := url.QueryEscape(fixture.root)

	unknownRoute := request(app, http.MethodGet, "/wrong?path="+rootQuery, nil)
	if unknownRoute.Code != http.StatusNotFound {
		t.Fatalf("unknown route status = %d", unknownRoute.Code)
	}

	r := httptest.NewRequest(http.MethodGet, "/?path="+rootQuery, nil)
	r.Host = "attacker.example"
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("invalid Host status = %d", w.Code)
	}

	traversal := request(app, http.MethodGet, "/?path="+url.QueryEscape(fixture.root+"/../outside"), nil)
	if traversal.Code != http.StatusBadRequest {
		t.Fatalf("traversal status = %d body = %s", traversal.Code, traversal.Body.String())
	}
	outsideDirectory := filepath.ToSlash(filepath.Join(filepath.Dir(filepath.FromSlash(fixture.root)), "outside"))
	outsideFile := filepath.ToSlash(filepath.Join(filepath.FromSlash(outsideDirectory), "outside.txt"))
	for _, route := range []string{"/", "/download", "/preview"} {
		outside := request(app, http.MethodGet, route+"?path="+url.QueryEscape(outsideFile), nil)
		if outside.Code != http.StatusForbidden || !strings.Contains(outside.Body.String(), "outside the configured root") {
			t.Fatalf("direct outside request %s = %d %s", route, outside.Code, outside.Body.String())
		}
	}
	prefixSibling := request(app, http.MethodGet, "/?path="+url.QueryEscape(fixture.root+"-sibling"), nil)
	if prefixSibling.Code != http.StatusForbidden {
		t.Fatalf("sibling-prefix path status = %d body = %s", prefixSibling.Code, prefixSibling.Body.String())
	}
	outsideUpload := uploadRequest(t, app, outsideDirectory, "outside-upload.txt", "blocked", false, true)
	if outsideUpload.Code != http.StatusForbidden {
		t.Fatalf("direct outside upload = %d %s", outsideUpload.Code, outsideUpload.Body.String())
	}
	outsideImport := importRequest(app, outsideDirectory, url.Values{"url": {"https://files.example/outside.bin"}}, false)
	if outsideImport.Code != http.StatusForbidden {
		t.Fatalf("direct outside import = %d %s", outsideImport.Code, outsideImport.Body.String())
	}

	noOrigin := uploadRequest(t, app, fixture.root, "blocked.txt", "blocked", false, false)
	if noOrigin.Code != http.StatusForbidden {
		t.Fatalf("missing Origin status = %d", noOrigin.Code)
	}
	if _, err := os.Stat(filepath.Join(filepath.FromSlash(fixture.root), "blocked.txt")); !os.IsNotExist(err) {
		t.Fatal("upload without Origin changed the filesystem")
	}

	body, contentType := multipartBody(t, "blocked.txt", "blocked")
	badOriginRequest := httptest.NewRequest(http.MethodPost, "/upload?path="+rootQuery, body)
	badOriginRequest.Host = testHost
	badOriginRequest.Header.Set("Content-Type", contentType)
	badOriginRequest.Header.Set("Origin", "http://attacker.example")
	badOriginResponse := httptest.NewRecorder()
	app.ServeHTTP(badOriginResponse, badOriginRequest)
	if badOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", badOriginResponse.Code)
	}
	methodRequest := httptest.NewRequest(http.MethodPut, "/?path="+rootQuery, nil)
	methodRequest.Host = testHost
	methodRequest.Header.Set("Origin", "http://"+testHost)
	methodResponse := httptest.NewRecorder()
	app.ServeHTTP(methodResponse, methodRequest)
	if methodResponse.Code != http.StatusMethodNotAllowed || methodResponse.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("method response = %d Allow %q", methodResponse.Code, methodResponse.Header().Get("Allow"))
	}
}

func TestURLImportAndConcurrentTransfers(t *testing.T) {
	for _, fixture := range fixtures(t) {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			defer fixture.close()
			var fetches atomic.Int32
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				fetches.Add(1)
				if request.URL.Host != "files.example" {
					return nil, fmt.Errorf("unexpected host")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("fetched locally")), Request: request}, nil
			})
			app := newTestApp(t, fixture, &http.Client{Transport: transport})
			invalidURL := importRequest(app, fixture.root, url.Values{"url": {"ftp://files.example/data.bin"}}, false)
			if invalidURL.Code != http.StatusBadRequest || !strings.Contains(invalidURL.Body.String(), "http or https") {
				t.Fatalf("invalid URL response = %d %s", invalidURL.Code, invalidURL.Body.String())
			}
			traversalName := importRequest(app, fixture.root, url.Values{"url": {"https://files.example/data.bin"}, "filename": {"../escaped.bin"}}, false)
			if traversalName.Code != http.StatusBadRequest || !strings.Contains(traversalName.Body.String(), "traversal") || fetches.Load() != 0 {
				t.Fatalf("traversal filename response = %d %s, fetch count = %d", traversalName.Code, traversalName.Body.String(), fetches.Load())
			}

			conflictForm := url.Values{"url": {"https://files.example/data.bin"}, "filename": {"alpha.txt"}}
			conflict := importRequest(app, fixture.root, conflictForm, false)
			if conflict.Code != http.StatusConflict || fetches.Load() != 0 {
				t.Fatalf("pre-fetch conflict = %d, fetch count = %d", conflict.Code, fetches.Load())
			}
			overwrite := importRequest(app, fixture.root, conflictForm, true)
			if overwrite.Code != http.StatusCreated || fetches.Load() != 1 {
				t.Fatalf("confirmed import = %d, fetch count = %d, body = %s", overwrite.Code, fetches.Load(), overwrite.Body.String())
			}

			form := url.Values{"url": {"https://files.example/data.bin"}, "filename": {"imported # 日本語.bin"}}
			response := importRequest(app, fixture.root, form, false)
			if response.Code != http.StatusCreated {
				t.Fatalf("import status = %d body = %s", response.Code, response.Body.String())
			}
			contents, err := os.ReadFile(filepath.Join(filepath.FromSlash(fixture.root), "imported # 日本語.bin"))
			if err != nil || string(contents) != "fetched locally" {
				t.Fatalf("imported contents = %q, %v", contents, err)
			}

			var group sync.WaitGroup
			errorsChannel := make(chan error, 12)
			for i := 0; i < 6; i++ {
				group.Add(2)
				go func(index int) {
					defer group.Done()
					response := request(app, http.MethodGet, "/download?path="+url.QueryEscape(filepath.ToSlash(filepath.Join(fixture.root, "alpha.txt"))), nil)
					if response.Code != http.StatusOK {
						errorsChannel <- fmt.Errorf("download %d status %d", index, response.Code)
					}
				}(i)
				go func(index int) {
					defer group.Done()
					response := uploadRequest(t, app, fixture.root, fmt.Sprintf("concurrent-%d.txt", index), strings.Repeat("x", 64<<10), false, true)
					if response.Code != http.StatusCreated {
						errorsChannel <- fmt.Errorf("upload %d status %d: %s", index, response.Code, response.Body.String())
					}
				}(i)
			}
			group.Wait()
			close(errorsChannel)
			for err := range errorsChannel {
				t.Error(err)
			}
		})
	}
}

func importRequest(app *App, root string, form url.Values, overwrite bool) *httptest.ResponseRecorder {
	target := "/import?path=" + url.QueryEscape(root)
	if overwrite {
		target += "&overwrite=1"
	}
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Host = testHost
	r.Header.Set("Origin", "http://"+testHost)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	return w
}

func closeFixtures(fixtures []backendFixture) {
	for _, fixture := range fixtures {
		fixture.close()
	}
}

func TestBackendErrorsAreSanitized(t *testing.T) {
	root := createFixture(t)
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantText   string
	}{
		{name: "permission", err: fs.ErrPermission, wantStatus: http.StatusForbidden, wantText: "permission was denied"},
		{name: "disconnect", err: errors.New("connection lost: secret backend detail"), wantStatus: http.StatusBadGateway, wantText: "remote filesystem operation failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newTestApp(t, backendFixture{backend: statErrorBackend{Backend: filesystem.Local{}, err: test.err}, root: root}, nil)
			response := request(app, http.MethodGet, "/?path="+url.QueryEscape(root), nil)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantText) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "secret backend detail") {
				t.Fatal("backend diagnostic leaked into HTTP response")
			}
		})
	}
}

func uploadRequest(t *testing.T, app *App, root, filename, contents string, overwrite, origin bool) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := multipartBody(t, filename, contents)
	target := "/upload?path=" + url.QueryEscape(root)
	if overwrite {
		target += "&overwrite=1"
	}
	r := httptest.NewRequest(http.MethodPost, target, body)
	r.Host = testHost
	r.Header.Set("Content-Type", contentType)
	if origin {
		r.Header.Set("Origin", "http://"+testHost)
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, r)
	return w
}

func multipartBody(t *testing.T, filename, contents string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type statErrorBackend struct {
	filesystem.Backend
	err error
}

func (b statErrorBackend) Stat(context.Context, string) (fs.FileInfo, error) {
	return nil, b.err
}
