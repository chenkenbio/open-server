package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"remote-browser/internal/filesystem"
)

type Options struct {
	Backend     filesystem.Backend
	Root        string
	SSHHost     string
	Title       string
	AllowedHost string
	HTTPClient  *http.Client
}

var errOutsideRoot = errors.New("path is outside the configured root")

type App struct {
	backend     filesystem.Backend
	root        string
	sshHost     string
	title       string
	allowedHost string
	httpClient  *http.Client
	template    *template.Template
}

func New(options Options) (*App, error) {
	if options.Backend == nil || options.AllowedHost == "" {
		return nil, errors.New("backend and allowed host are required")
	}
	root, err := filesystem.CleanRemotePath(options.Root)
	if err != nil {
		return nil, fmt.Errorf("invalid root: %w", err)
	}
	if !path.IsAbs(root) {
		return nil, errors.New("root must be an absolute remote path")
	}
	if options.Title == "" {
		options.Title = "Remote files — " + options.SSHHost
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{
			Timeout: 30 * time.Minute,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		}
	}
	tmpl, err := template.New("directory").Funcs(template.FuncMap{
		"size": formatSize,
		"time": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	}).Parse(directoryTemplate)
	if err != nil {
		return nil, err
	}
	return &App{
		backend:     options.Backend,
		root:        root,
		sshHost:     options.SSHHost,
		title:       options.Title,
		allowedHost: options.AllowedHost,
		httpClient:  options.HTTPClient,
		template:    tmpl,
	}, nil
}

