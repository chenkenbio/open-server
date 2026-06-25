package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackpal/gateway"
)

type Entry struct {
	Name         string
	Href         string
	FullPath     string
	ModTime      string
	ModTimeValue time.Time
	Size         string
	SizeBytes    int64
	IsDir        bool
}

type forbiddenPageData struct {
	Title   string
	Message string
	Detail  string
}

type sortState struct {
	Column string
	Order  string
}

type sortLinks struct {
	QuerySuffix    string
	NameHref       string
	NameMarker     string
	ModifiedHref   string
	ModifiedMarker string
	SizeHref       string
	SizeMarker     string
}

type breadcrumb struct {
	Label string
	Href  string
}

type serveMode string

const (
	serveModeTCP serveMode = "tcp-http"
	serveModeSSH serveMode = "ssh-unix"
)

type serveOptions struct {
	Mode         serveMode
	Address      string
	PortLo       int
	PortHi       int
	FileDir      string
	FileBase     string
	Title        string
	DisplayRoot  string
	Token        string
	TBBin        string
	TBDir        string
	Duration     time.Duration
	LocalPort    int
	SocketPath   string
	SSHHost      string
	SecureCookie bool
	StartWatcher bool
}

const (
	sortByName     = "name"
	sortByModified = "modified"
	sortBySize     = "size"
	sortOrderAsc   = "asc"
	sortOrderDesc  = "desc"

	legacyAuthCookieName = "open_server_token"
	authCookieNamePrefix = "open_server_token_"
	authCookieMaxAge     = 12 * 60 * 60
)

func parsePortSpec(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, errors.New("empty port spec")
	}
	parts := strings.SplitN(s, "-", 2)
	lo, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid port %q: %w", parts[0], err)
	}
	hi := lo
	if len(parts) == 2 {
		hi, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid port %q: %w", parts[1], err)
		}
	}
	if lo < 1 || lo > 65535 || hi < 1 || hi > 65535 {
		return 0, 0, fmt.Errorf("port out of range [1,65535]: %d-%d", lo, hi)
	}
	if lo > hi {
		return 0, 0, fmt.Errorf("invalid port range %d-%d: lower bound exceeds upper", lo, hi)
	}
	return lo, hi, nil
}

func validateToken(s string) error {
	if len(s) < 8 {
		return fmt.Errorf("token must be at least 8 characters (got %d)", len(s))
	}
	return nil
}

func parseDurationSpec(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("empty duration")
	}
	unit := s[len(s)-1]
	value := s[:len(s)-1]
	if value == "" {
		return 0, fmt.Errorf("missing duration value in %q", s)
	}
	var duration time.Duration
	var err error
	switch unit {
	case 'd':
		days, parseErr := strconv.ParseFloat(value, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("invalid day value %q: %w", value, parseErr)
		}
		duration = time.Duration(days * float64(24*time.Hour))
	case 'h', 'm':
		duration, err = time.ParseDuration(s)
		if err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("unsupported duration suffix %q; use d, h, or m", string(unit))
	}
	if duration <= 0 {
		return 0, fmt.Errorf("duration must be greater than zero")
	}
	return duration, nil
}

func formatDurationSpec(duration time.Duration) string {
	if duration%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", duration/(24*time.Hour))
	}
	if duration%time.Hour == 0 {
		return fmt.Sprintf("%dh", duration/time.Hour)
	}
	if duration%time.Minute == 0 {
		return fmt.Sprintf("%dm", duration/time.Minute)
	}
	return duration.String()
}

func parseSortState(values url.Values) sortState {
	column := values.Get("sort")
	switch column {
	case sortByName, sortByModified, sortBySize:
	default:
		column = sortByName
	}
	order := values.Get("order")
	if order != sortOrderDesc {
		order = sortOrderAsc
	}
	return sortState{Column: column, Order: order}
}

func sortMarker(state sortState, column string) string {
	if state.Column != column {
		return ""
	}
	if state.Order == sortOrderDesc {
		return " v"
	}
	return " ^"
}

func nextSortOrder(state sortState, column string) string {
	if state.Column == column && state.Order == sortOrderAsc {
		return sortOrderDesc
	}
	return sortOrderAsc
}

func sortHref(state sortState, column string) string {
	values := url.Values{}
	values.Set("sort", column)
	values.Set("order", nextSortOrder(state, column))
	return "?" + values.Encode()
}

