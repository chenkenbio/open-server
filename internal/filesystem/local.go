package filesystem

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Local implements Backend with the filesystem on the machine running
// open-server.
type Local struct{}

func (Local) Stat(_ context.Context, name string) (fs.FileInfo, error) {
	return os.Stat(filepath.FromSlash(name))
}

func (Local) Lstat(_ context.Context, name string) (fs.FileInfo, error) {
	return os.Lstat(filepath.FromSlash(name))
}

func (Local) ReadDir(ctx context.Context, name string) ([]Entry, error) {
	dirEntries, err := os.ReadDir(filepath.FromSlash(name))
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fullName := filepath.Join(filepath.FromSlash(name), dirEntry.Name())
		linkInfo, err := os.Lstat(fullName)
		if err != nil {
			return nil, err
		}
		entry := Entry{Name: dirEntry.Name(), LinkInfo: linkInfo, Info: linkInfo}
		if linkInfo.Mode()&fs.ModeSymlink != 0 {
			entry.LinkTarget, _ = os.Readlink(fullName)
			entry.Info, _ = os.Stat(fullName)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (Local) Open(_ context.Context, name string) (ReadSeekCloser, error) {
	return os.Open(filepath.FromSlash(name))
}

func (Local) Mkdir(_ context.Context, name string) error {
	return os.Mkdir(filepath.FromSlash(name), 0o755)
}

func (Local) Readlink(_ context.Context, name string) (string, error) {
	return os.Readlink(filepath.FromSlash(name))
}

func (Local) RealPath(_ context.Context, name string) (string, error) {
	abs, err := filepath.Abs(filepath.FromSlash(name))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(resolved), nil
}

func (Local) Upload(ctx context.Context, name string, src io.Reader, overwrite bool) (written int64, retErr error) {
	destination := filepath.FromSlash(name)
	existing, existingErr := os.Lstat(destination)
	if existingErr == nil && existing.IsDir() {
		return 0, fs.ErrInvalid
	}
	if !overwrite {
		if existingErr == nil {
			return 0, ErrExists
		} else if !errors.Is(existingErr, fs.ErrNotExist) {
			return 0, existingErr
		}
	}

	temporary, err := localTemporaryName(filepath.Dir(destination))
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = f.Close()
		if retErr != nil {
			_ = os.Remove(temporary)
		}
	}()

	written, err = copyContext(ctx, f, src)
	if err != nil {
		return written, err
	}
	if err = f.Sync(); err != nil {
		return written, err
	}
	if err = f.Close(); err != nil {
		return written, err
	}
	if !overwrite {
		// A same-directory hard link atomically publishes the completed file and
		// fails if another process created the destination in the meantime.
		if err = os.Link(temporary, destination); err != nil {
			if _, statErr := os.Lstat(destination); statErr == nil {
				return written, ErrExists
			}
			return written, err
		}
		if err = os.Remove(temporary); err != nil {
			return written, err
		}
		return written, nil
	}
	if err = os.Rename(temporary, destination); err != nil {
		return written, err
	}
	return written, nil
}

func localTemporaryName(dir string) (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return filepath.Join(dir, ".open-server-upload-"+hex.EncodeToString(random[:])), nil
}
