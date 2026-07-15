# remote-browser

`remote-browser` provides a local, loopback-only web interface for browsing and transferring files on a server over standard SSH/SFTP.

It is an open-source, lightweight SFTP browser and file-transfer tool that works through a local web interface without installing a separate client or service on the server.

It launches your system `ssh` client, so your existing SSH aliases, keys, agent, host-key checks, `ProxyJump`, and other `ssh_config` settings continue to work. Nothing is installed, started, or left behind on the remote server.

## Requirements

- Go 1.25 or newer to build from source
- An SSH account with the SFTP subsystem enabled
- A local OpenSSH-compatible client

## Build and run

```sh
go build -o remote-browser ./cmd/remote-browser
./remote-browser lab:~/projects
```

The target has the form `host:path`:

```text
lab:/data/project       absolute path
lab:projects            path relative to the SFTP working directory
lab:~/projects          path relative to the SFTP working directory
```

`~` and `~/path` resolve against the SFTP session's working directory, which is normally the account's home directory. `~user` is treated as a literal path. Paths are resolved through SFTP; the application does not invoke a remote shell or perform shell expansion.

The local server binds only to IPv4 loopback, prints its URL, and normally opens it in the default browser. Use `-no-open` to print the URL without opening a browser. The URL is printed even when the browser opens automatically.

```sh
./remote-browser --no-open lab:/data/project
./remote-browser --version
```

```text
Usage: remote-browser [options] host:/path
  -duration duration
        session duration (default 7d; for example 2h)
  -no-open
        print the URL without opening a browser
  -port int
        local loopback port (0 scans from 60000)
  -rsh string
        OpenSSH executable or compatible wrapper (default "ssh")
  -title string
        browser page title
  -v
        print the version and exit
  -version
        print the version and exit
```

Press Ctrl-C to end a session. Sessions end automatically after 7 days by default; use `-duration` to change this.

If `-rsh` points to a wrapper, it must accept normal OpenSSH arguments and replace itself with the SSH process—for example, with `exec ssh "$@"`—so `remote-browser` can monitor and stop the connection reliably.

## Features

- Directory navigation with breadcrumbs and name, size, and modified-time sorting
- Symlink navigation, including links whose targets are outside the starting directory
- Safe inline previews and ranged downloads
- Drag-and-drop and streaming multi-file uploads
- Batch overwrite confirmation and one-shot clipboard-image uploads
- URL fetching on the local device, streamed to the server through SFTP
- Copyable full remote paths

## Safety and scope

The browser namespace is rooted at the exact logical starting path. Parent navigation and breadcrumbs stop at that boundary, and direct paths to ancestors or siblings are rejected. Symlinks are followed normally, including links that point outside the starting directory; the root is a navigation boundary, not a filesystem sandbox. Permissions on the authenticated SSH account remain the final security boundary.

The listener is restricted to IPv4 loopback, has no path token, validates the `Host` header, and requires the exact local `Origin` for state-changing requests. Active content such as HTML, SVG, and JavaScript is served as attachments rather than rendered inline.

New-file uploads use OpenSSH's atomic hard-link extension. Overwrites use its atomic POSIX-rename extension. If the server lacks the required extension, the operation is refused rather than risking an unsafe overwrite or an unconfirmed new-file publication.

## Development

The HTTP behavior tests run against both a temporary local filesystem and an in-process SFTP server.

```sh
go test ./...
go test -race ./...
go vet ./...
```