func querySuffix(state sortState) string {
	values := url.Values{}
	values.Set("sort", state.Column)
	values.Set("order", state.Order)
	return "?" + values.Encode()
}

func cleanURLPath(requestPath string) string {
	return path.Clean("/" + strings.TrimPrefix(requestPath, "/"))
}

func uploadURL(requestPath string) string {
	values := url.Values{}
	values.Set("dir", cleanURLPath(requestPath))
	return "/upload?" + values.Encode()
}

func makeSortLinks(state sortState) sortLinks {
	return sortLinks{
		QuerySuffix:    querySuffix(state),
		NameHref:       sortHref(state, sortByName),
		NameMarker:     sortMarker(state, sortByName),
		ModifiedHref:   sortHref(state, sortByModified),
		ModifiedMarker: sortMarker(state, sortByModified),
		SizeHref:       sortHref(state, sortBySize),
		SizeMarker:     sortMarker(state, sortBySize),
	}
}

func compareEntries(a, b Entry, column string) int {
	switch column {
	case sortBySize:
		if a.SizeBytes < b.SizeBytes {
			return -1
		}
		if a.SizeBytes > b.SizeBytes {
			return 1
		}
	case sortByModified:
		if a.ModTimeValue.Before(b.ModTimeValue) {
			return -1
		}
		if a.ModTimeValue.After(b.ModTimeValue) {
			return 1
		}
	default:
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
	}
	if a.Name < b.Name {
		return -1
	}
	if a.Name > b.Name {
		return 1
	}
	return 0
}

func sortEntries(entries []Entry, state sortState) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		cmp := compareEntries(entries[i], entries[j], state.Column)
		if state.Order == sortOrderDesc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	suffixes := []string{"K", "M", "G", "T", "P"}
	if exp >= len(suffixes) {
		exp = len(suffixes) - 1
	}
	return fmt.Sprintf("%.1f%s", float64(n)/float64(div), suffixes[exp])
}

func generateRandomToken(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func getIPs() (ips []string) {
	interfaceAddr, err := net.InterfaceAddrs()
	if err != nil {
		fmt.Printf("fail to get net interface addrs: %v", err)
		return ips
	}
	for _, address := range interfaceAddr {
		if ipNet, isValidIpNet := address.(*net.IPNet); isValidIpNet && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				ips = append(ips, ipNet.IP.String())
			}
		}
	}
	return ips
}

func getGateway() string {
	gw, err := gateway.DiscoverGateway()
	if err != nil {
		return ""
	}
	return gw.String()
}

func expandHomePath(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func parsePath(path string) (string, string, error) {
	path = expandHomePath(path)
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("file or directory %q does not exist", path)
		}
		return "", "", fmt.Errorf("cannot access %q: %w", path, err)
	}
	switch mode := fi.Mode(); {
	case mode.IsDir():
		fileDir, err := filepath.Abs(path)
		if err != nil {
			return "", "", fmt.Errorf("cannot resolve directory %q: %w", path, err)
		}
		return fileDir, "", nil
	case mode.IsRegular():
		fileDir, err := filepath.Abs(filepath.Dir(path))
		if err != nil {
			return "", "", fmt.Errorf("cannot resolve file directory %q: %w", path, err)
		}
		return fileDir, filepath.Base(path), nil
	default:
		return "", "", fmt.Errorf("%q is not a regular file or directory", path)
	}
}

func panicMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("FATAL: server crashed with panic: %v\n%s", err, string(debug.Stack()))
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func tokenMatches(got, want string) bool {
	gotHash := sha256.Sum256([]byte(got))
	wantHash := sha256.Sum256([]byte(want))
	return len(got) == len(want) && subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) == 1
}

func tokenStrippedURI(r *http.Request) string {
	nextURL := *r.URL
	values := nextURL.Query()
	values.Del("token")
	nextURL.RawQuery = values.Encode()
	return nextURL.RequestURI()
}

func authCookieName(token string) string {
	sum := sha256.Sum256([]byte(token))
	return authCookieNamePrefix + hex.EncodeToString(sum[:])[:16]
}

func setAuthCookie(w http.ResponseWriter, token string, secureCookie bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName(token),
		Value:    token,
		Path:     "/",
		MaxAge:   authCookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureCookie,
	})
}

