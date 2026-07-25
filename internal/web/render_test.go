package web

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"open-server/internal/filesystem"
)

type countingOpenBackend struct {
	filesystem.Backend
	opened atomic.Int32
}

// Open records file opens while delegating to the selected fixture backend.
func (b *countingOpenBackend) Open(
	ctx context.Context,
	name string,
) (filesystem.ReadSeekCloser, error) {
	b.opened.Add(1)
	return b.Backend.Open(ctx, name)
}

// renderedFixtureFiles creates the documents and passive assets used by the
// rendered-preview integration tests.
func renderedFixtureFiles(t *testing.T, root string) (string, map[string]string) {
	t.Helper()
	directory := filepath.Join(filepath.FromSlash(root), "rendered site")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"index.html": `<!doctype html>
<html><head><link rel="stylesheet" href="style.css"></head>
<body onload="parent.pwned = true"><h1>Rendered HTML</h1><img src="diagram.svg" onerror="parent.pwned = true">
<script>parent.pwned = true</script><form action="/upload"></form>
<iframe src="https://external.invalid/frame"></iframe>
<object data="https://external.invalid/object"></object></body></html>`,
		"style.css": `body { color: #246; background-image: url("pixel.png"); }`,
		"diagram.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20">
<script>fetch("https://external.invalid/svg-probe")</script>
<image href="https://external.invalid/pixel.png" onload="parent.pwned = true"/>
<use href="https://external.invalid/shapes.svg#shape"/>
<foreignObject><div xmlns="http://www.w3.org/1999/xhtml">active HTML</div></foreignObject>
<circle cx="10" cy="10" r="8"/></svg>`,
		"notes.markdown": `# Preview heading

[Jump to heading](#preview-heading)

![Diagram](diagram.svg)

[Other Markdown](other.md)

[HTML page](index.html)

| Name | Value |
| --- | ---: |
| alpha | 1 |

- [x] rendered task

~~struck text~~

<https://example.com>

<script id="raw-script">parent.pwned = true</script>

[unsafe](javascript:alert(1))

[encoded unsafe](java&#x73;cript:alert(2))
`,
		"other.md":       "# Other document\n",
		"UPPER.HTML":     "<h1>Upper HTML</h1>",
		"UPPER.SVG":      `<svg xmlns="http://www.w3.org/2000/svg"/>`,
		"UPPER.MARKDOWN": "# Upper Markdown\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(directory, "pixel.png"),
		[]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(directory), files
}

// listingFileHref returns the rendered href for one named directory entry.
func listingFileHref(t *testing.T, body, name string) string {
	t.Helper()
	for _, match := range regexp.MustCompile(
		`href="([^"]+)">([^<]*)</a>`,
	).FindAllStringSubmatch(body, -1) {
		if html.UnescapeString(match[2]) == name {
			return html.UnescapeString(match[1])
		}
	}
	t.Fatalf("listing is missing the link for %q", name)
	return ""
}

