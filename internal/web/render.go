package web

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"open-server/internal/filesystem"
)

const (
	previewRenderRoutePrefix  = "/preview/render/"
	previewContentRoutePrefix = "/preview/content/"
)

var errUnsafePreviewContent = errors.New("preview content is not safe to render")

// markdownPreviewIDs tracks unique heading IDs for one Markdown render.
type markdownPreviewIDs struct {
	used       map[string]struct{}
	nextSuffix map[string]int
}

// newMarkdownPreviewIDs returns request-local heading ID state with
// constant-time progress through duplicate suffixes.
func newMarkdownPreviewIDs() *markdownPreviewIDs {
	return &markdownPreviewIDs{
		used:       make(map[string]struct{}),
		nextSuffix: make(map[string]int),
	}
}

// Generate implements parser.IDs while preserving Goldmark's heading slug
// format and avoiding its quadratic duplicate-ID search.
func (ids *markdownPreviewIDs) Generate(value []byte, kind ast.NodeKind) []byte {
	value = util.TrimRightSpace(util.TrimLeftSpace(value))
	result := make([]byte, 0, len(value))
	for index := 0; index < len(value); {
		character := value[index]
		length := int(util.UTF8Len(character))
		index += length
		if length != 1 {
			continue
		}
		if util.IsAlphaNumeric(character) {
			if 'A' <= character && character <= 'Z' {
				character += 'a' - 'A'
			}
			result = append(result, character)
		} else if util.IsSpace(character) || character == '-' || character == '_' {
			result = append(result, '-')
		}
	}
	if len(result) == 0 {
		result = []byte("id")
		if kind == ast.KindHeading {
			result = []byte("heading")
		}
	}

	base := string(result)
	if _, exists := ids.used[base]; !exists {
		ids.used[base] = struct{}{}
		ids.nextSuffix[base] = 1
		return result
	}
	suffix := ids.nextSuffix[base]
	if suffix < 1 {
		suffix = 1
	}
	for {
		candidate := base + "-" + strconv.Itoa(suffix)
		suffix++
		if _, exists := ids.used[candidate]; exists {
			continue
		}
		ids.used[candidate] = struct{}{}
		ids.nextSuffix[base] = suffix
		return []byte(candidate)
	}
}

// Put implements parser.IDs for explicit IDs.
func (ids *markdownPreviewIDs) Put(value []byte) {
	ids.used[string(value)] = struct{}{}
}

// renderedPreviewKind identifies a format with a rendered preview route.
type renderedPreviewKind uint8

const (
	renderedPreviewNone renderedPreviewKind = iota
	renderedPreviewHTML
	renderedPreviewSVG
	renderedPreviewMarkdown
)

var markdownPreviewRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

var svgPreviewPage = template.Must(template.New("svg-preview").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
html, body { width: 100%; min-height: 100%; margin: 0; }
body { display: grid; place-items: center; background: #fff; }
main { box-sizing: border-box; width: 100%; min-height: 100vh; padding: 1rem; display: grid; place-items: center; }
img { display: block; max-width: 100%; max-height: calc(100vh - 2rem); }
</style>
</head>
<body>
<main><img src="{{.ContentURL}}" alt="{{.Title}}"></main>
</body>
</html>`))

var markdownPreviewPage = template.Must(template.New("markdown-preview").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root { color-scheme: light dark; }
body { box-sizing: border-box; max-width: 64rem; margin: 0 auto; padding: 2rem; font: 16px/1.55 system-ui, sans-serif; overflow-wrap: break-word; }
img, video { max-width: 100%; height: auto; }
pre { overflow-x: auto; padding: 1rem; border-radius: 0.35rem; background: color-mix(in srgb, CanvasText 8%, Canvas); }
code { font-family: ui-monospace, SFMono-Regular, Consolas, monospace; }
:not(pre) > code { padding: 0.1em 0.3em; border-radius: 0.25rem; background: color-mix(in srgb, CanvasText 8%, Canvas); }
table { display: block; max-width: 100%; overflow-x: auto; border-collapse: collapse; }
th, td { padding: 0.4rem 0.65rem; border: 1px solid color-mix(in srgb, CanvasText 25%, Canvas); }
blockquote { margin-left: 0; padding-left: 1rem; border-left: 0.25rem solid color-mix(in srgb, CanvasText 25%, Canvas); }
</style>
</head>
<body>
<main>{{.Body}}</main>
</body>
</html>`))