func (a *App) URL() string {
	return "http://" + a.allowedHost + "/"
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.securityHeaders(w)
	if r.Host != a.allowedHost {
		http.Error(w, "invalid Host header", http.StatusMisdirectedRequest)
		return
	}
	route := r.URL.Path
	if isStateChanging(r.Method) && !a.validOrigin(r) {
		http.Error(w, "invalid Origin header", http.StatusForbidden)
		return
	}

	switch route {
	case "", "/":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		a.browse(w, r)
	case "/download":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		a.serveFile(w, r, false)
	case "/preview":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		a.serveFile(w, r, true)
	case "/upload":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		a.upload(w, r)
	case "/import":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		a.importURL(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

func isStateChanging(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func (a *App) validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme == "http" && parsed.Host == a.allowedHost && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

type directoryView struct {
	Title              string
	SSHHost            string
	RootPath           string
	CurrentPath        string
	UploadURL          string
	ImportURL          string
	NameSortURL        string
	ModifiedSortURL    string
	SizeSortURL        string
	NameSortMarker     string
	ModifiedSortMarker string
	SizeSortMarker     string
	HasParent          bool
	ParentPath         string
	ParentURL          string
	Breadcrumbs        []breadcrumbView
	Entries            []entryView
}

type breadcrumbView struct {
	Name string
	URL  string
}

type entryView struct {
	Name       string
	FullPath   string
	URL        string
	Download   string
	Size       int64
	ModTime    time.Time
	IsDir      bool
	IsLink     bool
	Broken     bool
	LinkTarget string
	Preview    bool
}

func (a *App) browse(w http.ResponseWriter, r *http.Request) {
	current, err := a.requestPath(r)
	if err != nil {
		a.pathError(w, err)
		return
	}
	info, err := a.backend.Stat(r.Context(), current)
	if err != nil {
		a.pathError(w, err)
		return
	}
	if !info.IsDir() {
		a.serveNamedFile(w, r, current, previewable(current))
		return
	}
	entries, err := a.backend.ReadDir(r.Context(), current)
	if err != nil {
		a.pathError(w, err)
		return
	}
	sortKey := r.URL.Query().Get("sort")
	if sortKey != "size" && sortKey != "modified" {
		sortKey = "name"
	}
	order := r.URL.Query().Get("order")
	if order != "desc" {
		order = "asc"
	}
	sortEntries(entries, sortKey, order == "desc")

	view := directoryView{
		Title:           a.title,
		SSHHost:         a.sshHost,
		RootPath:        a.root,
		CurrentPath:     current,
		UploadURL:       a.appURL("/upload", current, "", ""),
		ImportURL:       a.appURL("/import", current, "", ""),
		NameSortURL:     a.sortURL(current, sortKey, order, "name"),
		ModifiedSortURL: a.sortURL(current, sortKey, order, "modified"),
		SizeSortURL:     a.sortURL(current, sortKey, order, "size"),
		Breadcrumbs:     a.breadcrumbs(current, sortKey, order),
		Entries:         make([]entryView, 0, len(entries)),
	}
	view.NameSortMarker = sortMarker(sortKey, order, "name")
	view.ModifiedSortMarker = sortMarker(sortKey, order, "modified")
	view.SizeSortMarker = sortMarker(sortKey, order, "size")
	if current != a.root {
		view.HasParent = true
		view.ParentPath = path.Dir(current)
		view.ParentURL = a.appURL("/", view.ParentPath, sortKey, order)
	}
	for _, entry := range entries {
		fullName, childErr := filesystem.Child(current, entry.Name)
		if childErr != nil {
			continue
		}
		entryURL := a.appURL("/download", fullName, "", "")
		if entry.IsDir() {
			entryURL = a.appURL("/", fullName, sortKey, order)
		} else if previewable(fullName) {
			entryURL = a.appURL("/preview", fullName, "", "")
		}
		view.Entries = append(view.Entries, entryView{
			Name:       entry.Name,
			FullPath:   fullName,
			URL:        entryURL,
			Download:   a.appURL("/download", fullName, "", ""),
			Size:       entry.Size(),
			ModTime:    entry.ModTime(),
			IsDir:      entry.IsDir(),
			IsLink:     entry.IsLink(),
			Broken:     entry.IsLink() && entry.Info == nil,
			LinkTarget: entry.LinkTarget,
			Preview:    !entry.IsDir() && previewable(fullName),
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.template.Execute(w, view); err != nil {
		return
	}
}

func (a *App) requestPath(r *http.Request) (string, error) {
	name := r.URL.Query().Get("path")
	if name == "" {
		name = a.root
	}
	cleaned, err := filesystem.CleanRemotePath(name)
	if err != nil {
		return "", err
	}
	if !withinRoot(a.root, cleaned) {
		return "", errOutsideRoot
	}
	return cleaned, nil
}

func (a *App) breadcrumbs(name, sortKey, order string) []breadcrumbView {
	crumbs := []breadcrumbView{{Name: ".", URL: a.appURL("/", a.root, sortKey, order)}}
	if name == a.root {
		return crumbs
	}
	relative := strings.TrimPrefix(name, a.root)
	parts := strings.Split(strings.TrimPrefix(relative, "/"), "/")
	current := a.root
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = path.Join(current, part)
		crumbs = append(crumbs, breadcrumbView{Name: part, URL: a.appURL("/", current, sortKey, order)})
	}
	return crumbs
}

func withinRoot(root, name string) bool {
	root = path.Clean(root)
	name = path.Clean(name)
	if root == "/" {
		return strings.HasPrefix(name, "/")
	}
	return name == root || strings.HasPrefix(name, root+"/")
}

func (a *App) appURL(route, name, sortKey, order string) string {
	values := make(url.Values)
	values.Set("path", name)
	if sortKey != "" {
		values.Set("sort", sortKey)
		values.Set("order", order)
	}
	return route + "?" + values.Encode()
}

func (a *App) sortURL(name, currentKey, currentOrder, requestedKey string) string {
	order := "asc"
	if requestedKey == currentKey && currentOrder == "asc" {
		order = "desc"
	}
	return a.appURL("/", name, requestedKey, order)
}

func sortMarker(currentKey, currentOrder, requestedKey string) string {
	if currentKey != requestedKey {
		return ""
	}
	if currentOrder == "desc" {
		return " v"
	}
	return " ^"
}

func sortEntries(entries []filesystem.Entry, key string, descending bool) {
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		if left.IsDir() != right.IsDir() {
			return left.IsDir()
		}
		var less bool
		switch key {
		case "size":
			if left.Size() == right.Size() {
				less = strings.ToLower(left.Name) < strings.ToLower(right.Name)
			} else {
				less = left.Size() < right.Size()
			}
		case "modified":
			if left.ModTime().Equal(right.ModTime()) {
				less = strings.ToLower(left.Name) < strings.ToLower(right.Name)
			} else {
				less = left.ModTime().Before(right.ModTime())
			}
		default:
			less = strings.ToLower(left.Name) < strings.ToLower(right.Name)
		}
		if descending {
			return !less && strings.ToLower(left.Name) != strings.ToLower(right.Name)
		}
		return less
	})
}

func (a *App) serveFile(w http.ResponseWriter, r *http.Request, inline bool) {
	name, err := a.requestPath(r)
	if err != nil {
		a.pathError(w, err)
		return
	}
	a.serveNamedFile(w, r, name, inline)
}

func (a *App) serveNamedFile(w http.ResponseWriter, r *http.Request, name string, inline bool) {
	info, err := a.backend.Stat(r.Context(), name)
	if err != nil {
		a.pathError(w, err)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}
	f, err := a.backend.Open(r.Context(), name)
	if err != nil {
		a.pathError(w, err)
		return
	}
	defer f.Close()
	stopClose := context.AfterFunc(r.Context(), func() { _ = f.Close() })
	defer stopClose()

	w.Header().Set("Content-Type", contentType(name))
	disposition := "attachment"
	if inline && previewable(name) {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(name)}))
	http.ServeContent(w, r, path.Base(name), info.ModTime(), f)
}

func previewable(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".log", ".md", ".csv", ".json", ".yaml", ".yml",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".bmp", ".ico",
		".mp3", ".wav", ".ogg", ".m4a", ".mp4", ".webm", ".mov":
		return true
	default:
		return false
	}
}

func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".log", ".yaml", ".yml":
		return "text/plain; charset=utf-8"
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".json":
		return "application/json"
	}
	if detected := mime.TypeByExtension(strings.ToLower(path.Ext(name))); detected != "" {
		return detected
	}
	return "application/octet-stream"
}