func TestRenderedPreviewsAgainstLocalAndSFTP(t *testing.T) {
	for _, fixture := range fixtures(t) {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			defer fixture.close()
			directory, files := renderedFixtureFiles(t, fixture.root)
			counted := &countingOpenBackend{Backend: fixture.backend}
			app := newTestApp(t, backendFixture{
				name: fixture.name, backend: counted, root: fixture.root,
			}, nil)

			listing := request(
				app,
				http.MethodGet,
				"/?path="+url.QueryEscape(directory),
				nil,
			)
			if listing.Code != http.StatusOK {
				t.Fatalf("listing = %d %s", listing.Code, listing.Body.String())
			}
			if counted.opened.Load() != 0 {
				t.Fatalf("directory listing opened %d files", counted.opened.Load())
			}

			expectedLinks := map[string]string{
				"index.html":     previewContentRoutePrefix,
				"diagram.svg":    previewRenderRoutePrefix,
				"notes.markdown": previewRenderRoutePrefix,
				"UPPER.HTML":     previewContentRoutePrefix,
				"UPPER.SVG":      previewRenderRoutePrefix,
				"UPPER.MARKDOWN": previewRenderRoutePrefix,
			}
			for name, prefix := range expectedLinks {
				fullName := filepath.ToSlash(filepath.Join(filepath.FromSlash(directory), name))
				if got, want := listingFileHref(t, listing.Body.String(), name),
					previewRouteURL(prefix, fullName); got != want {
					t.Errorf("%s listing URL = %q, want %q", name, got, want)
				}
			}

			htmlName := filepath.ToSlash(
				filepath.Join(filepath.FromSlash(directory), "index.html"),
			)
			htmlURL := previewRouteURL(previewContentRoutePrefix, htmlName)
			htmlResponse := request(app, http.MethodGet, htmlURL, nil)
			if htmlResponse.Code != http.StatusOK ||
				htmlResponse.Body.String() != files["index.html"] ||
				htmlResponse.Header().Get("Content-Type") != "text/html; charset=utf-8" {
				t.Fatalf(
					"HTML preview = %d Content-Type %q body %q",
					htmlResponse.Code,
					htmlResponse.Header().Get("Content-Type"),
					htmlResponse.Body.String(),
				)
			}
			htmlCSP := htmlResponse.Header().Get("Content-Security-Policy")
			for _, want := range []string{
				"sandbox allow-same-origin",
				"script-src 'none'",
				"style-src 'self' 'unsafe-inline'",
				"img-src 'self' data:",
				"form-action 'none'",
				"frame-ancestors 'none'",
			} {
				if !strings.Contains(htmlCSP, want) {
					t.Errorf("HTML CSP %q is missing %q", htmlCSP, want)
				}
			}
			for key, want := range map[string]string{
				"Cache-Control":                "private, no-store",
				"Cross-Origin-Opener-Policy":   "same-origin",
				"Cross-Origin-Resource-Policy": "same-origin",
				"Referrer-Policy":              "no-referrer",
				"X-Content-Type-Options":       "nosniff",
				"X-Frame-Options":              "DENY",
			} {
				if got := htmlResponse.Header().Get(key); got != want {
					t.Errorf("HTML %s = %q, want %q", key, got, want)
				}
			}

			legacyHTML := request(
				app,
				http.MethodGet,
				"/preview?path="+url.QueryEscape(htmlName),
				nil,
			)
			if legacyHTML.Code != http.StatusTemporaryRedirect ||
				legacyHTML.Header().Get("Location") != htmlURL {
				t.Fatalf(
					"legacy HTML preview = %d Location %q",
					legacyHTML.Code,
					legacyHTML.Header().Get("Location"),
				)
			}
			directHTML := request(
				app,
				http.MethodGet,
				"/?path="+url.QueryEscape(htmlName),
				nil,
			)
			if directHTML.Code != http.StatusTemporaryRedirect ||
				directHTML.Header().Get("Location") != htmlURL {
				t.Fatalf(
					"direct HTML preview = %d Location %q",
					directHTML.Code,
					directHTML.Header().Get("Location"),
				)
			}

			rangeRequest := httptest.NewRequest(http.MethodGet, htmlURL, nil)
			rangeRequest.Host = testHost
			rangeRequest.Header.Set("Range", "bytes=0-14")
			rangeResponse := httptest.NewRecorder()
			app.ServeHTTP(rangeResponse, rangeRequest)
			if rangeResponse.Code != http.StatusPartialContent ||
				rangeResponse.Body.String() != files["index.html"][:15] {
				t.Fatalf(
					"HTML range = %d %q",
					rangeResponse.Code,
					rangeResponse.Body.String(),
				)
			}
			htmlHead := request(app, http.MethodHead, htmlURL, nil)
			if htmlHead.Code != http.StatusOK || htmlHead.Body.Len() != 0 ||
				htmlHead.Header().Get("Content-Length") != strconv.Itoa(len(files["index.html"])) {
				t.Fatalf("HTML HEAD = %d headers %#v", htmlHead.Code, htmlHead.Header())
			}

			styleName := filepath.ToSlash(
				filepath.Join(filepath.FromSlash(directory), "style.css"),
			)
			styleResponse := request(
				app,
				http.MethodGet,
				previewRouteURL(previewContentRoutePrefix, styleName),
				nil,
			)
			if styleResponse.Code != http.StatusOK ||
				styleResponse.Header().Get("Content-Type") != "text/css; charset=utf-8" ||
				styleResponse.Body.String() != files["style.css"] {
				t.Fatalf(
					"CSS asset = %d Content-Type %q body %q",
					styleResponse.Code,
					styleResponse.Header().Get("Content-Type"),
					styleResponse.Body.String(),
				)
			}

			svgName := filepath.ToSlash(
				filepath.Join(filepath.FromSlash(directory), "diagram.svg"),
			)
			svgURL := previewRouteURL(previewRenderRoutePrefix, svgName)
			opensBeforeSVG := counted.opened.Load()
			svgPage := request(app, http.MethodGet, svgURL, nil)
			if svgPage.Code != http.StatusOK ||
				svgPage.Header().Get("Content-Type") != "text/html; charset=utf-8" {
				t.Fatalf("SVG page = %d headers %#v", svgPage.Code, svgPage.Header())
			}
			if counted.opened.Load() != opensBeforeSVG {
				t.Fatal("SVG wrapper opened the source before the browser requested the image")
			}
			svgContentURL := previewRouteURL(previewContentRoutePrefix, svgName)
			if !strings.Contains(svgPage.Body.String(), `src="`+svgContentURL+`"`) ||
				strings.Contains(svgPage.Body.String(), "<svg") ||
				strings.Contains(svgPage.Body.String(), "external.invalid") {
				t.Fatalf("SVG wrapper contains unsafe or missing content: %s", svgPage.Body.String())
			}
			rawSVG := request(app, http.MethodGet, svgContentURL, nil)
			if rawSVG.Code != http.StatusOK ||
				rawSVG.Header().Get("Content-Type") != "image/svg+xml" ||
				rawSVG.Body.String() != files["diagram.svg"] {
				t.Fatalf(
					"SVG content = %d Content-Type %q body %q",
					rawSVG.Code,
					rawSVG.Header().Get("Content-Type"),
					rawSVG.Body.String(),
				)
			}
			svgCSP := rawSVG.Header().Get("Content-Security-Policy")
			for _, want := range []string{
				"sandbox",
				"default-src 'none'",
				"script-src 'none'",
				"img-src data:",
				"form-action 'none'",
			} {
				if !strings.Contains(svgCSP, want) {
					t.Errorf("SVG CSP %q is missing %q", svgCSP, want)
				}
			}
			if strings.Contains(svgCSP, "allow-same-origin") ||
				strings.Contains(svgCSP, "img-src 'self'") {
				t.Fatalf("raw SVG CSP is too permissive: %q", svgCSP)
			}

			markdownName := filepath.ToSlash(
				filepath.Join(filepath.FromSlash(directory), "notes.markdown"),
			)
			markdownURL := previewRouteURL(previewRenderRoutePrefix, markdownName)
			markdown := request(app, http.MethodGet, markdownURL, nil)
			if markdown.Code != http.StatusOK ||
				markdown.Header().Get("Content-Type") != "text/html; charset=utf-8" {
				t.Fatalf("Markdown preview = %d headers %#v", markdown.Code, markdown.Header())
			}
			markdownBody := markdown.Body.String()
			otherMarkdownName := filepath.ToSlash(
				filepath.Join(filepath.FromSlash(directory), "other.md"),
			)
			for _, want := range []string{
				`id="preview-heading"`,
				`href="#preview-heading"`,
				`src="` + svgContentURL + `"`,
				`href="` + previewRouteURL(previewRenderRoutePrefix, otherMarkdownName) + `"`,
				`href="` + htmlURL + `"`,
				"<table>",
				`type="checkbox"`,
				"<del>struck text</del>",
				`href="https://example.com"`,
			} {
				if !strings.Contains(markdownBody, want) {
					t.Errorf("Markdown output is missing %q: %s", want, markdownBody)
				}
			}
			if strings.Contains(markdownBody, "<base") ||
				strings.Contains(markdownBody, `<script id="raw-script">`) ||
				strings.Contains(strings.ToLower(markdownBody), `href="javascript:`) {
				t.Fatalf("Markdown output retained unsafe or base markup: %s", markdownBody)
			}
			markdownHead := request(app, http.MethodHead, markdownURL, nil)
			if markdownHead.Code != http.StatusOK || markdownHead.Body.Len() != 0 ||
				markdownHead.Header().Get("Content-Length") == "" {
				t.Fatalf(
					"Markdown HEAD = %d headers %#v body length %d",
					markdownHead.Code,
					markdownHead.Header(),
					markdownHead.Body.Len(),
				)
			}

			for name, expectedType := range map[string]string{
				"UPPER.HTML":     "text/html; charset=utf-8",
				"UPPER.SVG":      "text/html; charset=utf-8",
				"UPPER.MARKDOWN": "text/html; charset=utf-8",
			} {
				fullName := filepath.ToSlash(
					filepath.Join(filepath.FromSlash(directory), name),
				)
				response := request(app, http.MethodGet, app.renderedPreviewURL(fullName), nil)
				if response.Code != http.StatusOK ||
					response.Header().Get("Content-Type") != expectedType {
					t.Errorf(
						"%s preview = %d Content-Type %q",
						name,
						response.Code,
						response.Header().Get("Content-Type"),
					)
				}
			}

			for _, name := range []string{"index.html", "diagram.svg", "notes.markdown"} {
				fullName := filepath.ToSlash(
					filepath.Join(filepath.FromSlash(directory), name),
				)
				download := request(
					app,
					http.MethodGet,
					"/download?path="+url.QueryEscape(fullName),
					nil,
				)
				if download.Code != http.StatusOK ||
					download.Body.String() != files[name] ||
					!strings.HasPrefix(
						download.Header().Get("Content-Disposition"),
						"attachment",
					) {
					t.Errorf(
						"%s download = %d headers %#v body %q",
						name,
						download.Code,
						download.Header(),
						download.Body.String(),
					)
				}
			}
		})
	}
}

