package filesystem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sync"

	"github.com/pkg/sftp"
)

type SFTP struct {
	Client *sftp.Client
}

func (s SFTP) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	return awaitSFTP(ctx, func() (fs.FileInfo, error) { return s.Client.Stat(name) })
}

func (s SFTP) Lstat(ctx context.Context, name string) (fs.FileInfo, error) {
	return awaitSFTP(ctx, func() (fs.FileInfo, error) { return s.Client.Lstat(name) })
}

func (s SFTP) ReadDir(ctx context.Context, name string) ([]Entry, error) {
	infos, err := awaitSFTP(ctx, func() ([]fs.FileInfo, error) { return s.Client.ReadDir(name) })
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(infos))
	for _, listedInfo := range infos {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fullName := path.Join(name, listedInfo.Name())
		linkInfo, err := s.Lstat(ctx, fullName)
		if err != nil {
			return nil, err
		}
		entry := Entry{Name: listedInfo.Name(), LinkInfo: linkInfo, Info: linkInfo}
		if linkInfo.Mode()&fs.ModeSymlink != 0 {
			entry.LinkTarget, _ = s.Readlink(ctx, fullName)
			entry.Info, _ = s.Stat(ctx, fullName)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s SFTP) Open(ctx context.Context, name string) (ReadSeekCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make(chan sftpOpenResult, 1)
	go func() {
		f, err := s.Client.Open(name)
		result <- sftpOpenResult{file: f, err: err}
	}()
	select {
	case opened := <-result:
		if opened.err != nil {
			return nil, opened.err
		}
		return &cancelableSFTPFile{ctx: ctx, file: opened.file}, nil
	case <-ctx.Done():
		go func() {
			opened := <-result
			if opened.file != nil {
				_ = opened.file.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

func (s SFTP) Mkdir(ctx context.Context, name string) error {
	_, err := awaitSFTP(ctx, func() (struct{}, error) {
		return struct{}{}, s.Client.Mkdir(name)
	})
	return err
}

func (s SFTP) Readlink(ctx context.Context, name string) (string, error) {
	return awaitSFTP(ctx, func() (string, error) { return s.Client.ReadLink(name) })
}

func (s SFTP) RealPath(ctx context.Context, name string) (string, error) {
	return awaitSFTP(ctx, func() (string, error) { return s.Client.RealPath(name) })
}

func (s SFTP) Upload(ctx context.Context, name string, src io.Reader, overwrite bool) (written int64, retErr error) {
	existing, existingErr := s.Lstat(ctx, name)
	if existingErr == nil && existing.IsDir() {
		return 0, fs.ErrInvalid
	}
	if !overwrite {
		if existingErr == nil {
			return 0, ErrExists
		} else if !isNotExist(existingErr) {
			return 0, existingErr
		}
	}

	stagingDirectory, temporary, f, err := s.openUploadFile(ctx, path.Dir(name))
	if err != nil {
		return 0, err
	}
	defer func() {
		if retErr != nil && ctx.Err() != nil {
			// pkg/sftp does not expose per-request cancellation. Complete cleanup
			// asynchronously so a disconnected HTTP request is not held open while
			// an in-flight SFTP packet finishes.
			go func() {
				s.cleanupUploadStage(stagingDirectory, temporary, f)
			}()
			return
		}
		s.cleanupUploadStage(stagingDirectory, temporary, f)
	}()

	written, err = copySFTPContext(ctx, f, src)
	if err != nil {
		return written, err
	}
	if err = ctx.Err(); err != nil {
		return written, err
	}
	if err = awaitSFTPError(ctx, f.Close); err != nil {
		return written, err
	}

	if !overwrite {
		// OpenSSH's hardlink extension gives us an atomic no-clobber publish:
		// the complete temporary inode appears at the destination only if that
		// name is still unused.
		if err = awaitSFTPError(ctx, func() error { return s.Client.Link(temporary, name) }); err != nil {
			if _, statErr := s.Lstat(ctx, name); statErr == nil {
				return written, ErrExists
			}
			if isOperationUnsupported(err) {
				return written, ErrAtomicCreateUnsupported
			}
			return written, err
		}
		if err = awaitSFTPError(ctx, func() error { return s.Client.Remove(temporary) }); err != nil {
			return written, err
		}
		if err = awaitSFTPError(ctx, func() error { return s.Client.RemoveDirectory(stagingDirectory) }); err != nil {
			return written, err
		}
		return written, nil
	}

	// The OpenSSH posix-rename extension atomically replaces an existing file.
	// If a server does not implement it, preserve the old file under a temporary
	// name until the new upload has been moved into place.
	if err = awaitSFTPError(ctx, func() error { return s.Client.PosixRename(temporary, name) }); err == nil {
		if err = awaitSFTPError(ctx, func() error { return s.Client.RemoveDirectory(stagingDirectory) }); err != nil {
			return written, err
		}
		return written, nil
	}
	if isOperationUnsupported(err) {
		// There is no race-free SFTP v3 sequence that can replace a name while
		// guaranteeing restoration of the old file. Refuse the overwrite rather
		// than risk hiding or deleting it on a server without posix-rename.
		return written, ErrAtomicReplaceUnsupported
	}
	return written, err
}

type sftpOpenResult struct {
	directory string
	name      string
	file      *sftp.File
	err       error
}

func (s SFTP) openUploadFile(ctx context.Context, parent string) (string, string, *sftp.File, error) {
	if err := ctx.Err(); err != nil {
		return "", "", nil, err
	}
	directory, err := sftpTemporaryName(parent)
	if err != nil {
		return "", "", nil, err
	}
	name := path.Join(directory, "payload")
	result := make(chan sftpOpenResult, 1)
	go func() {
		// pkg/sftp v1 cannot set the creation mode in OpenFile. Create no
		// payload until an empty staging directory has been made private.
		if err := s.Client.Mkdir(directory); err != nil {
			result <- sftpOpenResult{err: err}
			return
		}

		var f *sftp.File
		err := s.Client.Chmod(directory, 0o700)
		if err == nil {
			var info fs.FileInfo
			info, err = s.Client.Lstat(directory)
			if err == nil && (!info.IsDir() || info.Mode().Perm() != 0o700) {
				err = fmt.Errorf("SFTP upload staging directory is not private: mode %#o", info.Mode().Perm())
			}
		}
		if err == nil {
			f, err = s.Client.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		}
		if err == nil {
			err = f.Chmod(0o600)
		}
		if err == nil {
			var info fs.FileInfo
			info, err = f.Stat()
			if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0o600) {
				err = fmt.Errorf("SFTP upload staging file is not private: mode %#o", info.Mode().Perm())
			}
		}
		if err != nil {
			s.cleanupUploadStage(directory, name, f)
			f = nil
		}
		result <- sftpOpenResult{directory: directory, name: name, file: f, err: err}
	}()
	select {
	case opened := <-result:
		return opened.directory, opened.name, opened.file, opened.err
	case <-ctx.Done():
		go func() {
			opened := <-result
			if opened.file != nil {
				s.cleanupUploadStage(opened.directory, opened.name, opened.file)
			}
		}()
		return "", "", nil, ctx.Err()
	}
}

func (s SFTP) cleanupUploadStage(directory, name string, f *sftp.File) {
	if f != nil {
		_ = f.Close()
	}
	if name != "" {
		_ = s.Client.Remove(name)
	}
	if directory != "" {
		_ = s.Client.RemoveDirectory(directory)
	}
}

type sftpResult[T any] struct {
	value T
	err   error
}

func awaitSFTP[T any](ctx context.Context, operation func() (T, error)) (T, error) {
	if err := ctx.Err(); err != nil {
		var zero T
		return zero, err
	}
	result := make(chan sftpResult[T], 1)
	go func() {
		value, err := operation()
		result <- sftpResult[T]{value: value, err: err}
	}()
	select {
	case completed := <-result:
		return completed.value, completed.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

func awaitSFTPError(ctx context.Context, operation func() error) error {
	_, err := awaitSFTP(ctx, func() (struct{}, error) { return struct{}{}, operation() })
	return err
}

type cancelableSFTPFile struct {
	ctx   context.Context
	file  *sftp.File
	once  sync.Once
	close error
}

type readResult struct {
	n   int
	err error
	buf []byte
}

func (f *cancelableSFTPFile) Read(p []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	result := make(chan readResult, 1)
	buffer := make([]byte, len(p))
	go func() {
		n, err := f.file.Read(buffer)
		result <- readResult{n: n, err: err, buf: buffer}
	}()
	select {
	case <-f.ctx.Done():
		return 0, f.ctx.Err()
	case read := <-result:
		copy(p, read.buf[:read.n])
		return read.n, read.err
	}
}

func (f *cancelableSFTPFile) Seek(offset int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	result := make(chan sftpSeekResult, 1)
	go func() {
		position, err := f.file.Seek(offset, whence)
		result <- sftpSeekResult{position: position, err: err}
	}()
	select {
	case <-f.ctx.Done():
		return 0, f.ctx.Err()
	case seek := <-result:
		return seek.position, seek.err
	}
}

type sftpSeekResult struct {
	position int64
	err      error
}

func (f *cancelableSFTPFile) Close() error {
	f.once.Do(func() {
		if f.ctx.Err() != nil {
			go func() { _ = f.file.Close() }()
			return
		}
		f.close = f.file.Close()
	})
	return f.close
}

type sftpWriteResult struct {
	n   int
	err error
}

type cancelableSFTPWriter struct {
	ctx  context.Context
	file *sftp.File
}

func (w cancelableSFTPWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	buffer := append([]byte(nil), p...)
	result := make(chan sftpWriteResult, 1)
	go func() {
		n, err := w.file.Write(buffer)
		result <- sftpWriteResult{n: n, err: err}
	}()
	select {
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	case write := <-result:
		return write.n, write.err
	}
}

func copySFTPContext(ctx context.Context, dst *sftp.File, src io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{cancelableSFTPWriter{ctx: ctx, file: dst}}, contextReader{ctx: ctx, r: src})
}

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist)
}

func isOperationUnsupported(err error) bool {
	var status *sftp.StatusError
	return errors.As(err, &status) && status.FxCode() == sftp.ErrSSHFxOpUnsupported
}

func sftpTemporaryName(dir string) (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return path.Join(dir, ".open-server-upload-"+hex.EncodeToString(random[:])), nil
}