// renderedPreviewKindForName returns the rendered preview selected by a file's
// extension. File contents are validated when the route is served.
func renderedPreviewKindForName(name string) renderedPreviewKind {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm", ".xhtml":
		return renderedPreviewHTML
	case ".svg":
		return renderedPreviewSVG
	case ".md", ".markdown":
		return renderedPreviewMarkdown
	default:
		return renderedPreviewNone
	}
}

// renderedPreviewURL returns the canonical preview URL for a rendered format.
func (a *App) renderedPreviewURL(name string) string {
	switch renderedPreviewKindForName(name) {
	case renderedPreviewHTML:
		return previewRouteURL(previewContentRoutePrefix, name)
	case renderedPreviewSVG, renderedPreviewMarkdown:
		return previewRouteURL(previewRenderRoutePrefix, name)
	default:
		return ""
	}
}

// previewRouteURL encodes an absolute filesystem path one segment at a time so
// relative browser URLs retain their directory structure.
func previewRouteURL(prefix, name string) string {
	cleaned := path.Clean(name)
	parts := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
	var result strings.Builder
	result.WriteString(prefix)
	for index, part := range parts {
		if part == "" {
			continue
		}
		if index > 0 {
			result.WriteByte('/')
		}
		result.WriteString(url.PathEscape(part))
	}
	return result.String()
}

// previewRoutePath decodes a path-shaped preview URL and applies the same
// configured-root policy used by query-based file operations.
func (a *App) previewRoutePath(r *http.Request, prefix string) (string, error) {
	escapedPath := r.URL.EscapedPath()
	if !strings.HasPrefix(escapedPath, prefix) {
		return "", fs.ErrInvalid
	}
	remainder := strings.TrimPrefix(escapedPath, prefix)
	if remainder == "" {
		return "", fs.ErrInvalid
	}
	encodedParts := strings.Split(remainder, "/")
	parts := make([]string, 0, len(encodedParts))
	for _, encodedPart := range encodedParts {
		part, err := url.PathUnescape(encodedPart)
		if err != nil || part == "" || part == "." || part == ".." ||
			strings.ContainsAny(part, "/\\\x00") {
			return "", fs.ErrInvalid
		}
		parts = append(parts, part)
	}
	name, err := filesystem.CleanRemotePath("/" + strings.Join(parts, "/"))
	if err != nil {
		return "", err
	}
	if !withinRoot(a.root, name) {
		return "", errOutsideRoot
	}
	return name, nil
}

// servePreview keeps the query-based preview route compatible while directing
// renderable documents to their path-shaped canonical URLs.
func (a *App) servePreview(w http.ResponseWriter, r *http.Request) {
	name, err := a.requestPath(r)
	if err != nil {
		a.pathError(w, err)
		return
	}
	if renderedURL := a.renderedPreviewURL(name); renderedURL != "" {
		http.Redirect(w, r, renderedURL, http.StatusTemporaryRedirect)
		return
	}
	a.serveNamedFile(w, r, name, true)
}

// serveRenderedPreview returns an application-owned SVG image page or a
// server-rendered Markdown document.
func (a *App) serveRenderedPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	name, err := a.previewRoutePath(r, previewRenderRoutePrefix)
	if err != nil {
		a.pathError(w, err)
		return
	}
	kind := renderedPreviewKindForName(name)
	if kind == renderedPreviewHTML {
		http.Redirect(
			w,
			r,
			previewRouteURL(previewContentRoutePrefix, name),
			http.StatusTemporaryRedirect,
		)
		return
	}
	if kind != renderedPreviewSVG && kind != renderedPreviewMarkdown {
		http.NotFound(w, r)
		return
	}

	backend, info, err := a.regularPreviewFileInfo(r.Context(), name)
	if err != nil {
		a.pathError(w, err)
		return
	}
	var output bytes.Buffer
	switch kind {
	case renderedPreviewSVG:
		err = svgPreviewPage.Execute(&output, struct {
			Title      string
			ContentURL string
		}{
			Title:      path.Base(name),
			ContentURL: previewRouteURL(previewContentRoutePrefix, name),
		})
	case renderedPreviewMarkdown:
		err = a.renderMarkdownPreview(r.Context(), backend, name, &output)
	}
	if err != nil {
		if errors.Is(err, errUnsafePreviewContent) {
			http.Error(w, errUnsafePreviewContent.Error(), http.StatusUnsupportedMediaType)
		} else {
			a.pathError(w, err)
		}
		return
	}

	a.renderedPreviewSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set(
		"Content-Disposition",
		mime.FormatMediaType("inline", map[string]string{"filename": path.Base(name)}),
	)
	http.ServeContent(w, r, path.Base(name), info.ModTime(), bytes.NewReader(output.Bytes()))
}