func TestRenderedPreviewPathEncodingAndConfinement(t *testing.T) {
	root := createFixture(t)
	directory := filepath.Join(filepath.FromSlash(root), "encoded dir")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	fileName := "space 日本語 #?+%.md"
	fullName := filepath.ToSlash(filepath.Join(directory, fileName))
	if err := os.WriteFile(fullName, []byte("# Encoded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := newTestApp(
		t,
		backendFixture{backend: filesystem.Local{}, root: root},
		nil,
	)
	listing := request(
		app,
		http.MethodGet,
		"/?path="+url.QueryEscape(filepath.ToSlash(directory)),
		nil,
	)
	href := listingFileHref(t, listing.Body.String(), fileName)
	parsed, err := url.Parse(href)
	if err != nil {
		t.Fatal(err)
	}
	expectedURL := previewRouteURL(previewRenderRoutePrefix, fullName)
	expectedPath := previewRenderRoutePrefix + strings.TrimPrefix(fullName, "/")
	if href != expectedURL || parsed.Path != expectedPath ||
		parsed.EscapedPath() != expectedURL || parsed.RequestURI() != expectedURL {
		t.Fatalf(
			"encoded href = %q Path %q EscapedPath %q RequestURI %q",
			href,
			parsed.Path,
			parsed.EscapedPath(),
			parsed.RequestURI(),
		)
	}
	rendered := request(app, http.MethodGet, href, nil)
	if rendered.Code != http.StatusOK ||
		!strings.Contains(rendered.Body.String(), `id="encoded"`) {
		t.Fatalf("encoded Markdown preview = %d %s", rendered.Code, rendered.Body.String())
	}

	directoryURL := previewRouteURL(
		previewRenderRoutePrefix,
		filepath.ToSlash(directory),
	)
	for _, target := range []string{
		directoryURL + "/%2e%2e/outside.md",
		directoryURL + "/bad%2Fname.md",
		directoryURL + "//empty.md",
	} {
		response := request(app, http.MethodGet, target, nil)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want %d", target, response.Code, http.StatusBadRequest)
		}
	}

	outsideName := filepath.ToSlash(
		filepath.Join(filepath.Dir(filepath.FromSlash(root)), "outside.md"),
	)
	outside := request(
		app,
		http.MethodGet,
		previewRouteURL(previewRenderRoutePrefix, outsideName),
		nil,
	)
	if outside.Code != http.StatusForbidden {
		t.Fatalf("outside rendered preview = %d %s", outside.Code, outside.Body.String())
	}
	for _, target := range []string{
		"/preview/render",
		"/preview/content",
		"/preview/render-evil/file.md",
		"/preview/content-evil/file.html",
	} {
		if response := request(app, http.MethodGet, target, nil); response.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want %d", target, response.Code, http.StatusNotFound)
		}
	}

	contentURL := previewRouteURL(
		previewContentRoutePrefix,
		filepath.ToSlash(filepath.Join(filepath.FromSlash(root), "active.html")),
	)
	for _, target := range []string{href, contentURL} {
		methodRequest := httptest.NewRequest(http.MethodPut, target, nil)
		methodRequest.Host = testHost
		methodRequest.Header.Set("Origin", "http://"+testHost)
		methodResponse := httptest.NewRecorder()
		app.ServeHTTP(methodResponse, methodRequest)
		if methodResponse.Code != http.StatusMethodNotAllowed ||
			methodResponse.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf(
				"%s method = %d Allow %q",
				target,
				methodResponse.Code,
				methodResponse.Header().Get("Allow"),
			)
		}
	}
	for _, target := range []string{href, contentURL} {
		hostRequest := httptest.NewRequest(http.MethodGet, target, nil)
		hostRequest.Host = "attacker.example"
		hostResponse := httptest.NewRecorder()
		app.ServeHTTP(hostResponse, hostRequest)
		if hostResponse.Code != http.StatusMisdirectedRequest {
			t.Errorf(
				"%s invalid Host status = %d",
				target,
				hostResponse.Code,
			)
		}
	}

	directoryNamedMarkdown := filepath.Join(filepath.FromSlash(root), "directory.md")
	if err := os.Mkdir(directoryNamedMarkdown, 0o700); err != nil {
		t.Fatal(err)
	}
	directoryResponse := request(
		app,
		http.MethodGet,
		previewRouteURL(
			previewRenderRoutePrefix,
			filepath.ToSlash(directoryNamedMarkdown),
		),
		nil,
	)
	if directoryResponse.Code != http.StatusBadRequest {
		t.Fatalf(
			"directory rendered preview = %d %s",
			directoryResponse.Code,
			directoryResponse.Body.String(),
		)
	}
}

