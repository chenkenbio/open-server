package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"open-server/internal/filesystem"
	"open-server/internal/tensorboard"
)

type Options struct {
	Backend     filesystem.Backend
	Root        string
	SSHHost     string
	Title       string
	AllowedHost string
	AccessToken string
	HTTPClient  *http.Client
	TensorBoard tensorboard.Launcher
	LaTeX       bool
}

var errOutsideRoot = errors.New("path is outside the configured root")

type tensorBoardLaunch struct {
	id     string
	prefix string
	ready  chan struct{}
	err    error
}

type App struct {
	backend     filesystem.Backend
	root        string
	sshHost     string
	title       string
	allowedHost string
	accessToken string
	csrfToken   string
	httpClient  *http.Client
	tensorBoard tensorboard.Launcher
	latex       bool
	tensorMu    sync.RWMutex
	tensorProxy map[string]http.Handler
	tensorByDir map[string]*tensorBoardLaunch
	template    *template.Template
}

func New(options Options) (*App, error) {
	if options.Backend == nil || options.AllowedHost == "" {
		return nil, errors.New("backend and allowed host are required")
	}
	if options.AccessToken != "" && len(options.AccessToken) < 8 {
		return nil, errors.New("access token must be at least 8 characters")
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
	csrfToken := ""
	if options.TensorBoard != nil {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, fmt.Errorf("create request token: %w", err)
		}
		csrfToken = hex.EncodeToString(token[:])
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
		accessToken: options.AccessToken,
		csrfToken:   csrfToken,
		httpClient:  options.HTTPClient,
		tensorBoard: options.TensorBoard,
		latex:       options.LaTeX,
		tensorProxy: make(map[string]http.Handler),
		tensorByDir: make(map[string]*tensorBoardLaunch),
		template:    tmpl,
	}, nil
}

func (a *App) URL() string {
	address := "http://" + a.allowedHost + "/"
	if a.accessToken == "" {
		return address
	}
	return address + "?token=" + url.QueryEscape(a.accessToken)
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := r.URL.Path
	proxyRoute := strings.HasPrefix(r.URL.Path, "/tensorboard/")
	if !proxyRoute {
		a.securityHeaders(w, a.latex && route == "/preview")
	}
	if r.Host != a.allowedHost {
		http.Error(w, "invalid Host header", http.StatusMisdirectedRequest)
		return
	}
	if !a.authorize(w, r) {
		return
	}
	if isStateChanging(r.Method) && !a.validStateChangingRequest(r) {
		http.Error(w, "invalid Origin header", http.StatusForbidden)
		return
	}
	if proxyRoute {
		a.serveTensorBoard(w, r)
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
	case "/mkdir":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		a.mkdir(w, r)
	case "/tensorboard":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		a.startTensorBoard(w, r)
	case "/live":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		a.livePDF(w, r)
	case "/live/status":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		a.livePDFStatus(w, r)
	default:
		http.NotFound(w, r)
	}
}

const authCookieMaxAge = 12 * 60 * 60

func (a *App) authorize(w http.ResponseWriter, r *http.Request) bool {
	if a.accessToken == "" {
		return true
	}
	queryToken := r.URL.Query().Get("token")
	cookieToken := ""
	if cookie, err := r.Cookie(authCookieName(a.accessToken)); err == nil {
		cookieToken = cookie.Value
	}
	queryValid := tokenMatches(queryToken, a.accessToken)
	if !queryValid && !tokenMatches(cookieToken, a.accessToken) {
		http.Error(w, "a valid access token is required", http.StatusForbidden)
		return false
	}
	if queryValid {
		http.SetCookie(w, &http.Cookie{
			Name: authCookieName(a.accessToken), Value: a.accessToken, Path: "/",
			MaxAge: authCookieMaxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			nextURL := *r.URL
			values := nextURL.Query()
			values.Del("token")
			nextURL.RawQuery = values.Encode()
			http.Redirect(w, r, nextURL.RequestURI(), http.StatusSeeOther)
			return false
		}
	}
	return true
}

func tokenMatches(got, want string) bool {
	gotHash := sha256.Sum256([]byte(got))
	wantHash := sha256.Sum256([]byte(want))
	return len(got) == len(want) && subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) == 1
}

func authCookieName(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "open_server_token_" + hex.EncodeToString(sum[:])[:16]
}

