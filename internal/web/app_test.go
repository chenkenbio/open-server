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

	"open-server/internal/filesystem"
	"open-server/internal/tensorboard"
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
		"config.xml":       "<?xml version=\"1.0\"?><root>value</root>",
		"data.csv":         "name,value\nalpha,1\n",
		"data.tsv":         "name\tvalue\nalpha\t1\n",
		"figure.eps":       "%!PS-Adobe-3.0 EPSF-3.0\n%%BoundingBox: 0 0 1 1\n",
		"paper.tex":        "\\documentclass{article}\n",
		"README":           "extensionless text\n",
		"preview.pdf":      "%PDF-1.4\n%%EOF\n",
		"pixel.png":        "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR",
		"binary.dat":       "\x00\x01\x02\x03\xff",
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
				if strings.Contains(body, `>..</a>`) || !strings.Contains(body, "Name ^") {
					t.Fatal("root listing exposes parent navigation or is missing the active sort marker")
				}
				if !strings.Contains(body, `class="site-name">open-server</span>`) {
					t.Fatal("listing does not show the compact application header")
				}
				if !strings.Contains(body, `class="root-path">`+fixture.root+`</span>`) {
					t.Fatal("listing does not show the session root path")
				}
				for _, want := range []string{`<th align="right">Path</th>`, `class="copy-path icon-button"`, `aria-label="Copy path"`, `title="Copy path"`, `class="download-file icon-button"`, `aria-label="Download alpha.txt"`, `<svg viewBox="0 0 24 24" aria-hidden="true"`} {
					if !strings.Contains(body, want) {
						t.Errorf("compact file actions are missing %q", want)
					}
				}
				if strings.Contains(body, ">Path</button>") || strings.Contains(body, ">Copy path</button>") || strings.Contains(body, ">Download</button>") {
					t.Fatal("listing still contains verbose row action text")
				}
				if !strings.Contains(body, "addEventListener(&#39;paste&#39;") && !strings.Contains(body, "addEventListener('paste'") {
					t.Fatal("clipboard file upload handler was not rendered")
				}
				if strings.Contains(body, `Remote <files>`) || strings.Contains(body, `weird <&".txt`) {
					t.Fatal("unescaped remote metadata appeared in HTML")
				}
				for _, fileName := range []string{"README", "active.html", "binary.dat", "data.tsv"} {
					match := regexp.MustCompile(`href="([^"]+)">` + regexp.QuoteMeta(fileName) + `</a>`).FindStringSubmatch(body)
					if len(match) != 2 {
						t.Fatalf("listing is missing the link for %s", fileName)
					}
					linked, parseErr := url.Parse(html.UnescapeString(match[1]))
					if parseErr != nil || linked.Path != "/preview" {
						t.Fatalf("%s link = %q, parsed as %#v, %v", fileName, match[1], linked, parseErr)
					}
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
				if insideResponse.Code != http.StatusOK || !strings.Contains(insideResponse.Body.String(), "inside.txt") || !strings.Contains(insideResponse.Body.String(), `>..</a>`) {
					t.Fatalf("navigation through internal symlink = %d %s", insideResponse.Code, insideResponse.Body.String())
				}
				tableBodyIndex := strings.Index(insideResponse.Body.String(), `<tbody>`)
				if tableBodyIndex == -1 {
					t.Fatal("nested listing is missing the directory table body")
				}
				tableBody := insideResponse.Body.String()[tableBodyIndex:]
				parentIndex := strings.Index(tableBody, `>..</a>`)
				currentIndex := strings.Index(tableBody, `>.</a>`)
				if parentIndex == -1 || currentIndex == -1 || parentIndex >= currentIndex {
					t.Fatal("parent directory must appear above the current directory")
				}
				rootLink := regexp.MustCompile(`<a href="([^"]+)" title="Session root">\.</a>`).FindStringSubmatch(insideResponse.Body.String())
				if len(rootLink) != 2 {
					t.Fatal("nested listing is missing the confined-root breadcrumb link")
				}
				parsedRootLink, parseRootErr := url.Parse(html.UnescapeString(rootLink[1]))
				if parseRootErr != nil || parsedRootLink.Query().Get("path") != fixture.root {
					t.Fatalf("confined-root breadcrumb = %q, parsed as %#v, %v", rootLink[1], parsedRootLink, parseRootErr)
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

			t.Run("preview MIME security and streaming", func(t *testing.T) {
				textFiles := map[string]string{
					"alpha.txt":   "0123456789",
					"active.html": "<script>parent.pwned=true</script>",
					"active.js":   "parent.pwned=true",
					"active.svg":  "<svg xmlns=\"http://www.w3.org/2000/svg\"><script>parent.pwned=true</script></svg>",
					"config.xml":  "<?xml version=\"1.0\"?><root>value</root>",
					"data.csv":    "name,value\nalpha,1\n",
					"data.tsv":    "name\tvalue\nalpha\t1\n",
					"figure.eps":  "%!PS-Adobe-3.0 EPSF-3.0\n%%BoundingBox: 0 0 1 1\n",
					"paper.tex":   "\\documentclass{article}\n",
					"README":      "extensionless text\n",
				}
				for fileName, contents := range textFiles {
					fileName, contents := fileName, contents
					t.Run(fileName, func(t *testing.T) {
						filePath := filepath.ToSlash(filepath.Join(fixture.root, fileName))
						response := request(app, http.MethodGet, "/preview?path="+url.QueryEscape(filePath), nil)
						if response.Code != http.StatusOK || response.Body.String() != contents {
							t.Fatalf("preview = %d %q", response.Code, response.Body.String())
						}
						if response.Header().Get("Content-Type") != "text/plain; charset=utf-8" || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "inline") {
							t.Fatalf("preview headers = %#v", response.Header())
						}
						csp := response.Header().Get("Content-Security-Policy")
						for _, policy := range []string{"sandbox", "default-src 'none'", "script-src 'none'", "object-src 'none'", "form-action 'none'", "frame-ancestors 'none'"} {
							if !strings.Contains(csp, policy) {
								t.Errorf("preview CSP %q is missing %q", csp, policy)
							}
						}
						if strings.Contains(csp, "unsafe-inline") || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
							t.Fatalf("unsafe preview headers = %#v", response.Header())
						}
					})
				}

				for _, media := range []struct {
					name        string
					contentType string
				}{
					{name: "pixel.png", contentType: "image/png"},
					{name: "preview.pdf", contentType: "application/pdf"},
				} {
					mediaPath := filepath.ToSlash(filepath.Join(fixture.root, media.name))
					response := request(app, http.MethodGet, "/preview?path="+url.QueryEscape(mediaPath), nil)
					if response.Code != http.StatusOK || response.Header().Get("Content-Type") != media.contentType || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "inline") {
						t.Errorf("%s preview = %d headers %#v", media.name, response.Code, response.Header())
					}
				}

				binaryPath := filepath.ToSlash(filepath.Join(fixture.root, "binary.dat"))
				binaryResponse := request(app, http.MethodGet, "/preview?path="+url.QueryEscape(binaryPath), nil)
				if binaryResponse.Code != http.StatusOK || binaryResponse.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(binaryResponse.Header().Get("Content-Disposition"), "attachment") || binaryResponse.Body.String() != "\x00\x01\x02\x03\xff" {
					t.Fatalf("binary preview fallback = %d headers %#v body %q", binaryResponse.Code, binaryResponse.Header(), binaryResponse.Body.String())
				}

				csvPath := filepath.ToSlash(filepath.Join(fixture.root, "data.csv"))
				csvDownload := request(app, http.MethodGet, "/download?path="+url.QueryEscape(csvPath), nil)
				if csvDownload.Header().Get("Content-Type") != "text/csv; charset=utf-8" || !strings.HasPrefix(csvDownload.Header().Get("Content-Disposition"), "attachment") {
					t.Fatalf("CSV download headers = %#v", csvDownload.Header())
				}

				textPath := filepath.ToSlash(filepath.Join(fixture.root, "alpha.txt"))
				rangeRequest := httptest.NewRequest(http.MethodGet, "/preview?path="+url.QueryEscape(textPath), nil)
				rangeRequest.Host = testHost
				rangeRequest.Header.Set("Range", "bytes=2-5")
				rangeResponse := httptest.NewRecorder()
				app.ServeHTTP(rangeResponse, rangeRequest)
				if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "2345" || rangeResponse.Header().Get("Content-Range") != "bytes 2-5/10" {
					t.Fatalf("preview range = %d %q Content-Range %q", rangeResponse.Code, rangeResponse.Body.String(), rangeResponse.Header().Get("Content-Range"))
				}
				headResponse := request(app, http.MethodHead, "/preview?path="+url.QueryEscape(textPath), nil)
				if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 || headResponse.Header().Get("Content-Length") != "10" || headResponse.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
					t.Fatalf("preview HEAD = %d headers %#v body length %d", headResponse.Code, headResponse.Header(), headResponse.Body.Len())
				}

				directPath := filepath.ToSlash(filepath.Join(fixture.root, "README"))
				directResponse := request(app, http.MethodGet, "/?path="+url.QueryEscape(directPath), nil)
				if directResponse.Code != http.StatusOK || directResponse.Header().Get("Content-Type") != "text/plain; charset=utf-8" || !strings.Contains(directResponse.Header().Get("Content-Security-Policy"), "sandbox") {
					t.Fatalf("direct file preview = %d headers %#v", directResponse.Code, directResponse.Header())
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
					temporary, globErr := filepath.Glob(filepath.Join(filepath.FromSlash(fixture.root), ".open-server-upload-*"))
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
	pathIndex := strings.Index(directoryTemplate, `class="header-path"`)
	uploadIndex := strings.Index(directoryTemplate, `id="drop-zone"`)
	headerIndex := strings.Index(directoryTemplate, `<tr><th align="left"`)
	if pathIndex == -1 || uploadIndex == -1 || headerIndex == -1 || !(pathIndex < uploadIndex && uploadIndex < headerIndex) {
		t.Fatal("upload frame must appear between the path line and directory table")
	}
	for _, want := range []string{
		`action="{{.UploadURL}}" method="POST" enctype="multipart/form-data"`,
		`id="btn-upload-files"`,
		`id="btn-paste-file"`,
		`id="btn-from-url"`,
		`id="conflict-modal"`,
		`Apply this choice to all remaining conflicts`,
		`id="paste-modal"`,
		`function armPasteCapture()`,
		`document.addEventListener('paste', onPaste)`,
		`e.clipboardData && e.clipboardData.files`,
		`items[index].kind === 'file'`,
		`pasteName.value = file.name || defaultPasteName(file.type)`,
		`xhr.upload.addEventListener('progress'`,
		`dz.addEventListener('drop'`,
		`class="upload-hint">or drag files here</span>`,
		`id="copy-status" class="visually-hidden" role="status" aria-live="polite"`,
		`.copy-success::after { color: #555; content: "\2713"; }`,
		`button.classList.add('copy-result', succeeded ? 'copy-success' : 'copy-failure')`,
		`copyStatus.textContent = succeeded ? 'Copied to clipboard.' : 'Copy failed.'`,
		`}, 300);`,
		`tbody tr:hover td`,
		`class="page-header"`,
	} {
		if !strings.Contains(directoryTemplate, want) {
			t.Errorf("directory template is missing %q", want)
		}
	}
	if strings.Contains(directoryTemplate, `items[index].type.indexOf('image/')`) {
		t.Error("paste upload must not reject non-image clipboard files")
	}
	if strings.Contains(directoryTemplate, `border: 2px dashed #999`) {
		t.Error("upload toolbar still contains the old always-visible drop-zone border")
	}
	if strings.Contains(directoryTemplate, `showCopyResult(button, 'Copied')`) {
		t.Error("copy actions still replace icons with visible Copied text")
	}
}