func TestRenderedPreviewsRejectRenamedBinaryFiles(t *testing.T) {
	root := createFixture(t)
	binary := []byte("\x00\x01\x02\x03\xff")
	app := newTestApp(
		t,
		backendFixture{backend: filesystem.Local{}, root: root},
		nil,
	)
	for _, test := range []struct {
		name   string
		prefix string
	}{
		{name: "binary.HTML", prefix: previewContentRoutePrefix},
		{name: "binary.SVG", prefix: previewContentRoutePrefix},
		{name: "binary.MD", prefix: previewRenderRoutePrefix},
	} {
		t.Run(test.name, func(t *testing.T) {
			fullName := filepath.ToSlash(
				filepath.Join(filepath.FromSlash(root), test.name),
			)
			if err := os.WriteFile(fullName, binary, 0o600); err != nil {
				t.Fatal(err)
			}
			preview := request(
				app,
				http.MethodGet,
				previewRouteURL(test.prefix, fullName),
				nil,
			)
			if preview.Code != http.StatusUnsupportedMediaType ||
				!strings.Contains(preview.Body.String(), "not safe to render") {
				t.Fatalf("binary preview = %d %q", preview.Code, preview.Body.String())
			}
			download := request(
				app,
				http.MethodGet,
				"/download?path="+url.QueryEscape(fullName),
				nil,
			)
			if download.Code != http.StatusOK ||
				!bytes.Equal(download.Body.Bytes(), binary) ||
				!strings.HasPrefix(
					download.Header().Get("Content-Disposition"),
					"attachment",
				) {
				t.Fatalf(
					"binary download = %d headers %#v body %q",
					download.Code,
					download.Header(),
					download.Body.Bytes(),
				)
			}
		})
	}
}