func tokenAuthMiddleware(next http.Handler, token string, secureCookie bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			http.NotFound(w, r)
			return
		}
		queryToken := r.URL.Query().Get("token")
		cookieToken := ""
		legacyCookieToken := ""
		if cookie, err := r.Cookie(authCookieName(token)); err == nil {
			cookieToken = cookie.Value
		}
		if cookie, err := r.Cookie(legacyAuthCookieName); err == nil {
			legacyCookieToken = cookie.Value
		}

		queryValid := tokenMatches(queryToken, token)
		cookieValid := tokenMatches(cookieToken, token)
		legacyCookieValid := tokenMatches(legacyCookieToken, token)
		if queryValid || cookieValid || legacyCookieValid {
			if queryValid || legacyCookieValid {
				setAuthCookie(w, token, secureCookie)
			}
			if queryValid {
				if r.Method == http.MethodGet || r.Method == http.MethodHead {
					http.Redirect(w, r, tokenStrippedURI(r), http.StatusSeeOther)
					return
				}
			}
			next.ServeHTTP(w, r)
		} else {
			writeForbiddenPage(w, queryToken != "", cookieToken != "" || legacyCookieToken != "")
		}
	})
}

func writeForbiddenPage(w http.ResponseWriter, hasQueryToken, hasCookieToken bool) {
	data := forbiddenPageData{
		Title:   "Secure link needed",
		Message: "This file server is protected by a temporary access token.",
		Detail:  "Token status: missing from URL and browser cookie.",
	}
	if hasQueryToken || hasCookieToken {
		data.Title = "Token did not match"
		data.Message = "The request reached the server, but the provided token does not match this running instance. This usually means the tunnel is forwarding to a different or restarted server process."
		data.Detail = "Token status: present but invalid for this server."
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	tmpl, err := template.New("forbidden").Parse(forbiddenTemplate)
	if err != nil {
		_, _ = io.WriteString(w, "Forbidden")
		return
	}
	_ = tmpl.Execute(w, data)
}

func isPathWithin(path, root string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	if cleanPath == cleanRoot {
		return true
	}
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func openURL(host string, port int, fileBase, token string) string {
	path := "/"
	if fileBase != "" {
		path = "/" + url.PathEscape(fileBase)
	}
	return fmt.Sprintf("http://%s:%d%s?token=%s", host, port, path, token)
}

func openURLWithPath(host string, port int, requestPath, token string) string {
	if requestPath == "" {
		requestPath = "/"
	}
	return fmt.Sprintf("http://%s:%d%s?token=%s", host, port, requestPath, token)
}

func logicalWorkingDir() string {
	pwd := os.Getenv("PWD")
	if filepath.IsAbs(pwd) {
		cwdInfo, cwdErr := os.Stat(".")
		pwdInfo, pwdErr := os.Stat(pwd)
		if cwdErr == nil && pwdErr == nil && os.SameFile(cwdInfo, pwdInfo) {
			return pwd
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func defaultPageTitle(path, fileBase string) string {
	titlePath := expandHomePath(path)
	if fileBase != "" {
		titlePath = filepath.Dir(titlePath)
	}
	if !filepath.IsAbs(titlePath) {
		titlePath = filepath.Join(logicalWorkingDir(), titlePath)
	}
	return filepath.Clean(titlePath)
}

func displayPath(root, requestPath string) string {
	cleanRequest := filepath.Clean(requestPath)
	if cleanRequest == "." || cleanRequest == string(os.PathSeparator) {
		return filepath.Clean(root)
	}
	rel := strings.TrimPrefix(cleanRequest, string(os.PathSeparator))
	return filepath.Clean(filepath.Join(root, rel))
}

func makeBreadcrumbs(requestPath, querySuffix string) []breadcrumb {
	cleanPath := path.Clean("/" + strings.TrimPrefix(requestPath, "/"))
	crumbs := []breadcrumb{{Label: ".", Href: "/" + querySuffix}}
	if cleanPath == "/" {
		return crumbs
	}

	currentPath := ""
	for _, segment := range strings.Split(strings.Trim(cleanPath, "/"), "/") {
		if segment == "" {
			continue
		}
		currentPath += "/" + url.PathEscape(segment)
		crumbs = append(crumbs, breadcrumb{
			Label: segment,
			Href:  currentPath + "/" + querySuffix,
		})
	}
	return crumbs
}

func printOpenLink(url string) {
	fmt.Println()
	fmt.Println("File server ready")
	fmt.Println("Open this secure link in your browser:")
	sepLine := strings.Repeat("━", len(url)+2)
	fmt.Printf("\n┏%s┓\n┃ %s ┃\n┗%s┛\n", sepLine, url, sepLine)
}

func printSSHOpenLink(command, url string) {
	fmt.Println()
	fmt.Println("File server ready")
	fmt.Println("Server is listening on a private Unix socket.")
	fmt.Println("Run this SSH tunnel from your local device:")
	fmt.Printf("\n  %s\n", command)
	fmt.Println()
	fmt.Println("Then open this local link in your browser:")
	sepLine := strings.Repeat("━", len(url)+2)
	fmt.Printf("\n┏%s┓\n┃ %s ┃\n┗%s┛\n", sepLine, url, sepLine)
}

func pickPort(lo, hi int) (int, error) {
	if lo == hi {
		return lo, nil
	}
	span := int64(hi - lo + 1)
	r, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		return 0, err
	}
	return lo + int(r.Int64()), nil
}

func bindListener(address string, lo, hi int) (net.Listener, int, error) {
	if lo == hi {
		ln, err := net.Listen("tcp", net.JoinHostPort(address, strconv.Itoa(lo)))
		if err != nil {
			return nil, 0, fmt.Errorf("cannot bind %s:%d: %w", address, lo, err)
		}
		return ln, lo, nil
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		port, err := pickPort(lo, hi)
		if err != nil {
			lastErr = err
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(address, strconv.Itoa(port)))
		if err == nil {
			return ln, port, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("could not bind any port in [%d,%d] after 5 attempts: %w", lo, hi, lastErr)
}

func hasParentDirSegment(requestPath string) bool {
	for _, segment := range strings.Split(requestPath, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func uploadTargetDir(fileDir, requestPath string) (string, error) {
	if hasParentDirSegment(requestPath) {
		return "", fmt.Errorf("parent directory segments are not allowed in upload target")
	}
	cleanRequest := cleanURLPath(requestPath)
	rel := strings.TrimPrefix(cleanRequest, "/")
	targetDir := filepath.Join(fileDir, filepath.FromSlash(rel))
	absRoot, rootErr := filepath.Abs(fileDir)
	absTarget, targetErr := filepath.Abs(targetDir)
	if rootErr != nil || targetErr != nil {
		return "", fmt.Errorf("cannot resolve upload target")
	}
	if !isPathWithin(absTarget, absRoot) {
		return "", fmt.Errorf("upload target escapes served root")
	}
	info, err := os.Stat(absTarget)
	if err != nil {
		return "", fmt.Errorf("cannot access upload target: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("upload target is not a directory")
	}
	return absTarget, nil
}

var errUploadConflict = errors.New("upload destination already exists")

func openUploadDestination(dstPath string, overwrite bool) (*os.File, error) {
	info, err := os.Stat(dstPath)
	if err == nil && info.IsDir() {
		return nil, errUploadConflict
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	flags := os.O_WRONLY | os.O_CREATE
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	dst, err := os.OpenFile(dstPath, flags, 0o666)
	if err != nil {
		if os.IsExist(err) {
			return nil, errUploadConflict
		}
		return nil, err
	}
	return dst, nil
}

func uploadHandler(fileDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		targetDir, targetErr := uploadTargetDir(fileDir, r.URL.Query().Get("dir"))
		if targetErr != nil {
			http.Error(w, "Forbidden: invalid upload target", http.StatusForbidden)
			return
		}
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "Bad request: expected multipart/form-data", http.StatusBadRequest)
			return
		}
		overwrite := r.URL.Query().Get("overwrite") == "1"
		absDir, absErr := filepath.Abs(targetDir)
		if absErr != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		absDir = filepath.Clean(absDir)
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if part.FileName() == "" {
				continue
			}
			dstPath := filepath.Join(targetDir, part.FileName())
			absDst, absErr2 := filepath.Abs(dstPath)
			if absErr2 != nil || !isPathWithin(absDst, absDir) {
				http.Error(w, "Forbidden: path traversal not allowed", http.StatusForbidden)
				return
			}
			dst, err := openUploadDestination(dstPath, overwrite)
			if err != nil {
				if errors.Is(err, errUploadConflict) {
					http.Error(w, "Conflict: file already exists", http.StatusConflict)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, copyErr := io.Copy(dst, part)
			closeErr := dst.Close()
			if copyErr != nil {
				http.Error(w, copyErr.Error(), http.StatusInternalServerError)
				return
			}
			if closeErr != nil {
				http.Error(w, closeErr.Error(), http.StatusInternalServerError)
				return
			}
			log.Printf("Uploaded file: %q to %q", part.FileName(), targetDir)
		}
		w.WriteHeader(http.StatusOK)
	}
}

func serveFiles(address string, portLo, portHi int, fileDir, fileBase, title, displayRoot, token, tbBin, tbDir string, duration time.Duration) error {
	return serveFilesWithOptions(serveOptions{
		Mode:        serveModeTCP,
		Address:     address,
		PortLo:      portLo,
		PortHi:      portHi,
		FileDir:     fileDir,
		FileBase:    fileBase,
		Title:       title,
		DisplayRoot: displayRoot,
		Token:       token,
		TBBin:       tbBin,
		TBDir:       tbDir,
		Duration:    duration,
	})
}

func serveFilesWithOptions(opt serveOptions) error {
	if opt.Mode == "" {
		opt.Mode = serveModeTCP
	}
	appMux := http.NewServeMux()
	appMux.HandleFunc("/upload", uploadHandler(opt.FileDir))

	var tb *tbProcess
	if opt.TBDir != "" {
		var err error
		tb, err = startTensorboard(opt.TBBin, opt.TBDir)
		if err != nil {
			return err
		}
		// Register before finalHandler is built so the proxy inherits the token
		// auth and panic middleware below.
		appMux.Handle(tbPathPrefix+"/", tb.proxy)
		appMux.HandleFunc(tbPathPrefix, func(w http.ResponseWriter, r *http.Request) {
			target := tbPathPrefix + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		})
	}

	appMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fullPath := filepath.Join(opt.FileDir, r.URL.Path)
		absFileDir, absErr := filepath.Abs(opt.FileDir)
		absFullPath, absPathErr := filepath.Abs(fullPath)
		if absErr != nil || absPathErr != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if !isPathWithin(absFullPath, absFileDir) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		info, err := os.Stat(fullPath)
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if !info.IsDir() {
			http.ServeFile(w, r, fullPath)
			return
		}
		dirEntries, err := os.ReadDir(fullPath)
		if err != nil {
			http.Error(w, "Failed to read directory", http.StatusInternalServerError)
			return
		}
		sortState := parseSortState(r.URL.Query())
		currentPath := displayPath(opt.DisplayRoot, r.URL.Path)
		entries := make([]Entry, 0, len(dirEntries))
		for _, de := range dirEntries {
			fi, infoErr := de.Info()
			modTime := ""
			var modTimeValue time.Time
			size := "-"
			var sizeBytes int64
			if infoErr == nil {
				modTimeValue = fi.ModTime()
				modTime = modTimeValue.Format("2006-01-02 15:04")
				if !de.IsDir() {
					sizeBytes = fi.Size()
					size = humanSize(sizeBytes)
				}
			}
			name := de.Name()
			href := name
			if de.IsDir() {
				href = name + "/"
				name = name + "/"
			}
			entries = append(entries, Entry{
				Name:         name,
				Href:         href,
				FullPath:     filepath.Join(currentPath, de.Name()),
				ModTime:      modTime,
				ModTimeValue: modTimeValue,
				Size:         size,
				SizeBytes:    sizeBytes,
				IsDir:        de.IsDir(),
			})
		}
		sortEntries(entries, sortState)
		var parentDir string
		var parentPath string
		if absFullPath != absFileDir {
			parentDir = filepath.Join(r.URL.Path, "..")
			parentPath = displayPath(opt.DisplayRoot, parentDir)
		}
		data := struct {
			Entries     []Entry
			PageTitle   string
			Breadcrumbs []breadcrumb
			ParentDir   string
			ParentPath  string
			Sort        sortLinks
			UploadURL   string
		}{
			Entries:     entries,
			PageTitle:   opt.Title,
			Breadcrumbs: makeBreadcrumbs(r.URL.Path, querySuffix(sortState)),
			ParentDir:   parentDir,
			ParentPath:  parentPath,
			Sort:        makeSortLinks(sortState),
			UploadURL:   uploadURL(r.URL.Path),
		}
		tmpl, err := template.New("dir").Parse(htmlTemplate)
		if err != nil {
			log.Printf("Template parsing error: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err = tmpl.Execute(w, data)
		if err != nil {
			log.Printf("Template execution error: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	})

	finalHandler := panicMiddleware(tokenAuthMiddleware(appMux, opt.Token, opt.SecureCookie))
	server := &http.Server{
		Handler:      finalHandler,
		ReadTimeout:  10 * time.Minute,
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	var (
		boundPort int
		listener  net.Listener
		err       error
	)
	switch opt.Mode {
	case serveModeTCP:
		listener, boundPort, err = bindListener(opt.Address, opt.PortLo, opt.PortHi)
		if err != nil {
			return err
		}
		link := openURL(opt.Address, boundPort, opt.FileBase, opt.Token)
		printOpenLink(link)
		fmt.Printf(
			"\nServing:  %s\nAddress:  http://%s:%d/\nDuration: %s\nStop:     Ctrl+C\n",
			opt.FileDir,
			opt.Address,
			boundPort,
			formatDurationSpec(opt.Duration),
		)
	case serveModeSSH:
		target, err := prepareSSHSocketTarget(opt.SocketPath)
		if err != nil {
			return err
		}
		defer target.cleanup()
		if opt.StartWatcher {
			if _, err := startCleanupWatcher(target); err != nil {
				return fmt.Errorf("could not start cleanup watcher: %w", err)
			}
		}
		listener, err = net.Listen("unix", target.SocketPath)
		if err != nil {
			return fmt.Errorf("cannot listen on Unix socket %q: %w", target.SocketPath, err)
		}
		link := openURL("127.0.0.1", opt.LocalPort, opt.FileBase, opt.Token)
		printSSHOpenLink(sshTunnelCommand(opt.SSHHost, opt.LocalPort, target.SocketPath), link)
		fmt.Printf(
			"\nServing:  %s\nSocket:   %s\nDuration: %s\nStop:     Ctrl+C\n",
			opt.FileDir,
			target.SocketPath,
			formatDurationSpec(opt.Duration),
		)
	default:
		return fmt.Errorf("unsupported serve mode %q", opt.Mode)
	}
	defer listener.Close()

	if tb != nil {
		// Backstop in case neither the timer nor the signal path runs (e.g. an
		// unexpected Serve error). stop() is guarded by sync.Once.
		defer tb.stop()
		if tb.waitReady(15 * time.Second) {
			tbLink := ""
			if opt.Mode == serveModeSSH {
				tbLink = openURLWithPath("127.0.0.1", opt.LocalPort, tbPathPrefix+"/", opt.Token)
			} else {
				tbLink = openURLWithPath(opt.Address, boundPort, tbPathPrefix+"/", opt.Token)
			}
			fmt.Printf("\nTensorBoard ready (logdir: %s)\nOpen: %s\n", opt.TBDir, tbLink)
		} else {
			tbLink := ""
			if opt.Mode == serveModeSSH {
				tbLink = openURLWithPath("127.0.0.1", opt.LocalPort, tbPathPrefix+"/", opt.Token)
			} else {
				tbLink = openURLWithPath(opt.Address, boundPort, tbPathPrefix+"/", opt.Token)
			}
			fmt.Printf("\nTensorBoard did not become ready within 15s; it may still be starting.\nTry: %s\n", tbLink)
		}
	}

	timer := time.AfterFunc(opt.Duration, func() {
		log.Printf("Duration %s reached; shutting down", formatDurationSpec(opt.Duration))
		if tb != nil {
			tb.stop()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Graceful shutdown failed: %v", err)
			_ = server.Close()
		}
	})
	defer timer.Stop()

	// Handle Ctrl+C / SIGTERM so the TensorBoard subprocess is reaped instead of
	// orphaned. Without TensorBoard there is no prior signal handler either, but
	// it is only required when a child process is running.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		log.Printf("Signal received; shutting down")
		if tb != nil {
			tb.stop()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Graceful shutdown failed: %v", err)
			_ = server.Close()
		}
	}()

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("file server stopped unexpectedly: %w", err)
	}
	return nil
}