func (a *App) upload(w http.ResponseWriter, r *http.Request) {
	directory, err := a.requestPath(r)
	if err != nil {
		a.jsonError(w, err, statusForError(err))
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		jsonMessage(w, http.StatusBadRequest, "expected a multipart upload")
		return
	}
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			jsonMessage(w, http.StatusBadRequest, "could not read upload")
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		filename, filenameErr := safeFilename(part.FileName())
		if filenameErr != nil {
			_ = part.Close()
			jsonMessage(w, http.StatusBadRequest, filenameErr.Error())
			return
		}
		destination, _ := filesystem.Child(directory, filename)
		overwrite := r.URL.Query().Get("overwrite") == "1"
		written, uploadErr := a.backend.Upload(r.Context(), destination, part, overwrite)
		_ = part.Close()
		if errors.Is(uploadErr, filesystem.ErrExists) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "destination already exists", "requires_confirmation": true, "path": destination})
			return
		}
		if uploadErr != nil {
			a.jsonError(w, uploadErr, statusForError(uploadErr))
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"path": destination, "bytes": written})
		return
	}
	jsonMessage(w, http.StatusBadRequest, "no file was provided")
}

func (a *App) importURL(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		jsonMessage(w, http.StatusBadRequest, "invalid import request")
		return
	}
	directory, err := a.requestPath(r)
	if err != nil {
		a.jsonError(w, err, statusForError(err))
		return
	}
	source, err := url.Parse(r.Form.Get("url"))
	if err != nil || (source.Scheme != "http" && source.Scheme != "https") || source.Host == "" {
		jsonMessage(w, http.StatusBadRequest, "URL must use http or https")
		return
	}
	filename := r.Form.Get("filename")
	if filename == "" {
		filename = path.Base(source.Path)
		if filename == "." || filename == "/" || filename == "" {
			filename = "download"
		}
	}
	filename, err = safeFilename(filename)
	if err != nil {
		jsonMessage(w, http.StatusBadRequest, err.Error())
		return
	}
	destination, _ := filesystem.Child(directory, filename)
	overwrite := r.URL.Query().Get("overwrite") == "1"
	existing, statErr := a.backend.Lstat(r.Context(), destination)
	if statErr == nil && existing.IsDir() {
		a.jsonError(w, fs.ErrInvalid, http.StatusBadRequest)
		return
	}
	if !overwrite && statErr == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "destination already exists", "requires_confirmation": true, "path": destination})
		return
	}
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		a.jsonError(w, statErr, statusForError(statErr))
		return
	}

	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, source.String(), nil)
	if err != nil {
		jsonMessage(w, http.StatusBadRequest, "invalid source URL")
		return
	}
	response, err := a.httpClient.Do(request)
	if err != nil {
		jsonMessage(w, http.StatusBadGateway, "could not fetch the URL from this device")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		jsonMessage(w, http.StatusBadGateway, fmt.Sprintf("URL returned HTTP %d", response.StatusCode))
		return
	}
	written, err := a.backend.Upload(r.Context(), destination, response.Body, overwrite)
	if errors.Is(err, filesystem.ErrExists) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "destination already exists", "requires_confirmation": true, "path": destination})
		return
	}
	if err != nil {
		a.jsonError(w, err, statusForError(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"path": destination, "bytes": written})
}