func TestMarkdownRenderingConcurrent(t *testing.T) {
	app := &App{root: "/preview-root"}
	source := []byte("# Concurrent\n\n![image](image.png)\n\n~~safe~~\n")
	const workers = 16
	var group sync.WaitGroup
	errors := make(chan error, workers)
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			var output bytes.Buffer
			if err := app.renderMarkdownDocument(
				source,
				"/preview-root/document.md",
				&output,
			); err != nil {
				errors <- err
				return
			}
			if !strings.Contains(
				output.String(),
				`src="/preview/content/preview-root/image.png"`,
			) {
				errors <- fmt.Errorf("unexpected Markdown output: %s", output.String())
			}
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestMarkdownDuplicateHeadingIDs(t *testing.T) {
	const headings = 4096
	source := bytes.Repeat([]byte("# Repeated heading\n\n"), headings)
	app := &App{root: "/preview-root"}
	var output bytes.Buffer
	if err := app.renderMarkdownDocument(
		source,
		"/preview-root/document.md",
		&output,
	); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(output.String(), `id="repeated-heading`); got != headings {
		t.Fatalf("rendered %d duplicate heading IDs, want %d", got, headings)
	}
	if !strings.Contains(
		output.String(),
		`id="repeated-heading-4095"`,
	) {
		t.Fatal("last duplicate heading does not have the expected unique suffix")
	}

	output.Reset()
	if err := app.renderMarkdownDocument(
		[]byte("# Same\n\n# Same-1\n\n# Same\n"),
		"/preview-root/collisions.md",
		&output,
	); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"same", "same-1", "same-2"} {
		if got := strings.Count(output.String(), `id="`+id+`"`); got != 1 {
			t.Fatalf("heading ID %q appears %d times in %s", id, got, output.String())
		}
	}
}