// renderMarkdownPreview reads and converts one selected Markdown file.
func (a *App) renderMarkdownPreview(
	ctx context.Context,
	backend filesystem.Backend,
	name string,
	output io.Writer,
) error {
	file, err := backend.Open(ctx, name)
	if err != nil {
		return err
	}
	defer file.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = file.Close() })
	defer stopClose()

	source, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	return a.renderMarkdownDocument(source, name, output)
}

// renderMarkdownDocument converts one Markdown document after mapping relative
// links and images onto the appropriate authenticated preview routes.
func (a *App) renderMarkdownDocument(source []byte, name string, output io.Writer) error {
	if !textPreviewSample(source) {
		return errUnsafePreviewContent
	}
	parseContext := parser.NewContext(parser.WithIDs(newMarkdownPreviewIDs()))
	document := markdownPreviewRenderer.Parser().Parse(
		text.NewReader(source),
		parser.WithContext(parseContext),
	)
	if err := ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch typed := node.(type) {
		case *ast.Link:
			typed.Destination = a.markdownPreviewDestination(name, typed.Destination, false)
		case *ast.Image:
			typed.Destination = a.markdownPreviewDestination(name, typed.Destination, true)
		}
		return ast.WalkContinue, nil
	}); err != nil {
		return err
	}
	var body bytes.Buffer
	if err := markdownPreviewRenderer.Renderer().Render(&body, source, document); err != nil {
		return err
	}

	return markdownPreviewPage.Execute(output, struct {
		Title string
		Body  template.HTML
	}{
		Title: path.Base(name),
		// Goldmark's safe renderer omits raw HTML and dangerous URLs.
		Body: template.HTML(body.String()),
	})
}

// markdownPreviewDestination maps a relative Markdown URL without changing
// fragments, root-relative URLs, or external schemes.
func (a *App) markdownPreviewDestination(
	markdownName string,
	destination []byte,
	image bool,
) []byte {
	parsed, err := url.Parse(string(destination))
	if err != nil || parsed.IsAbs() || parsed.Host != "" ||
		parsed.Path == "" || strings.HasPrefix(parsed.Path, "/") {
		return destination
	}
	target := path.Clean(path.Join(path.Dir(markdownName), parsed.Path))
	if !withinRoot(a.root, target) {
		return destination
	}
	targetURL := ""
	if !image {
		targetURL = a.renderedPreviewURL(target)
	}
	if targetURL == "" {
		targetURL = previewRouteURL(previewContentRoutePrefix, target)
	}
	rewritten, err := url.Parse(targetURL)
	if err != nil {
		return destination
	}
	rewritten.RawQuery = parsed.RawQuery
	rewritten.Fragment = parsed.Fragment
	return []byte(rewritten.String())
}

