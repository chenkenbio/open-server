package filesystem

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
)

var (
	ErrExists                   = errors.New("destination already exists")
	ErrTraversal                = errors.New("path contains a parent traversal")
	ErrAtomicCreateUnsupported  = errors.New("server cannot safely create a new file without overwrite")
	ErrAtomicReplaceUnsupported = errors.New("server cannot safely replace an existing file")
)

// ReadSeekCloser is the file shape required by http.ServeContent.
type ReadSeekCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}

// Entry describes a directory entry. Info follows symlinks, while LinkInfo
// describes the link itself. Broken links have a nil Info value.
type Entry struct {
	Name       string
	Info       fs.FileInfo
	LinkInfo   fs.FileInfo
	LinkTarget string
}

func (e Entry) IsLink() bool {
	return e.LinkInfo != nil && e.LinkInfo.Mode()&fs.ModeSymlink != 0
}

func (e Entry) IsDir() bool {
	return e.Info != nil && e.Info.IsDir()
}

func (e Entry) Size() int64 {
	if e.Info != nil {
		return e.Info.Size()
	}
	if e.LinkInfo != nil {
		return e.LinkInfo.Size()
	}
	return 0
}

func (e Entry) ModTime() time.Time {
	if e.Info != nil {
		return e.Info.ModTime()
	}
	if e.LinkInfo != nil {
		return e.LinkInfo.ModTime()
	}
	return time.Time{}
}

// Backend is deliberately small so the HTTP behavior can run against either
// the local filesystem or an SFTP server.
type Backend interface {
	Stat(context.Context, string) (fs.FileInfo, error)
	Lstat(context.Context, string) (fs.FileInfo, error)
	ReadDir(context.Context, string) ([]Entry, error)
	Open(context.Context, string) (ReadSeekCloser, error)
	Upload(context.Context, string, io.Reader, bool) (int64, error)
	Readlink(context.Context, string) (string, error)
	RealPath(context.Context, string) (string, error)
}

// CleanRemotePath rejects lexical parent traversal in browser-supplied paths.
// Absolute paths are allowed so the web layer can enforce its configured root
// with component-aware comparisons. Symlinks are handled by the backend.
func CleanRemotePath(name string) (string, error) {
	if name == "" || strings.IndexByte(name, 0) >= 0 {
		return "", fs.ErrInvalid
	}
	for _, part := range strings.Split(strings.ReplaceAll(name, "\\", "/"), "/") {
		if part == ".." {
			return "", ErrTraversal
		}
	}
	cleaned := path.Clean(name)
	if cleaned == "." && strings.HasPrefix(name, "/") {
		cleaned = "/"
	}
	return cleaned, nil
}

func Child(dir, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", fs.ErrInvalid
	}
	return path.Join(dir, name), nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.r.Read(p)
	}
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	return io.Copy(dst, contextReader{ctx: ctx, r: src})
}