func safeFilename(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", errors.New("parent path traversal is not allowed")
		}
	}
	name = path.Base(name)
	if name == "" || name == "." || name == ".." || strings.IndexByte(name, 0) >= 0 {
		return "", errors.New("invalid destination filename")
	}
	return name, nil
}

func (a *App) pathError(w http.ResponseWriter, err error) {
	http.Error(w, publicError(err), statusForError(err))
}

func (a *App) jsonError(w http.ResponseWriter, err error, status int) {
	writeJSON(w, status, map[string]any{"error": publicError(err)})
}

func publicError(err error) string {
	switch {
	case errors.Is(err, errOutsideRoot):
		return "path is outside the configured root"
	case errors.Is(err, filesystem.ErrTraversal):
		return "parent path traversal is not allowed"
	case errors.Is(err, fs.ErrNotExist):
		return "remote path was not found"
	case errors.Is(err, fs.ErrPermission):
		return "permission was denied by the remote server"
	case errors.Is(err, context.Canceled):
		return "request was canceled"
	case errors.Is(err, filesystem.ErrExists):
		return "destination already exists"
	case errors.Is(err, filesystem.ErrAtomicCreateUnsupported):
		return "the remote server cannot safely create a new file without overwrite"
	case errors.Is(err, filesystem.ErrAtomicReplaceUnsupported):
		return "the remote server cannot safely replace the existing file"
	case errors.Is(err, fs.ErrInvalid):
		return "invalid remote path or destination"
	case errors.Is(err, context.DeadlineExceeded):
		return "request timed out"
	default:
		return "remote filesystem operation failed"
	}
}

func statusForError(err error) int {
	switch {
	case errors.Is(err, errOutsideRoot):
		return http.StatusForbidden
	case errors.Is(err, filesystem.ErrTraversal), errors.Is(err, fs.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, fs.ErrNotExist):
		return http.StatusNotFound
	case errors.Is(err, fs.ErrPermission):
		return http.StatusForbidden
	case errors.Is(err, filesystem.ErrExists):
		return http.StatusConflict
	case errors.Is(err, filesystem.ErrAtomicCreateUnsupported), errors.Is(err, filesystem.ErrAtomicReplaceUnsupported):
		return http.StatusNotImplemented
	case errors.Is(err, context.Canceled):
		return 499
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func jsonMessage(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func methodNotAllowed(w http.ResponseWriter, allow ...string) {
	w.Header().Set("Allow", strings.Join(allow, ", "))
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func formatSize(size int64) string {
	if size < 1024 {
		return strconv.FormatInt(size, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	unit := "B"
	for _, candidate := range units {
		value /= 1024
		unit = candidate
		if value < 1024 {
			break
		}
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + unit
}