// servePreviewContent streams static HTML, self-contained SVG, and allowlisted
// companion resources without executing server-side converters.
func (a *App) servePreviewContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	name, err := a.previewRoutePath(r, previewContentRoutePrefix)
	if err != nil {
		a.pathError(w, err)
		return
	}
	backend, info, err := a.regularPreviewFileInfo(r.Context(), name)
	if err != nil {
		a.pathError(w, err)
		return
	}
	file, err := backend.Open(r.Context(), name)
	if err != nil {
		a.pathError(w, err)
		return
	}
	defer file.Close()
	stopClose := context.AfterFunc(r.Context(), func() { _ = file.Close() })
	defer stopClose()

	sample, err := readPreviewSample(file)
	if err != nil {
		a.pathError(w, err)
		return
	}
	contentType, inline := previewResourceContentType(name, sample)
	if !inline {
		http.Error(w, "file contents are not safe to render", http.StatusUnsupportedMediaType)
		return
	}
	if renderedPreviewKindForName(name) == renderedPreviewSVG {
		a.svgPreviewContentSecurityHeaders(w)
	} else {
		a.htmlPreviewContentSecurityHeaders(w)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set(
		"Content-Disposition",
		mime.FormatMediaType("inline", map[string]string{"filename": path.Base(name)}),
	)
	http.ServeContent(w, r, path.Base(name), info.ModTime(), file)
}

// regularPreviewFileInfo validates a regular preview target against one backend
// snapshot so reconnects cannot mix Stat and Open across backends.
func (a *App) regularPreviewFileInfo(
	ctx context.Context,
	name string,
) (filesystem.Backend, fs.FileInfo, error) {
	backend := a.currentBackend()
	info, err := backend.Stat(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fs.ErrInvalid
	}
	return backend, info, nil
}

// readPreviewSample reads and rewinds the MIME-detection prefix of a file.
func readPreviewSample(file filesystem.ReadSeekCloser) ([]byte, error) {
	sample, err := io.ReadAll(io.LimitReader(file, 512))
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return sample, nil
}

// previewResourceContentType selects deterministic document and companion
// resource MIME types, failing closed for renamed binary source files.
func previewResourceContentType(name string, sample []byte) (string, bool) {
	extension := strings.ToLower(path.Ext(name))
	switch extension {
	case ".html", ".htm":
		if textPreviewSample(sample) {
			return "text/html; charset=utf-8", true
		}
		return "application/octet-stream", false
	case ".xhtml":
		if textPreviewSample(sample) {
			return "application/xhtml+xml; charset=utf-8", true
		}
		return "application/octet-stream", false
	case ".svg":
		if textPreviewSample(sample) {
			return "image/svg+xml", true
		}
		return "application/octet-stream", false
	case ".css":
		if textPreviewSample(sample) {
			return "text/css; charset=utf-8", true
		}
		return "application/octet-stream", false
	case ".woff":
		return "font/woff", true
	case ".woff2":
		return "font/woff2", true
	case ".ttf":
		return "font/ttf", true
	case ".otf":
		return "font/otf", true
	default:
		return previewContentType(name, sample)
	}
}

// textPreviewSample reports whether the content sample can be treated as text.
func textPreviewSample(sample []byte) bool {
	if len(sample) > 512 {
		sample = sample[:512]
	}
	return len(sample) == 0 || textLikeContentType(http.DetectContentType(sample))
}

// renderedPreviewSecurityHeaders restrict application-owned SVG and Markdown
// pages to passive same-origin resources.
func (a *App) renderedPreviewSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set(
		"Content-Security-Policy",
		"sandbox allow-same-origin; default-src 'none'; script-src 'none'; "+
			"connect-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; "+
			"style-src 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; "+
			"form-action 'none'; frame-ancestors 'none'",
	)
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

// htmlPreviewContentSecurityHeaders allows only passive resources needed by a
// static HTML document and explicitly keeps all script execution disabled.
func (a *App) htmlPreviewContentSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set(
		"Content-Security-Policy",
		"sandbox allow-same-origin; default-src 'none'; script-src 'none'; "+
			"connect-src 'none'; object-src 'none'; frame-src 'none'; worker-src 'none'; "+
			"style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
			"media-src 'self' data:; font-src 'self' data:; base-uri 'none'; "+
			"form-action 'none'; frame-ancestors 'none'",
	)
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

// svgPreviewContentSecurityHeaders is a second boundary around SVG image mode:
// direct navigation still cannot run scripts or fetch external resources.
func (a *App) svgPreviewContentSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set(
		"Content-Security-Policy",
		"sandbox; default-src 'none'; script-src 'none'; connect-src 'none'; "+
			"object-src 'none'; frame-src 'none'; worker-src 'none'; "+
			"style-src 'unsafe-inline'; img-src data:; font-src data:; "+
			"media-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
	)
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}