func TestDirectoryFontSizeAndDensity(t *testing.T) {
	root := createFixture(t)
	for _, test := range []struct {
		name     string
		fontSize int
		want     string
	}{
		{name: "default", want: "14px"},
		{name: "custom", fontSize: 18, want: "18px"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, err := New(Options{
				Backend: filesystem.Local{}, Root: root, SSHHost: "lab",
				Title: "Files", AllowedHost: testHost, FontSize: test.fontSize,
			})
			if err != nil {
				t.Fatal(err)
			}
			response := request(app, http.MethodGet, "/", nil)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			body := response.Body.String()
			if !strings.Contains(body, "font-size: "+test.want+"; }") {
				t.Fatalf("directory CSS does not contain font size %s", test.want)
			}
			if !strings.Contains(body, "button, input, select, textarea { font-size: inherit; }") {
				t.Fatal("form controls do not inherit the configured font size")
			}
		})
	}
	for _, want := range []string{
		"table { width: 100%; border-collapse: collapse; font-size: 0.95em; }",
		"th { padding: 0.23em 0.5em;",
		"td { padding: 0.21em 0.5em;",
		".icon-button { padding: 0.15em;",
		"width: 1.3em; height: 1.3em;",
	} {
		if !strings.Contains(directoryTemplate, want) {
			t.Errorf("directory template is missing compact font/height rule %q", want)
		}
	}

	for _, fontSize := range []int{7, 73} {
		_, err := New(Options{
			Backend: filesystem.Local{}, Root: root, SSHHost: "lab",
			Title: "Files", AllowedHost: testHost, FontSize: fontSize,
		})
		if err == nil || !strings.Contains(err.Error(), "font size must be between 8 and 72 pixels") {
			t.Errorf("New with font size %d error = %v", fontSize, err)
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

func TestAccessTokenBecomesScopedCookie(t *testing.T) {
	root := createFixture(t)
	app, err := New(Options{
		Backend: filesystem.Local{}, Root: root, SSHHost: "lab",
		Title: "Files", AllowedHost: testHost, AccessToken: "secret-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := app.URL(); got != "http://"+testHost+"/?token=secret-token" {
		t.Fatalf("token URL = %q", got)
	}
	missing := request(app, http.MethodGet, "/", nil)
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing token status = %d", missing.Code)
	}
	initial := request(app, http.MethodGet, "/?token=secret-token", nil)
	if initial.Code != http.StatusSeeOther || initial.Header().Get("Location") != "/" {
		t.Fatalf("initial token response = %d Location %q", initial.Code, initial.Header().Get("Location"))
	}
	cookies := initial.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Value != "secret-token" {
		t.Fatalf("auth cookies = %#v", cookies)
	}
	follow := httptest.NewRequest(http.MethodGet, "/", nil)
	follow.Host = testHost
	follow.AddCookie(cookies[0])
	followResponse := httptest.NewRecorder()
	app.ServeHTTP(followResponse, follow)
	if followResponse.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated response = %d", followResponse.Code)
	}
	for _, warning := range []string{"Security warning:", "token-protected plain HTTP", "traffic is not encrypted"} {
		if !strings.Contains(followResponse.Body.String(), warning) {
			t.Errorf("serve-mode listing is missing %q", warning)
		}
	}
}

func TestCloseSession(t *testing.T) {
	root := createFixture(t)
	disabled, err := New(Options{
		Backend: filesystem.Local{}, Root: root, SSHHost: "lab",
		Title: "Files", AllowedHost: testHost,
	})
	if err != nil {
		t.Fatal(err)
	}
	disabledListing := request(disabled, http.MethodGet, "/", nil)
	if strings.Contains(disabledListing.Body.String(), "btn-close-server") {
		t.Fatal("non-closeable app rendered the close button")
	}
	disabledClose := httptest.NewRequest(http.MethodPost, "/close", nil)
	disabledClose.Host = testHost
	disabledClose.Header.Set("Origin", "http://"+testHost)
	disabledResponse := httptest.NewRecorder()
	disabled.ServeHTTP(disabledResponse, disabledClose)
	if disabledResponse.Code != http.StatusNotFound {
		t.Fatalf("disabled close status = %d, want %d", disabledResponse.Code, http.StatusNotFound)
	}
	select {
	case <-disabled.CloseRequested():
		t.Fatal("disabled close request ended the app")
	default:
	}

	app, err := New(Options{
		Backend: filesystem.Local{}, Root: root, SSHHost: "lab",
		Title: "Files", AllowedHost: testHost, Closeable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	listing := request(app, http.MethodGet, "/", nil)
	for _, want := range []string{
		`<form action="/close" method="post"`,
		`onsubmit="return window.confirm('Close open-server and all sessions?');"`,
		`name="csrf" value="` + app.csrfToken + `"`,
		`id="btn-close-server"`,
		`>Close open-server</button>`,
	} {
		if !strings.Contains(listing.Body.String(), want) {
			t.Errorf("closeable listing is missing %q", want)
		}
	}

	wrongMethod := request(app, http.MethodGet, "/close", nil)
	if wrongMethod.Code != http.StatusMethodNotAllowed || wrongMethod.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("close GET = %d Allow %q", wrongMethod.Code, wrongMethod.Header().Get("Allow"))
	}
	missingOrigin := request(app, http.MethodPost, "/close", nil)
	if missingOrigin.Code != http.StatusForbidden {
		t.Fatalf("close without Origin = %d", missingOrigin.Code)
	}
	badOrigin := httptest.NewRequest(http.MethodPost, "/close", nil)
	badOrigin.Host = testHost
	badOrigin.Header.Set("Origin", "http://attacker.example")
	badOriginResponse := httptest.NewRecorder()
	app.ServeHTTP(badOriginResponse, badOrigin)
	if badOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin close = %d", badOriginResponse.Code)
	}
	select {
	case <-app.CloseRequested():
		t.Fatal("rejected close request ended the app")
	default:
	}

	closeRequest := httptest.NewRequest(http.MethodPost, "/close", nil)
	closeRequest.Host = testHost
	closeRequest.Header.Set("Origin", "http://"+testHost)
	closeResponse := httptest.NewRecorder()
	app.ServeHTTP(closeResponse, closeRequest)
	if closeResponse.Code != http.StatusOK {
		t.Fatalf("close response = %d %s", closeResponse.Code, closeResponse.Body.String())
	}
	if contentType := closeResponse.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("close Content-Type = %q", contentType)
	}
	for _, want := range []string{"open-server is closing", "Exit reason: user close", "Exit time:", "press Ctrl-C to close it manually", "You can close this tab."} {
		if !strings.Contains(closeResponse.Body.String(), want) {
			t.Errorf("close response is missing %q", want)
		}
	}
	var requestedAt time.Time
	select {
	case requestedAt = <-app.CloseRequested():
	default:
		t.Fatal("valid close request did not end the app")
	}
	if requestedAt.IsZero() || !strings.Contains(closeResponse.Body.String(), requestedAt.Format(time.RFC3339)) {
		t.Fatalf("close response does not contain request time %s", requestedAt.Format(time.RFC3339))
	}

	repeatRequest := httptest.NewRequest(http.MethodPost, "/close", nil)
	repeatRequest.Host = testHost
	repeatRequest.Header.Set("Origin", "http://"+testHost)
	repeatResponse := httptest.NewRecorder()
	app.ServeHTTP(repeatResponse, repeatRequest)
	if repeatResponse.Code != http.StatusOK {
		t.Fatalf("repeated close response = %d", repeatResponse.Code)
	}

	fallbackApp, err := New(Options{
		Backend: filesystem.Local{}, Root: root, SSHHost: "lab",
		Title: "Files", AllowedHost: testHost, Closeable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fallbackForm := url.Values{"csrf": {fallbackApp.csrfToken}}
	fallbackRequest := httptest.NewRequest(http.MethodPost, "/close", strings.NewReader(fallbackForm.Encode()))
	fallbackRequest.Host = testHost
	fallbackRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	fallbackRequest.Header.Set("Origin", "null")
	fallbackResponse := httptest.NewRecorder()
	fallbackApp.ServeHTTP(fallbackResponse, fallbackRequest)
	if fallbackResponse.Code != http.StatusOK {
		t.Fatalf("CSRF-authenticated close with null Origin = %d %s", fallbackResponse.Code, fallbackResponse.Body.String())
	}
	select {
	case <-fallbackApp.CloseRequested():
	default:
		t.Fatal("CSRF-authenticated close did not end the app")
	}
}

func TestOptionalUIActions(t *testing.T) {
	root := createFixture(t)
	for name, contents := range map[string]string{
		".hidden.txt": "hidden", "results.csv": "a,b\n1,2\n", "results.tsv": "a\tb\n1\t2\n",
		"figure.png": "png", "report.pdf": "%PDF-1.4\n%%EOF\n",
	} {
		if err := os.WriteFile(filepath.Join(filepath.FromSlash(root), name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(filepath.FromSlash(root), "folder", "events.out.tfevents.test"), []byte("event data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(filepath.FromSlash(root), "missing.pdf"), filepath.Join(filepath.FromSlash(root), "broken.pdf")); err != nil {
		t.Fatal(err)
	}
	backendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Path)
	})
	launcher := &recordingTensorBoardLauncher{handler: backendHandler}
	app, err := New(Options{
		Backend: filesystem.Local{}, Root: root, SSHHost: "lab", Title: "Files",
		AllowedHost: testHost, TensorBoard: launcher, LaTeX: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	listing := request(app, http.MethodGet, "/", nil)
	body := listing.Body.String()
	for _, want := range []string{"+ New folder", "Show hidden items", "Copy path", ">Actions</th>", "/assets/apps/tensorboard.png", `aria-label="LaTeX tools"`, `id="latex-full-environment"`, `role="switch"`, `>Short</span>`, `>Full env</span>`, `data-snippet-full=`, `data-snippet-short=`, `open-server-latex-snippet-mode`, `title="Launch TensorBoard"`, `title="Copy LaTeX table snippet"`, `title="Copy LaTeX figure snippet"`, `title="Open live PDF preview in a new tab"`, `\csvautotabular`, `width=1.00\textwidth`} {
		if !strings.Contains(body, want) {
			t.Errorf("enhanced listing is missing %q", want)
		}
	}
	if strings.Contains(body, ".hidden.txt") {
		t.Fatal("hidden item is visible by default")
	}
	if got := strings.Count(body, `class="copy-snippet icon-button"`); got != 8 {
		t.Fatalf("copy snippet button count = %d, want 8", got)
	}
	if got := strings.Count(body, `class="open-live icon-button"`); got != 2 {
		t.Fatalf("live button count = %d, want 2", got)
	}
	for _, button := range []string{">Table</button>", ">Figure</button>"} {
		if strings.Contains(body, button) {
			t.Errorf("enhanced listing still contains text button %q", button)
		}
	}
	shown := request(app, http.MethodGet, "/?hidden=1", nil)
	if !strings.Contains(shown.Body.String(), ".hidden.txt") || !strings.Contains(shown.Body.String(), "Hide hidden items") {
		t.Fatalf("show-hidden listing = %s", shown.Body.String())
	}

	mkdirRequest := httptest.NewRequest(http.MethodPost, "/mkdir?path="+url.QueryEscape(root), strings.NewReader("name=new-folder"))
	mkdirRequest.Host = testHost
	mkdirRequest.Header.Set("Origin", "http://"+testHost)
	mkdirRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mkdirResponse := httptest.NewRecorder()
	app.ServeHTTP(mkdirResponse, mkdirRequest)
	if mkdirResponse.Code != http.StatusCreated {
		t.Fatalf("mkdir response = %d %s", mkdirResponse.Code, mkdirResponse.Body.String())
	}
	if info, err := os.Stat(filepath.Join(filepath.FromSlash(root), "new-folder")); err != nil || !info.IsDir() {
		t.Fatalf("new folder = %#v, %v", info, err)
	}

	tensorRequest := httptest.NewRequest(http.MethodPost, "/tensorboard?path="+url.QueryEscape(filepath.ToSlash(filepath.Join(filepath.FromSlash(root), "folder"))), nil)
	tensorRequest.Host = testHost
	tensorRequest.Header.Set("Origin", "http://"+testHost)
	tensorResponse := httptest.NewRecorder()
	app.ServeHTTP(tensorResponse, tensorRequest)
	if tensorResponse.Code != http.StatusSeeOther || !strings.HasPrefix(tensorResponse.Header().Get("Location"), "/tensorboard/") || !strings.HasSuffix(tensorResponse.Header().Get("Location"), "/#scalars") {
		t.Fatalf("TensorBoard start = %d Location %q body %s", tensorResponse.Code, tensorResponse.Header().Get("Location"), tensorResponse.Body.String())
	}
	proxied := request(app, http.MethodGet, tensorResponse.Header().Get("Location"), nil)
	if proxied.Code != http.StatusOK || proxied.Body.String() != tensorResponse.Header().Get("Location") {
		t.Fatalf("TensorBoard proxy = %d %q", proxied.Code, proxied.Body.String())
	}
	if len(launcher.starts) != 1 || launcher.starts[0].directory != filepath.ToSlash(filepath.Join(filepath.FromSlash(root), "folder")) {
		t.Fatalf("TensorBoard launches = %#v", launcher.starts)
	}

	live := request(app, http.MethodGet, "/live?path="+url.QueryEscape(filepath.ToSlash(filepath.Join(filepath.FromSlash(root), "report.pdf"))), nil)
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), `data-initial-ready="true"`) {
		t.Fatalf("live PDF response = %d %s", live.Code, live.Body.String())
	}
	if liveBody := live.Body.String(); !strings.Contains(liveBody, `id="pageNumber"`) || !strings.Contains(liveBody, `id="viewer" class="pdfViewer"`) || !strings.Contains(liveBody, `src="/live/assets/live-v2.mjs"`) || strings.Contains(liveBody, "<iframe") {
		t.Fatalf("live PDF does not use the controlled PDF.js viewer: %s", liveBody)
	}
	preview := request(app, http.MethodGet, "/preview?path="+url.QueryEscape(filepath.ToSlash(filepath.Join(filepath.FromSlash(root), "report.pdf"))), nil)
	if preview.Code != http.StatusOK || preview.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(preview.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("PDF preview framing policy = %d X-Frame-Options %q CSP %q", preview.Code, preview.Header().Get("X-Frame-Options"), preview.Header().Get("Content-Security-Policy"))
	}
	textPreview := request(app, http.MethodGet, "/preview?path="+url.QueryEscape(filepath.ToSlash(filepath.Join(filepath.FromSlash(root), "alpha.txt"))), nil)
	if textPreview.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(textPreview.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("non-PDF preview framing policy = X-Frame-Options %q CSP %q", textPreview.Header().Get("X-Frame-Options"), textPreview.Header().Get("Content-Security-Policy"))
	}
	status := request(app, http.MethodGet, "/live/status?path="+url.QueryEscape(filepath.ToSlash(filepath.Join(filepath.FromSlash(root), "report.pdf"))), nil)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"version"`) || !strings.Contains(status.Body.String(), `"ready":true`) {
		t.Fatalf("live status = %d %s", status.Code, status.Body.String())
	}
	incompleteName := filepath.Join(filepath.FromSlash(root), "compiling.pdf")
	if err := os.WriteFile(incompleteName, []byte("%PDF-1.4\n1 0 obj\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	incompleteURL := "/live?path=" + url.QueryEscape(filepath.ToSlash(incompleteName))
	incompleteLive := request(app, http.MethodGet, incompleteURL, nil)
	if incompleteLive.Code != http.StatusOK || !strings.Contains(incompleteLive.Body.String(), "waiting for PDF compilation") || !strings.Contains(incompleteLive.Body.String(), `data-initial-ready="false"`) {
		t.Fatalf("incomplete live PDF response = %d %s", incompleteLive.Code, incompleteLive.Body.String())
	}
	incompleteStatus := request(app, http.MethodGet, "/live/status?path="+url.QueryEscape(filepath.ToSlash(incompleteName)), nil)
	if incompleteStatus.Code != http.StatusOK || !strings.Contains(incompleteStatus.Body.String(), `"ready":false`) || !strings.Contains(incompleteStatus.Body.String(), `"version":""`) {
		t.Fatalf("incomplete PDF status = %d %s", incompleteStatus.Code, incompleteStatus.Body.String())
	}
	if err := os.WriteFile(incompleteName, []byte("%PDF-1.4\n1 0 obj\nendobj\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	completeStatus := request(app, http.MethodGet, "/live/status?path="+url.QueryEscape(filepath.ToSlash(incompleteName)), nil)
	if completeStatus.Code != http.StatusOK || !strings.Contains(completeStatus.Body.String(), `"ready":true`) {
		t.Fatalf("completed PDF status = %d %s", completeStatus.Code, completeStatus.Body.String())
	}
	nonPDFLive := request(app, http.MethodGet, "/live?path="+url.QueryEscape(filepath.ToSlash(filepath.Join(filepath.FromSlash(root), "figure.png"))), nil)
	if nonPDFLive.Code != http.StatusBadRequest {
		t.Fatalf("non-PDF live status = %d, want %d", nonPDFLive.Code, http.StatusBadRequest)
	}

	plain, err := New(Options{Backend: filesystem.Local{}, Root: root, SSHHost: "lab", Title: "Files", AllowedHost: testHost})
	if err != nil {
		t.Fatal(err)
	}
	plainBody := request(plain, http.MethodGet, "/", nil).Body.String()
	if strings.Contains(plainBody, "LaTeX tools") || strings.Contains(plainBody, `id="latex-full-environment"`) || strings.Contains(plainBody, "Open live PDF preview") || strings.Contains(plainBody, "TensorBoard") {
		t.Fatal("optional actions are visible without their flags")
	}
	if strings.Contains(plainBody, "token-protected plain HTTP") {
		t.Fatal("plain-HTTP warning is visible in default loopback mode")
	}
	plainLive := request(plain, http.MethodGet, "/live?path="+url.QueryEscape(filepath.ToSlash(filepath.Join(filepath.FromSlash(root), "report.pdf"))), nil)
	if plainLive.Code != http.StatusNotFound {
		t.Fatalf("plain live status = %d, want %d", plainLive.Code, http.StatusNotFound)
	}
	plainPreview := request(plain, http.MethodGet, "/preview?path="+url.QueryEscape(filepath.ToSlash(filepath.Join(filepath.FromSlash(root), "report.pdf"))), nil)
	if plainPreview.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(plainPreview.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("plain preview framing policy = X-Frame-Options %q CSP %q", plainPreview.Header().Get("X-Frame-Options"), plainPreview.Header().Get("Content-Security-Policy"))
	}

	latexOnly, err := New(Options{Backend: filesystem.Local{}, Root: root, SSHHost: "lab", Title: "Files", AllowedHost: testHost, LaTeX: true})
	if err != nil {
		t.Fatal(err)
	}
	latexOnlyBody := request(latexOnly, http.MethodGet, "/", nil).Body.String()
	if !strings.Contains(latexOnlyBody, `colspan="1">Actions</th>`) || !strings.Contains(latexOnlyBody, `colspan="3" aria-label="LaTeX tools"`) {
		t.Fatal("LaTeX-only mode does not use fixed Download, Preview, Table, and Figure columns")
	}
}

func TestTensorBoardLaunchRequiresEventFilesAndAllowsFormToken(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "logs")
	empty := filepath.Join(root, "empty")
	for _, directory := range []string{logs, empty} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(logs, "events.out.tfevents.test-host.1"), []byte("events"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := &recordingTensorBoardLauncher{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	app, err := New(Options{
		Backend: filesystem.Local{}, Root: filepath.ToSlash(root), SSHHost: "lab",
		Title: "Files", AllowedHost: testHost, TensorBoard: launcher,
	})
	if err != nil {
		t.Fatal(err)
	}

	listing := request(app, http.MethodGet, "/", nil)
	if got := strings.Count(listing.Body.String(), `aria-label="Launch TensorBoard"`); got != 1 {
		t.Fatalf("TensorBoard icon count = %d, want 1; body = %s", got, listing.Body.String())
	}
	if strings.Contains(listing.Body.String(), ">Launch</button>") || strings.Contains(listing.Body.String(), ">TensorBoard</button>") {
		t.Fatal("legacy visible launcher button label is still present")
	}

	emptyRequest := httptest.NewRequest(http.MethodPost, "/tensorboard?path="+url.QueryEscape(filepath.ToSlash(empty)), nil)
	emptyRequest.Host = testHost
	emptyRequest.Header.Set("Origin", "http://"+testHost)
	emptyResponse := httptest.NewRecorder()
	app.ServeHTTP(emptyResponse, emptyRequest)
	if emptyResponse.Code != http.StatusBadRequest || !strings.Contains(emptyResponse.Body.String(), "event files were not found") {
		t.Fatalf("non-event directory launch = %d %q", emptyResponse.Code, emptyResponse.Body.String())
	}

	wrongToken := httptest.NewRequest(http.MethodPost, "/tensorboard?path="+url.QueryEscape(filepath.ToSlash(logs)), strings.NewReader("csrf=wrong"))
	wrongToken.Host = testHost
	wrongToken.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wrongTokenResponse := httptest.NewRecorder()
	app.ServeHTTP(wrongTokenResponse, wrongToken)
	if wrongTokenResponse.Code != http.StatusForbidden {
		t.Fatalf("wrong form token status = %d", wrongTokenResponse.Code)
	}

	form := url.Values{"csrf": {app.csrfToken}}
	launchRequest := httptest.NewRequest(http.MethodPost, "/tensorboard?path="+url.QueryEscape(filepath.ToSlash(logs)), strings.NewReader(form.Encode()))
	launchRequest.Host = testHost
	launchRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	launchResponse := httptest.NewRecorder()
	app.ServeHTTP(launchResponse, launchRequest)
	if launchResponse.Code != http.StatusSeeOther || !strings.HasPrefix(launchResponse.Header().Get("Location"), "/tensorboard/") || !strings.HasSuffix(launchResponse.Header().Get("Location"), "/#scalars") {
		t.Fatalf("token-authenticated launch = %d Location %q body %q", launchResponse.Code, launchResponse.Header().Get("Location"), launchResponse.Body.String())
	}
	if len(launcher.starts) != 1 || launcher.starts[0].directory != filepath.ToSlash(logs) {
		t.Fatalf("TensorBoard launches = %#v", launcher.starts)
	}
	repeatRequest := httptest.NewRequest(http.MethodPost, "/tensorboard?path="+url.QueryEscape(filepath.ToSlash(logs)), nil)
	repeatRequest.Host = testHost
	repeatRequest.Header.Set("Origin", "http://"+testHost)
	repeatResponse := httptest.NewRecorder()
	app.ServeHTTP(repeatResponse, repeatRequest)
	if repeatResponse.Code != http.StatusSeeOther || repeatResponse.Header().Get("Location") != launchResponse.Header().Get("Location") {
		t.Fatalf("repeated launch = %d Location %q, want original Location %q", repeatResponse.Code, repeatResponse.Header().Get("Location"), launchResponse.Header().Get("Location"))
	}
	if len(launcher.starts) != 1 {
		t.Fatalf("repeated Launch started %d TensorBoard processes, want 1", len(launcher.starts))
	}
}

func TestConcurrentTensorBoardLaunchesReuseOneProcess(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "logs")
	if err := os.Mkdir(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "events.out.tfevents.concurrent"), []byte("events"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := &blockingTensorBoardLauncher{
		started: make(chan struct{}),
		release: make(chan struct{}),
		handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	}
	app, err := New(Options{
		Backend: filesystem.Local{}, Root: filepath.ToSlash(root), SSHHost: "lab",
		Title: "Files", AllowedHost: testHost, TensorBoard: launcher,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := "/tensorboard?path=" + url.QueryEscape(filepath.ToSlash(logs))
	launch := func(result chan<- *httptest.ResponseRecorder) {
		request := httptest.NewRequest(http.MethodPost, target, nil)
		request.Host = testHost
		request.Header.Set("Origin", "http://"+testHost)
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		result <- response
	}
	results := make(chan *httptest.ResponseRecorder, 2)
	go launch(results)
	select {
	case <-launcher.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first TensorBoard launch did not start")
	}
	go launch(results)
	time.Sleep(100 * time.Millisecond)
	if got := launcher.starts.Load(); got != 1 {
		t.Fatalf("concurrent requests started %d TensorBoard processes, want 1", got)
	}
	close(launcher.release)
	first, second := <-results, <-results
	if first.Code != http.StatusSeeOther || second.Code != http.StatusSeeOther || first.Header().Get("Location") != second.Header().Get("Location") {
		t.Fatalf("concurrent launch responses = (%d, %q) and (%d, %q)", first.Code, first.Header().Get("Location"), second.Code, second.Header().Get("Location"))
	}
	if got := launcher.starts.Load(); got != 1 {
		t.Fatalf("concurrent Launch started %d TensorBoard processes, want 1", got)
	}
}

func TestTensorBoardProxyRewritesOriginForBackend(t *testing.T) {
	target, err := url.Parse("http://127.0.0.1:54321")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newTensorBoardReverseProxy(target, "secret-token")
	request := httptest.NewRequest(http.MethodPost, "http://"+testHost+"/tensorboard/session/data/plugin", nil)
	request.Host = testHost
	request.Header.Set("Origin", "http://"+testHost)
	proxy.Director(request)
	if request.URL.Scheme != "http" || request.URL.Host != target.Host || request.Host != target.Host {
		t.Fatalf("proxied destination = URL %s Host %q", request.URL.String(), request.Host)
	}
	if got, want := request.Header.Get("Origin"), "http://"+target.Host; got != want {
		t.Fatalf("proxied Origin = %q, want %q", got, want)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer secret-token" {
		t.Fatalf("proxied Authorization = %q", got)
	}
}

func TestLaTeXSnippets(t *testing.T) {
	figureFull := "\\begin{figure}[htbp]\n" +
		"  \\centering\n" +
		"  \\includegraphics[width=1.00\\textwidth]{\\detokenize{/paper/Figure One.PNG}}\n" +
		"  % \\caption{}\n" +
		"  % \\label{fig:figure-one}\n" +
		"\\end{figure}"
	figureShort := "\\includegraphics[width=1.00\\textwidth]{\\detokenize{/paper/Figure One.PNG}}"
	tableFull := "\\begin{table}[htbp]\n" +
		"  \\centering\n" +
		"  \\csvautotabular[separator=tab]{\\detokenize{/paper/Data Set.TSV}}\n" +
		"  % \\caption{}\n" +
		"  % \\label{tab:data-set}\n" +
		"\\end{table}"
	tableShort := "\\csvautotabular[separator=tab]{\\detokenize{/paper/Data Set.TSV}}"

	gotFigure, gotTable := makeLaTeXSnippets("/paper/Figure One.PNG")
	if gotFigure.Full != figureFull || gotFigure.Short != figureShort || gotTable.Full != "" || gotTable.Short != "" {
		t.Fatalf("PNG snippets = figure %q, table %q", gotFigure, gotTable)
	}
	gotFigure, gotTable = makeLaTeXSnippets("/paper/Data Set.TSV")
	if gotFigure.Full != "" || gotFigure.Short != "" || gotTable.Full != tableFull || gotTable.Short != tableShort {
		t.Fatalf("TSV snippets = figure %q, table %q", gotFigure, gotTable)
	}
	for _, name := range []string{"plot.jpg", "plot.jpeg", "plot.pdf"} {
		gotFigure, gotTable = makeLaTeXSnippets(name)
		if gotFigure.Full == "" || gotFigure.Short == "" || gotTable.Full != "" || gotTable.Short != "" {
			t.Errorf("%s snippets = figure %q, table %q", name, gotFigure, gotTable)
		}
	}
	gotFigure, gotTable = makeLaTeXSnippets("data.csv")
	if gotFigure.Full != "" || gotFigure.Short != "" || !strings.Contains(gotTable.Full, `\csvautotabular{\detokenize{data.csv}}`) || gotTable.Short != `\csvautotabular{\detokenize{data.csv}}` || strings.Contains(gotTable.Full, "separator=tab") {
		t.Fatalf("CSV snippets = figure %q, table %q", gotFigure, gotTable)
	}
	for _, name := range []string{"plot.svg", "plot.eps", "plot.gif", "notes.txt", "folder"} {
		gotFigure, gotTable = makeLaTeXSnippets(name)
		if gotFigure.Full != "" || gotFigure.Short != "" || gotTable.Full != "" || gotTable.Short != "" {
			t.Errorf("unsupported %s snippets = figure %q, table %q", name, gotFigure, gotTable)
		}
	}
}

func TestLivePDFVersionChangesWithFile(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "paper.pdf")
	if err := os.WriteFile(name, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	firstVersion := fileVersion(first)
	if err := os.WriteFile(name, []byte("second-version"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if secondVersion := fileVersion(second); secondVersion == firstVersion {
		t.Fatalf("file version did not change: %q", secondVersion)
	}
}

func TestLivePDFCompletionAgainstLocalAndSFTP(t *testing.T) {
	for _, fixture := range fixtures(t) {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			defer fixture.close()
			name := filepath.Join(filepath.FromSlash(fixture.root), "paper.pdf")
			if err := os.WriteFile(name, []byte("%PDF-1.7\n1 0 obj\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			app, err := New(Options{Backend: fixture.backend, Root: fixture.root, SSHHost: "lab", Title: "Files", AllowedHost: testHost, LaTeX: true})
			if err != nil {
				t.Fatal(err)
			}
			statusURL := "/live/status?path=" + url.QueryEscape(filepath.ToSlash(name))
			incomplete := request(app, http.MethodGet, statusURL, nil)
			if incomplete.Code != http.StatusOK || !strings.Contains(incomplete.Body.String(), `"ready":false`) {
				t.Fatalf("incomplete PDF status = %d %s", incomplete.Code, incomplete.Body.String())
			}
			if err := os.WriteFile(name, []byte("%PDF-1.7\n1 0 obj\nendobj\n%%EOF\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			complete := request(app, http.MethodGet, statusURL, nil)
			if complete.Code != http.StatusOK || !strings.Contains(complete.Body.String(), `"ready":true`) {
				t.Fatalf("complete PDF status = %d %s", complete.Code, complete.Body.String())
			}
		})
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
	if listing.Code != http.StatusOK || !strings.Contains(listing.Body.String(), `data-path="`+filepath.ToSlash(logicalRoot)+`"`) || !strings.Contains(listing.Body.String(), "note.txt") {
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

func TestPreviewContentTypePolicy(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	for _, test := range []struct {
		name        string
		fileName    string
		sample      []byte
		contentType string
		inline      bool
	}{
		{name: "extensionless text", fileName: "README", sample: []byte("plain text\n"), contentType: "text/plain; charset=utf-8", inline: true},
		{name: "HTML disguised as PNG", fileName: "payload.png", sample: []byte("<script>alert(1)</script>"), contentType: "text/plain; charset=utf-8", inline: true},
		{name: "PNG disguised as HTML remains source", fileName: "payload.HTML", sample: png, contentType: "text/plain; charset=utf-8", inline: true},
		{name: "PostScript source", fileName: "figure.eps", sample: []byte("%!PS-Adobe-3.0"), contentType: "text/plain; charset=utf-8", inline: true},
		{name: "signature detected PNG", fileName: "image.unknown", sample: png, contentType: "image/png", inline: true},
		{name: "signature detected PDF", fileName: "paper.unknown", sample: []byte("%PDF-1.7\n"), contentType: "application/pdf", inline: true},
		{name: "AVIF extension fallback", fileName: "image.avif", sample: []byte("\x00\x01\x02\x03"), contentType: "image/avif", inline: true},
		{name: "archive fails closed", fileName: "archive.zip", sample: []byte("PK\x03\x04payload"), contentType: "application/octet-stream", inline: false},
		{name: "WASM fails closed", fileName: "module.wasm", sample: []byte("\x00asm\x01\x00\x00\x00"), contentType: "application/octet-stream", inline: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			contentType, inline := previewContentType(test.fileName, test.sample)
			if contentType != test.contentType || inline != test.inline {
				t.Fatalf("previewContentType(%q) = %q, %v; want %q, %v", test.fileName, contentType, inline, test.contentType, test.inline)
			}
		})
	}
}

func TestPreviewRejectsNonRegularFilesAndReadFailures(t *testing.T) {
	readFailure := errors.New("read failed")
	seekFailure := errors.New("seek failed")
	for _, test := range []struct {
		name       string
		info       fs.FileInfo
		open       func() (filesystem.ReadSeekCloser, error)
		wantStatus int
		wantOpen   bool
	}{
		{
			name:       "named pipe",
			info:       fixedFileInfo{name: "pipe", mode: fs.ModeNamedPipe},
			open:       func() (filesystem.ReadSeekCloser, error) { return nil, errors.New("must not open") },
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "read error",
			info: fixedFileInfo{name: "file", mode: 0o600, size: 1},
			open: func() (filesystem.ReadSeekCloser, error) {
				return &stubReadSeekCloser{read: func([]byte) (int, error) { return 0, readFailure }}, nil
			},
			wantStatus: http.StatusBadGateway,
			wantOpen:   true,
		},
		{
			name: "seek error",
			info: fixedFileInfo{name: "file", mode: 0o600, size: 1},
			open: func() (filesystem.ReadSeekCloser, error) {
				reader := bytes.NewReader([]byte("x"))
				return &stubReadSeekCloser{
					read: reader.Read,
					seek: func(int64, int) (int64, error) { return 0, seekFailure },
				}, nil
			},
			wantStatus: http.StatusBadGateway,
			wantOpen:   true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &previewTestBackend{info: test.info, open: test.open}
			app, err := New(Options{Backend: backend, Root: "/root", SSHHost: "test", AllowedHost: testHost})
			if err != nil {
				t.Fatal(err)
			}
			response := request(app, http.MethodGet, "/preview?path=/root/file", nil)
			if response.Code != test.wantStatus || backend.opened.Load() != test.wantOpen {
				t.Fatalf("response = %d, opened = %v; want %d, %v", response.Code, backend.opened.Load(), test.wantStatus, test.wantOpen)
			}
			if !strings.Contains(response.Header().Get("Content-Security-Policy"), "sandbox") {
				t.Fatalf("preview error is missing sandbox CSP: %#v", response.Header())
			}
		})
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

type tensorBoardStart struct {
	directory string
	prefix    string
}

type recordingTensorBoardLauncher struct {
	handler http.Handler
	starts  []tensorBoardStart
}

type blockingTensorBoardLauncher struct {
	starts      atomic.Int32
	startedOnce sync.Once
	started     chan struct{}
	release     chan struct{}
	handler     http.Handler
}

func (l *blockingTensorBoardLauncher) Start(_, _ string) (*tensorboard.Instance, error) {
	l.starts.Add(1)
	l.startedOnce.Do(func() { close(l.started) })
	<-l.release
	return &tensorboard.Instance{Handler: l.handler}, nil
}

func (l *recordingTensorBoardLauncher) Start(directory, prefix string) (*tensorboard.Instance, error) {
	l.starts = append(l.starts, tensorBoardStart{directory: directory, prefix: prefix})
	return &tensorboard.Instance{Handler: l.handler}, nil
}

type statErrorBackend struct {
	filesystem.Backend
	err error
}

func (b statErrorBackend) Stat(context.Context, string) (fs.FileInfo, error) {
	return nil, b.err
}

type fixedFileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (i fixedFileInfo) Name() string       { return i.name }
func (i fixedFileInfo) Size() int64        { return i.size }
func (i fixedFileInfo) Mode() fs.FileMode  { return i.mode }
func (i fixedFileInfo) ModTime() time.Time { return time.Time{} }
func (i fixedFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fixedFileInfo) Sys() any           { return nil }

type previewTestBackend struct {
	filesystem.Backend
	info   fs.FileInfo
	open   func() (filesystem.ReadSeekCloser, error)
	opened atomic.Bool
}

func (b *previewTestBackend) Stat(context.Context, string) (fs.FileInfo, error) {
	return b.info, nil
}

func (b *previewTestBackend) Open(context.Context, string) (filesystem.ReadSeekCloser, error) {
	b.opened.Store(true)
	return b.open()
}

type stubReadSeekCloser struct {
	read func([]byte) (int, error)
	seek func(int64, int) (int64, error)
}

func (f *stubReadSeekCloser) Read(buffer []byte) (int, error) {
	return f.read(buffer)
}

func (f *stubReadSeekCloser) Seek(offset int64, whence int) (int64, error) {
	if f.seek == nil {
		return offset, nil
	}
	return f.seek(offset, whence)
}

func (f *stubReadSeekCloser) Close() error { return nil }

func TestUploadsFinishWithPrivatePermissions(t *testing.T) {
	for _, fixture := range fixtures(t) {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			defer fixture.close()
			ctx := context.Background()
			name := filepath.ToSlash(filepath.Join(fixture.root, "uploaded.txt"))
			if _, err := fixture.backend.Upload(ctx, name, strings.NewReader("secret"), false); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("new upload mode = %#o, want 0600", info.Mode().Perm())
			}
			if _, err := fixture.backend.Upload(ctx, name, strings.NewReader("replacement"), true); err != nil {
				t.Fatal(err)
			}
			info, err = os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("replaced upload mode = %#o, want 0600", info.Mode().Perm())
			}
		})
	}
}