func (a *App) securityHeaders(w http.ResponseWriter, allowSameOriginFrame bool) {
	frameAncestors := "'none'"
	frameOptions := "DENY"
	if allowSameOriginFrame {
		frameAncestors = "'self'"
		frameOptions = "SAMEORIGIN"
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; frame-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors "+frameAncestors)
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", frameOptions)
}

func isStateChanging(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func (a *App) validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme == "http" && parsed.Host == a.allowedHost && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func (a *App) validStateChangingRequest(r *http.Request) bool {
	if a.validOrigin(r) {
		return true
	}
	if r.URL.Path != "/tensorboard" || a.csrfToken == "" || r.ParseForm() != nil {
		return false
	}
	got := []byte(r.PostForm.Get("csrf"))
	want := []byte(a.csrfToken)
	return len(got) == len(want) && subtle.ConstantTimeCompare(got, want) == 1
}

type directoryView struct {
	Title              string
	SSHHost            string
	PlainHTTPWarning   bool
	RootPath           string
	CurrentPath        string
	UploadURL          string
	ImportURL          string
	MkdirURL           string
	HiddenToggleURL    string
	HiddenToggleLabel  string
	ShowHidden         bool
	TensorBoardEnabled bool
	CSRFToken          string
	LaTeXEnabled       bool
	ColumnCount        int
	NameSortURL        string
	ModifiedSortURL    string
	SizeSortURL        string
	NameSortMarker     string
	ModifiedSortMarker string
	SizeSortMarker     string
	HasParent          bool
	ParentPath         string
	ParentURL          string
	ParentTensorBoard  string
	Breadcrumbs        []breadcrumbView
	Entries            []entryView
}

type breadcrumbView struct {
	Name string
	URL  string
}

type entryView struct {
	Name          string
	FullPath      string
	URL           string
	Download      string
	TensorBoard   string
	TableSnippet  string
	FigureSnippet string
	LiveURL       string
	Size          int64
	ModTime       time.Time
	IsDir         bool
	IsLink        bool
	Broken        bool
	LinkTarget    string
	Preview       bool
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
	showHidden := r.URL.Query().Get("hidden") == "1"
	if !showHidden {
		visible := entries[:0]
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name, ".") {
				visible = append(visible, entry)
			}
		}
		entries = visible
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
		Title:              a.title,
		SSHHost:            a.sshHost,
		PlainHTTPWarning:   a.accessToken != "",
		RootPath:           a.root,
		CurrentPath:        current,
		UploadURL:          a.appURL("/upload", current, "", "", showHidden),
		ImportURL:          a.appURL("/import", current, "", "", showHidden),
		MkdirURL:           a.appURL("/mkdir", current, "", "", showHidden),
		HiddenToggleURL:    a.appURL("/", current, sortKey, order, !showHidden),
		ShowHidden:         showHidden,
		TensorBoardEnabled: a.tensorBoard != nil,
		CSRFToken:          a.csrfToken,
		LaTeXEnabled:       a.latex,
		NameSortURL:        a.sortURL(current, sortKey, order, "name", showHidden),
		ModifiedSortURL:    a.sortURL(current, sortKey, order, "modified", showHidden),
		SizeSortURL:        a.sortURL(current, sortKey, order, "size", showHidden),
		Breadcrumbs:        a.breadcrumbs(current, sortKey, order, showHidden),
		Entries:            make([]entryView, 0, len(entries)),
	}
	view.HiddenToggleLabel = "Show hidden items"
	if showHidden {
		view.HiddenToggleLabel = "Hide hidden items"
	}
	view.ColumnCount = 5
	if view.TensorBoardEnabled {
		view.ColumnCount++
	}
	if view.LaTeXEnabled {
		view.ColumnCount += 3
	}
	view.NameSortMarker = sortMarker(sortKey, order, "name")
	view.ModifiedSortMarker = sortMarker(sortKey, order, "modified")
	view.SizeSortMarker = sortMarker(sortKey, order, "size")
	if current != a.root {
		view.HasParent = true
		view.ParentPath = path.Dir(current)
		view.ParentURL = a.appURL("/", view.ParentPath, sortKey, order, showHidden)
		if a.tensorBoard != nil && a.directoryHasTensorBoardEvents(r.Context(), view.ParentPath) {
			view.ParentTensorBoard = a.appURL("/tensorboard", view.ParentPath, "", "", showHidden)
		}
	}
	for _, entry := range entries {
		fullName, childErr := filesystem.Child(current, entry.Name)
		if childErr != nil {
			continue
		}
		entryURL := a.appURL("/download", fullName, "", "", showHidden)
		if entry.IsDir() {
			entryURL = a.appURL("/", fullName, sortKey, order, showHidden)
		} else if previewable(fullName) {
			entryURL = a.appURL("/preview", fullName, "", "", showHidden)
		}
		tensorBoardURL := ""
		if entry.IsDir() && a.tensorBoard != nil && a.directoryHasTensorBoardEvents(r.Context(), fullName) {
			tensorBoardURL = a.appURL("/tensorboard", fullName, "", "", showHidden)
		}
		figureSnippet, tableSnippet := "", ""
		liveURL := ""
		if a.latex && entry.Info != nil && entry.Info.Mode().IsRegular() {
			figureSnippet, tableSnippet = makeLaTeXSnippets(fullName)
			if strings.EqualFold(path.Ext(fullName), ".pdf") {
				liveURL = a.appURL("/live", fullName, "", "", showHidden)
			}
		}
		view.Entries = append(view.Entries, entryView{
			Name:          entry.Name,
			FullPath:      fullName,
			URL:           entryURL,
			Download:      a.appURL("/download", fullName, "", "", showHidden),
			TensorBoard:   tensorBoardURL,
			TableSnippet:  tableSnippet,
			FigureSnippet: figureSnippet,
			LiveURL:       liveURL,
			Size:          entry.Size(),
			ModTime:       entry.ModTime(),
			IsDir:         entry.IsDir(),
			IsLink:        entry.IsLink(),
			Broken:        entry.IsLink() && entry.Info == nil,
			LinkTarget:    entry.LinkTarget,
			Preview:       !entry.IsDir() && previewable(fullName),
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

func (a *App) breadcrumbs(name, sortKey, order string, showHidden bool) []breadcrumbView {
	crumbs := []breadcrumbView{{Name: ".", URL: a.appURL("/", a.root, sortKey, order, showHidden)}}
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
		crumbs = append(crumbs, breadcrumbView{Name: part, URL: a.appURL("/", current, sortKey, order, showHidden)})
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

func (a *App) appURL(route, name, sortKey, order string, showHidden bool) string {
	values := make(url.Values)
	values.Set("path", name)
	if sortKey != "" {
		values.Set("sort", sortKey)
		values.Set("order", order)
	}
	if showHidden {
		values.Set("hidden", "1")
	}
	return route + "?" + values.Encode()
}

func (a *App) sortURL(name, currentKey, currentOrder, requestedKey string, showHidden bool) string {
	order := "asc"
	if requestedKey == currentKey && currentOrder == "asc" {
		order = "desc"
	}
	return a.appURL("/", name, requestedKey, order, showHidden)
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

func makeLaTeXSnippets(name string) (figure, table string) {
	extension := strings.ToLower(path.Ext(name))
	fileArgument := `\detokenize{` + name + `}`
	label := latexLabel(strings.TrimSuffix(path.Base(name), path.Ext(name)))
	switch extension {
	case ".png", ".jpg", ".jpeg", ".pdf":
		return "\\begin{figure}[htbp]\n" +
			"  \\centering\n" +
			"  \\includegraphics[width=1.00\\textwidth]{" + fileArgument + "}\n" +
			"  \\caption{}\n" +
			"  % \\label{fig:" + label + "}\n" +
			"\\end{figure}", ""
	case ".csv", ".tsv":
		options := ""
		if extension == ".tsv" {
			options = "[separator=tab]"
		}
		return "", "\\begin{table}[htbp]\n" +
			"  \\centering\n" +
			"  \\csvautotabular" + options + "{" + fileArgument + "}\n" +
			"  \\caption{}\n" +
			"  % \\label{tab:" + label + "}\n" +
			"\\end{table}"
	default:
		return "", ""
	}
}

func latexLabel(name string) string {
	name = strings.ToLower(name)
	var label strings.Builder
	lastDash := false
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			label.WriteRune(character)
			lastDash = false
		} else if !lastDash && label.Len() > 0 {
			label.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(label.String(), "-")
	if result == "" {
		return "file"
	}
	return result
}

func (a *App) mkdir(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		jsonMessage(w, http.StatusBadRequest, "invalid create-folder request")
		return
	}
	directory, err := a.requestPath(r)
	if err != nil {
		a.jsonError(w, err, statusForError(err))
		return
	}
	name := strings.TrimSpace(r.Form.Get("name"))
	destination, err := filesystem.Child(directory, name)
	if err != nil {
		jsonMessage(w, http.StatusBadRequest, "folder name must be a single valid path component")
		return
	}
	if err := a.backend.Mkdir(r.Context(), destination); err != nil {
		a.jsonError(w, err, statusForError(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"path": destination})
}

func (a *App) startTensorBoard(w http.ResponseWriter, r *http.Request) {
	if a.tensorBoard == nil {
		http.NotFound(w, r)
		return
	}
	directory, err := a.requestPath(r)
	if err != nil {
		a.pathError(w, err)
		return
	}
	info, err := a.backend.Stat(r.Context(), directory)
	if err != nil {
		a.pathError(w, err)
		return
	}
	if !info.IsDir() {
		http.Error(w, "TensorBoard path must be a directory", http.StatusBadRequest)
		return
	}
	entries, err := a.backend.ReadDir(r.Context(), directory)
	if err != nil {
		a.pathError(w, err)
		return
	}
	if !hasTensorBoardEvents(entries) {
		http.Error(w, "TensorBoard event files were not found in this directory", http.StatusBadRequest)
		return
	}
	launch, owner, err := a.acquireTensorBoardLaunch(directory)
	if err != nil {
		http.Error(w, "could not create TensorBoard session", http.StatusInternalServerError)
		return
	}
	if owner {
		instance, startErr := a.tensorBoard.Start(directory, launch.prefix)
		var handler http.Handler
		if startErr == nil {
			if instance == nil || (instance.Target == nil && instance.Handler == nil) {
				startErr = errors.New("launcher returned no backend")
			} else {
				handler = instance.Handler
				if handler == nil {
					proxy := newTensorBoardReverseProxy(instance.Target)
					proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, _ error) {
						http.Error(response, "TensorBoard is starting or unavailable; retry shortly", http.StatusBadGateway)
					}
					handler = proxy
				}
			}
		}
		a.completeTensorBoardLaunch(directory, launch, handler, startErr)
	}
	a.respondTensorBoardLaunch(w, r, launch)
}

func (a *App) acquireTensorBoardLaunch(directory string) (*tensorBoardLaunch, bool, error) {
	a.tensorMu.Lock()
	defer a.tensorMu.Unlock()
	if existing := a.tensorByDir[directory]; existing != nil {
		return existing, false, nil
	}
	var randomID [8]byte
	if _, err := rand.Read(randomID[:]); err != nil {
		return nil, false, err
	}
	id := hex.EncodeToString(randomID[:])
	launch := &tensorBoardLaunch{id: id, prefix: "/tensorboard/" + id, ready: make(chan struct{})}
	a.tensorByDir[directory] = launch
	return launch, true, nil
}

func (a *App) completeTensorBoardLaunch(directory string, launch *tensorBoardLaunch, handler http.Handler, err error) {
	a.tensorMu.Lock()
	launch.err = err
	if err == nil {
		a.tensorProxy[launch.id] = handler
	} else if a.tensorByDir[directory] == launch {
		delete(a.tensorByDir, directory)
	}
	close(launch.ready)
	a.tensorMu.Unlock()
}

func (a *App) respondTensorBoardLaunch(w http.ResponseWriter, r *http.Request, launch *tensorBoardLaunch) {
	select {
	case <-launch.ready:
	case <-r.Context().Done():
		return
	}
	if launch.err != nil {
		http.Error(w, "could not start TensorBoard: "+launch.err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, launch.prefix+"/#scalars", http.StatusSeeOther)
}

const tensorBoardEventPrefix = "events.out.tfevents."

func hasTensorBoardEvents(entries []filesystem.Entry) bool {
	for _, entry := range entries {
		if entry.Info != nil && entry.Info.Mode().IsRegular() && len(entry.Name) > len(tensorBoardEventPrefix) && strings.HasPrefix(entry.Name, tensorBoardEventPrefix) {
			return true
		}
	}
	return false
}

func (a *App) directoryHasTensorBoardEvents(ctx context.Context, directory string) bool {
	entries, err := a.backend.ReadDir(ctx, directory)
	return err == nil && hasTensorBoardEvents(entries)
}

func newTensorBoardReverseProxy(target *url.URL) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(request *http.Request) {
		director(request)
		request.Host = target.Host
		if request.Header.Get("Origin") != "" {
			request.Header.Set("Origin", target.Scheme+"://"+target.Host)
		}
	}
	return proxy
}

func (a *App) serveTensorBoard(w http.ResponseWriter, r *http.Request) {
	remainder := strings.TrimPrefix(r.URL.Path, "/tensorboard/")
	id := strings.SplitN(remainder, "/", 2)[0]
	a.tensorMu.RLock()
	proxy := a.tensorProxy[id]
	a.tensorMu.RUnlock()
	if proxy == nil {
		http.NotFound(w, r)
		return
	}
	proxy.ServeHTTP(w, r)
}

func (a *App) livePDF(w http.ResponseWriter, r *http.Request) {
	name, info, err := a.livePDFInfo(r)
	if err != nil {
		a.pathError(w, err)
		return
	}
	ready, err := a.pdfComplete(r.Context(), name, info)
	if err != nil {
		a.pathError(w, err)
		return
	}
	initialVersion := ""
	if ready {
		initialVersion = fileVersion(info)
	}
	data := struct {
		Title          string
		PreviewURL     string
		StatusURL      string
		InitialVersion string
		InitialReady   bool
		PlainHTTP      bool
	}{
		Title:          path.Base(name),
		PreviewURL:     a.appURL("/preview", name, "", "", false),
		StatusURL:      a.appURL("/live/status", name, "", "", false),
		InitialVersion: initialVersion,
		InitialReady:   ready,
		PlainHTTP:      a.accessToken != "",
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = livePDFPage.Execute(w, data)
}

func (a *App) livePDFStatus(w http.ResponseWriter, r *http.Request) {
	name, info, err := a.livePDFInfo(r)
	if err != nil {
		a.jsonError(w, err, statusForError(err))
		return
	}
	ready, err := a.pdfComplete(r.Context(), name, info)
	if err != nil {
		a.jsonError(w, err, statusForError(err))
		return
	}
	version := ""
	if ready {
		version = fileVersion(info)
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version, "ready": ready})
}

func (a *App) pdfComplete(ctx context.Context, name string, info fs.FileInfo) (bool, error) {
	const marker = "%%EOF"
	if info.Size() < int64(len(marker)) {
		return false, nil
	}
	file, err := a.backend.Open(ctx, name)
	if err != nil {
		return false, err
	}
	defer file.Close()

	tailSize := info.Size()
	if tailSize > 4096 {
		tailSize = 4096
	}
	if _, err := file.Seek(-tailSize, io.SeekEnd); err != nil {
		return false, nil
	}
	tail, err := io.ReadAll(io.LimitReader(file, tailSize))
	if err != nil || int64(len(tail)) != tailSize {
		return false, nil
	}
	markerIndex := bytes.LastIndex(tail, []byte(marker))
	if markerIndex < 0 || !onlyPDFWhitespace(tail[markerIndex+len(marker):]) {
		return false, nil
	}
	latest, err := a.backend.Stat(ctx, name)
	if err != nil {
		return false, err
	}
	return latest.Mode().IsRegular() && fileVersion(latest) == fileVersion(info), nil
}

func onlyPDFWhitespace(value []byte) bool {
	for _, character := range value {
		switch character {
		case 0, '\t', '\n', '\f', '\r', ' ':
		default:
			return false
		}
	}
	return true
}

func (a *App) livePDFInfo(r *http.Request) (string, fs.FileInfo, error) {
	if !a.latex {
		return "", nil, fs.ErrNotExist
	}
	name, err := a.requestPath(r)
	if err != nil {
		return "", nil, err
	}
	if !strings.EqualFold(path.Ext(name), ".pdf") {
		return "", nil, fs.ErrInvalid
	}
	info, err := a.backend.Stat(r.Context(), name)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fs.ErrInvalid
	}
	return name, info, nil
}

func fileVersion(info fs.FileInfo) string {
	return strconv.FormatInt(info.Size(), 10) + "-" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
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
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".bmp", ".ico", ".pdf",
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
	case errors.Is(err, fs.ErrExist):
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
	case errors.Is(err, fs.ErrExist):
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
