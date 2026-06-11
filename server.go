package main

import (
	"context"
	"crypto/rand"
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
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackpal/gateway"
)

type Entry struct {
	Name    string
	Href    string
	ModTime string
	Size    string
	IsDir   bool
}

type forbiddenPageData struct {
	Title   string
	Message string
	Detail  string
}

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

func tokenAuthMiddleware(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			http.NotFound(w, r)
			return
		}
		queryToken := r.URL.Query().Get("token")
		cookieToken := ""
		if cookie, err := r.Cookie("open_server_token"); err == nil {
			cookieToken = cookie.Value
		}

		if queryToken == token || cookieToken == token {
			if queryToken == token {
				http.SetCookie(w, &http.Cookie{
					Name:     "open_server_token",
					Value:    token,
					Path:     "/",
					MaxAge:   12 * 60 * 60,
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
				})
			}
			next.ServeHTTP(w, r)
		} else {
			writeForbiddenPage(w, queryToken != "", cookieToken != "")
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

func printOpenLink(url string) {
	fmt.Println()
	fmt.Println("File server ready")
	fmt.Println("Open this secure link in your browser:")
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

func uploadHandler(fileDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "Bad request: expected multipart/form-data", http.StatusBadRequest)
			return
		}
		absDir, absErr := filepath.Abs(fileDir)
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
			dstPath := filepath.Join(fileDir, part.FileName())
			absDst, absErr2 := filepath.Abs(dstPath)
			if absErr2 != nil || !isPathWithin(absDst, absDir) {
				http.Error(w, "Forbidden: path traversal not allowed", http.StatusForbidden)
				return
			}
			dst, err := os.Create(dstPath)
			if err != nil {
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
			log.Printf("Uploaded file: %q", part.FileName())
		}
		w.WriteHeader(http.StatusOK)
	}
}

func serveFiles(address string, portLo, portHi int, fileDir, fileBase, title, token string, duration time.Duration) error {
	appMux := http.NewServeMux()
	appMux.HandleFunc("/upload", uploadHandler(fileDir))

	appMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fullPath := filepath.Join(fileDir, r.URL.Path)
		absFileDir, absErr := filepath.Abs(fileDir)
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
		entries := make([]Entry, 0, len(dirEntries))
		for _, de := range dirEntries {
			fi, infoErr := de.Info()
			modTime := ""
			size := "-"
			if infoErr == nil {
				modTime = fi.ModTime().Format("2006-01-02 15:04")
				if !de.IsDir() {
					size = humanSize(fi.Size())
				}
			}
			name := de.Name()
			href := name
			if de.IsDir() {
				href = name + "/"
				name = name + "/"
			}
			entries = append(entries, Entry{
				Name:    name,
				Href:    href,
				ModTime: modTime,
				Size:    size,
				IsDir:   de.IsDir(),
			})
		}
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].IsDir != entries[j].IsDir {
				return entries[i].IsDir
			}
			return entries[i].Name < entries[j].Name
		})
		var parentDir string
		if absFullPath != absFileDir {
			parentDir = filepath.Join(r.URL.Path, "..")
		}
		data := struct {
			Entries   []Entry
			PageTitle string
			ParentDir string
			Token     string
		}{
			Entries: entries, PageTitle: title, ParentDir: parentDir, Token: token,
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

	finalHandler := panicMiddleware(tokenAuthMiddleware(appMux, token))
	server := &http.Server{
		Handler:      finalHandler,
		ReadTimeout:  10 * time.Minute,
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	listener, boundPort, err := bindListener(address, portLo, portHi)
	if err != nil {
		return err
	}
	defer listener.Close()

	link := openURL(address, boundPort, fileBase, token)
	printOpenLink(link)
	fmt.Printf("\nServing:  %s\nAddress:  http://%s:%d/\nDuration: %s\nStop:     Ctrl+C\n", fileDir, address, boundPort, formatDurationSpec(duration))

	timer := time.AfterFunc(duration, func() {
		log.Printf("Duration %s reached; shutting down", formatDurationSpec(duration))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Graceful shutdown failed: %v", err)
			_ = server.Close()
		}
	})
	defer timer.Stop()

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("file server stopped unexpectedly: %w", err)
	}
	return nil
}
