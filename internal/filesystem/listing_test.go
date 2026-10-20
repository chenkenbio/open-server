package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

type listingInfo struct {
	name    string
	mode    fs.FileMode
	rawMode uint32
}

func (i listingInfo) Name() string       { return i.name }
func (i listingInfo) Size() int64        { return 123 }
func (i listingInfo) Mode() fs.FileMode  { return i.mode }
func (i listingInfo) ModTime() time.Time { return time.Unix(100, 0) }
func (i listingInfo) IsDir() bool        { return i.mode.IsDir() }
func (i listingInfo) Sys() any           { return &sftp.FileStat{Mode: i.rawMode} }

type listingHandler struct {
	infos   []os.FileInfo
	delay   time.Duration
	lstats  atomic.Int64
	stats   atomic.Int64
	links   atomic.Int64
	active  atomic.Int64
	peak    atomic.Int64
	gate    <-chan struct{}
	started chan struct{}
	once    sync.Once
}

func (h *listingHandler) metadata(counter *atomic.Int64) func() {
	counter.Add(1)
	active := h.active.Add(1)
	for peak := h.peak.Load(); active > peak; peak = h.peak.Load() {
		if h.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	if h.started != nil && active == 1 {
		h.once.Do(func() { close(h.started) })
	}
	if h.gate != nil {
		<-h.gate
	}
	if h.delay > 0 {
		time.Sleep(h.delay)
	}
	return func() { h.active.Add(-1) }
}

func (h *listingHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	if r.Method == "List" {
		return listingAt{infos: h.infos, delay: h.delay}, nil
	}
	defer h.metadata(&h.stats)()
	if path.Base(r.Filepath) == "broken" {
		return nil, fs.ErrNotExist
	}
	return listingAt{infos: []os.FileInfo{listingInfo{name: path.Base(r.Filepath), mode: fs.ModeDir | 0o700}}}, nil
}

func (h *listingHandler) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	defer h.metadata(&h.lstats)()
	return listingAt{infos: []os.FileInfo{listingInfo{name: path.Base(r.Filepath), mode: 0o600}}}, nil
}

func (h *listingHandler) Readlink(name string) (string, error) {
	defer h.metadata(&h.links)()
	return "../target/" + path.Base(name), nil
}

type listingAt struct {
	infos []os.FileInfo
	delay time.Duration
}

func (l listingAt) ListAt(dst []os.FileInfo, offset int64) (int, error) {
	if l.delay > 0 {
		time.Sleep(l.delay)
	}
	if offset >= int64(len(l.infos)) {
		return 0, io.EOF
	}
	n := copy(dst, l.infos[offset:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}

func listingClient(t testing.TB, handler *listingHandler) *sftp.Client {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	server := sftp.NewRequestServer(serverConn, sftp.Handlers{FileList: handler})
	go func() { _ = server.Serve() }()
	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	return client
}

func TestSFTPListingReusesDirectoryAttributes(t *testing.T) {
	handler := &listingHandler{infos: []os.FileInfo{
		listingInfo{name: "file.txt", mode: 0o600},
		listingInfo{name: "folder", mode: fs.ModeDir | 0o700},
		listingInfo{name: "empty-permissions", mode: 0},
	}}
	backend := SFTP{Client: listingClient(t, handler)}
	entries, err := backend.ReadDir(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Name != "file.txt" || entries[0].Size() != 123 || !entries[1].IsDir() {
		t.Fatalf("listing = %#v", entries)
	}
	if handler.lstats.Load() != 0 || handler.stats.Load() != 0 || handler.links.Load() != 0 {
		t.Fatalf("ordinary listing issued metadata requests: lstat=%d stat=%d readlink=%d", handler.lstats.Load(), handler.stats.Load(), handler.links.Load())
	}
}

func TestSFTPListingResolvesLinksAndUnknownTypes(t *testing.T) {
	handler := &listingHandler{infos: []os.FileInfo{
		listingInfo{name: "linked-folder", mode: fs.ModeSymlink | 0o777},
		listingInfo{name: "broken", mode: fs.ModeSymlink | 0o777},
	}}
	backend := SFTP{Client: listingClient(t, handler)}
	entries, err := backend.ReadDir(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if !entries[0].IsLink() || !entries[0].IsDir() || entries[0].LinkTarget != "../target/linked-folder" {
		t.Fatalf("followed link = %#v", entries[0])
	}
	if !entries[1].IsLink() || entries[1].Info != nil {
		t.Fatalf("broken link = %#v", entries[1])
	}
	if handler.lstats.Load() != 0 || handler.stats.Load() != 2 || handler.links.Load() != 2 {
		t.Fatal("unexpected symlink request counts")
	}
	entries, err = backend.directoryEntries(context.Background(), "/", []fs.FileInfo{listingInfo{name: "unknown", mode: 0o600}})
	if err != nil || len(entries) != 1 || entries[0].Name != "unknown" || handler.lstats.Load() != 1 {
		t.Fatalf("fallback listing = %#v, %v; lstats = %d", entries, err, handler.lstats.Load())
	}
}

func TestSFTPListingCancellationBoundsMetadataWork(t *testing.T) {
	gate := make(chan struct{})
	handler := &listingHandler{gate: gate, started: make(chan struct{})}
	backend := SFTP{Client: listingClient(t, handler)}
	// Release blocked server requests even when an assertion fails.
	defer close(gate)
	infos := make([]fs.FileInfo, 200)
	for i := range infos {
		infos[i] = listingInfo{name: fmt.Sprint(i)}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		entries, err := backend.directoryEntries(ctx, "/", infos)
		if entries != nil {
			err = fmt.Errorf("canceled listing exposed partial contents")
		}
		done <- err
	}()
	select {
	case <-handler.started:
	case <-time.After(3 * time.Second):
		t.Fatal("metadata workers did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled listing did not return promptly")
	}
	if handler.lstats.Load() > sftpDirectoryWorkers || handler.peak.Load() > sftpDirectoryWorkers {
		t.Fatalf("metadata work was not bounded: requests=%d peak=%d", handler.lstats.Load(), handler.peak.Load())
	}
}

func BenchmarkSFTPListingLatency(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		for _, linkPercent := range []int{0, 25} {
			b.Run(fmt.Sprintf("entries=%d/links=%d/latency=1ms", count, linkPercent), func(b *testing.B) {
				handler := &listingHandler{delay: time.Millisecond, infos: make([]os.FileInfo, count)}
				for i := range handler.infos {
					mode := fs.FileMode(0o600)
					if i%100 < linkPercent {
						mode |= fs.ModeSymlink
					}
					handler.infos[i] = listingInfo{name: fmt.Sprintf("entry-%05d", i), mode: mode}
				}
				backend := SFTP{Client: listingClient(b, handler)}
				b.ResetTimer()
				for range b.N {
					if _, err := backend.ReadDir(context.Background(), "/"); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(handler.lstats.Load())/float64(b.N), "lstat/op")
			})
		}
	}
}