func BenchmarkMarkdownRendering1MiB(b *testing.B) {
	prefix := []byte(`# Benchmark document

| Name | Value |
| --- | ---: |
| alpha | 1 |

- [x] complete
- [ ] pending

~~struck~~ [relative](other.md) ![image](figure.svg)

`)
	paragraph := []byte(strings.Repeat(
		"Representative Markdown prose for a large scientific report. ",
		72,
	) + "\n\n")
	source := append(prefix, bytes.Repeat(paragraph, (1<<20)/len(paragraph)+1)...)
	source = source[:1<<20]
	benchmarkMarkdownRendering(b, source)
}

func BenchmarkMarkdownRenderingDuplicateHeadings1MiB(b *testing.B) {
	block := []byte("# Repeated heading\n\n")
	source := bytes.Repeat(block, (1<<20)/len(block)+1)
	source = source[:1<<20]
	benchmarkMarkdownRendering(b, source)
}

// benchmarkMarkdownRendering measures the production Markdown conversion
// without filesystem or HTTP setup.
func benchmarkMarkdownRendering(b *testing.B, source []byte) {
	b.Helper()
	app := &App{root: "/benchmark"}
	var output bytes.Buffer
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		output.Reset()
		if err := app.renderMarkdownDocument(
			source,
			"/benchmark/document.md",
			&output,
		); err != nil {
			b.Fatal(err)
		}
	}
}
