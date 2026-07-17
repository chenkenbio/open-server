package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const (
	liveAssetRoutePrefix = "/live/assets/"
	appAssetRoutePrefix  = "/assets/apps/"
	// Rename both live-vN assets whenever either file changes; these URLs are
	// cached as immutable.
	liveControllerURL    = liveAssetRoutePrefix + "live-v2.mjs"
	liveStylesURL        = liveAssetRoutePrefix + "live-v2.css"
	pdfJSVersion         = "5.7.284"
	pdfJSRoutePrefix     = liveAssetRoutePrefix + "pdfjs-" + pdfJSVersion + "/"
)

// liveAssets contains the application-owned live viewer and its pinned PDF.js
// runtime. The route is authenticated like every other open-server endpoint.
//
//go:embed assets/live-v2.* assets/pdfjs-5.7.284
var liveAssets embed.FS

// appAssets contains the application launcher artwork.
//
//go:embed assets/apps
var appAssets embed.FS

// serveAppAsset serves one allowlisted launcher icon.
func (a *App) serveAppAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, appAssetRoutePrefix)
	contentType := ""
	switch name {
	case "jupyter.svg":
		contentType = "image/svg+xml"
	case "tensorboard.png":
		contentType = "image/png"
	default:
		http.NotFound(w, r)
		return
	}
	embeddedName := path.Join("assets/apps", name)
	info, err := fs.Stat(appAssets, embeddedName)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFileFS(w, r, appAssets, embeddedName)
}

// serveLiveAsset serves one validated, embedded live-viewer asset.
func (a *App) serveLiveAsset(w http.ResponseWriter, r *http.Request) {
	if !a.latex {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, liveAssetRoutePrefix)
	if !fs.ValidPath(name) || name == "." {
		http.NotFound(w, r)
		return
	}
	if name != "live-v2.mjs" && name != "live-v2.css" &&
		!strings.HasPrefix(name, "pdfjs-"+pdfJSVersion+"/") {
		http.NotFound(w, r)
		return
	}

	embeddedName := path.Join("assets", name)
	info, err := fs.Stat(liveAssets, embeddedName)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; connect-src 'self'")
	w.Header().Set("Content-Type", liveAssetContentType(name))
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFileFS(w, r, liveAssets, embeddedName)
}

// liveAssetContentType returns deterministic MIME types for embedded assets.
func liveAssetContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".mjs", ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".wasm":
		return "application/wasm"
	case ".svg":
		return "image/svg+xml"
	case ".gif":
		return "image/gif"
	case ".ttf":
		return "font/ttf"
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".bcmap", ".icc", ".pfb":
		return "application/octet-stream"
	default:
		return "text/plain; charset=utf-8"
	}
}
