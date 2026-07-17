package filesystem

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

func TestSFTPUploadStagesPrivatelyBeforeReadingSource(t *testing.T) {
	tests := []struct {
		name      string
		overwrite bool
	}{
		{name: "create"},
		{name: "overwrite", overwrite: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			client := newSFTPTestClient(t, root)
			backend := SFTP{Client: client}
			destination := filepath.ToSlash(filepath.Join(root, "result.txt"))
			if test.overwrite {
				if err := os.WriteFile(destination, []byte("original"), 0o640); err != nil {
					t.Fatal(err)
				}
			}

			source := &inspectingReader{
				Reader: strings.NewReader("secret"),
				Inspect: func() {
					staging := uploadStagingMatches(t, root)
					if len(staging) != 1 {
						t.Fatalf("staging paths before first source read = %v, want one", staging)
					}
					directoryInfo, err := os.Lstat(staging[0])
					if err != nil {
						t.Fatal(err)
					}
					if !directoryInfo.IsDir() || directoryInfo.Mode().Perm() != 0o700 {
						t.Fatalf("staging directory mode = %v, want directory 0700", directoryInfo.Mode())
					}

					payloadInfo, err := os.Lstat(filepath.Join(staging[0], "payload"))
					if err != nil {
						t.Fatal(err)
					}
					if !payloadInfo.Mode().IsRegular() || payloadInfo.Mode().Perm() != 0o600 || payloadInfo.Size() != 0 {
						t.Fatalf("staging payload = mode %v size %d, want regular 0600 size 0", payloadInfo.Mode(), payloadInfo.Size())
					}

					contents, err := os.ReadFile(destination)
					if test.overwrite {
						if err != nil || string(contents) != "original" {
							t.Fatalf("destination before publish = %q, %v; want original", contents, err)
						}
					} else if !os.IsNotExist(err) {
						t.Fatalf("destination was exposed before publish: %q, %v", contents, err)
					}
				},
			}

			written, err := backend.Upload(context.Background(), destination, source, test.overwrite)
			if err != nil {
				t.Fatal(err)
			}
			if written != int64(len("secret")) {
				t.Fatalf("written = %d, want %d", written, len("secret"))
			}
			assertFileContents(t, destination, "secret")
			info, err := os.Stat(destination)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("published mode = %#o, want 0600", info.Mode().Perm())
			}
			if staging := uploadStagingMatches(t, root); len(staging) != 0 {
				t.Fatalf("staging paths after publish = %v, want none", staging)
			}
		})
	}
}

type inspectingReader struct {
	Reader  io.Reader
	Inspect func()
	read    bool
}

func (r *inspectingReader) Read(buffer []byte) (int, error) {
	if !r.read {
		r.read = true
		r.Inspect()
	}
	return r.Reader.Read(buffer)
}

func newSFTPTestClient(t *testing.T, workingDirectory string) *sftp.Client {
	t.Helper()
	clientConnection, serverConnection := net.Pipe()
	server, err := sftp.NewServer(serverConnection, sftp.WithServerWorkingDirectory(workingDirectory))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve() }()
	client, err := sftp.NewClientPipe(
		clientConnection,
		clientConnection,
		sftp.UseConcurrentReads(true),
		sftp.UseConcurrentWrites(true),
	)
	if err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		_ = clientConnection.Close()
		_ = serverConnection.Close()
	})
	return client
}

func uploadStagingMatches(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".open-server-upload-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}
