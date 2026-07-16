# open-server

**A lightweight browser for local and SSH/SFTP files, with LaTeX and TensorBoard helpers.**

`open-server` runs as a single local binary and opens files in a web browser. For
remote paths, it uses the system `ssh` client and the standard SFTP subsystem.
Your SSH aliases, keys, agent, host-key checks, `ProxyJump`, and other
`ssh_config` settings remain in use. No `open-server` agent, package, or service
is installed on the remote machine.

## Main features

- **No remote `open-server` installation** — the default remote mode needs only
  SSH/SFTP access already provided by the server.
- **Local browser interface** — ordinary local and SSH/SFTP sessions listen only
  on IPv4 loopback.
- **TensorBoard helper** — launches TensorBoard beside remote event files,
  creates the SSH tunnel, proxies the page, and stops the process with the
  session.
- **LaTeX helper** — copies table and figure environments from listed files and
  follows a compiled PDF without moving the project to the local machine.
- **One interface for several targets** — local paths, remote paths, and saved
  sessions can be opened together, each in its own browser tab.

`open-server` is a file browser, not a web editor or file-synchronization
service.

## Quick start

Prebuilt Linux and macOS binaries for amd64 and arm64 are available from
[GitHub Releases](https://github.com/chenkenbio/open-server/releases). Building
from source requires Go 1.25 or newer. Remote browsing also requires a local
OpenSSH-compatible client and an account whose server enables SFTP. TensorBoard
is optional and must be installed on the machine containing the event files.

```sh
# Build from source.
go build -o open-server ./cmd/open-server

# Browse a local project.
./open-server ./project

# Browse a remote project through SSH/SFTP.
./open-server lab:~/projects

# Enable the research helpers for a remote project.
./open-server lab:/data/project -latex -tensorboard
```

The program prints a loopback URL and normally opens it in the default browser.
Use `-no-open` to print the URL without opening a tab.

## Targets and saved sessions

Remote targets use `host:path` syntax:

```text
lab:/data/project       absolute remote path
lab:projects            path relative to the SFTP working directory
lab:~/projects          path relative to the SFTP working directory
```

Local directories and files open directly. Use `-local` when a bare name could
be mistaken for a saved session.

```sh
./open-server .
./open-server -local README.md
./open-server work lab:/data/results ./local-paper
```

Frequently used targets can be saved under a name:

```sh
./open-server --add work lab:~/projects
./open-server --list
./open-server work
./open-server --delete work
./open-server --edit
```

Options supplied to `--add` are saved with the target. Explicit command-line
options override saved values. Run `./open-server -help` for the complete CLI
reference.

## LaTeX helper

LaTeX actions are enabled automatically for local targets. Add `-latex` for
SSH/SFTP or server-hosted sessions.

```sh
./open-server lab:/data/paper -latex
```

| File | Action |
| --- | --- |
| `.csv`, `.tsv` | Copy a complete `table` environment using `\csvautotabular`. |
| `.png`, `.jpg`, `.jpeg`, `.pdf` | Copy a complete `figure` environment using `\includegraphics`. |
| `.pdf` | Open a live preview that waits for a completed, stable PDF before reloading. |

The generated snippets retain the full path from the active filesystem, so a
TeX build on the remote machine can use the listed artifact in place. Figure
snippets require `graphicx`; table snippets require `csvsimple`.

## TensorBoard helper

```sh
./open-server lab:/data/runs -tensorboard
./open-server lab:/data/runs -tensorboard -py /opt/venv/bin/python
```

A **Launch** action appears for a folder containing an
`events.out.tfevents.*` file directly inside it. In SSH/SFTP mode,
`open-server` starts TensorBoard on remote loopback, creates an SSH tunnel, and
proxies it beneath the current browser URL. Repeated launches reuse the running
process for that folder; it is stopped when the session ends.

By default, `tensorboard` must be on `PATH` on the machine containing the files.
Use `-py` or `--python-interpreter` when it belongs to a virtual or Conda
environment.

## Server-hosted fallback

If the binary cannot run on the local device, it can instead run on the machine
containing the files:

```sh
./open-server -serve /data/project
./open-server -serve -address 10.0.0.5 -port 60123 /data/project
```

With no path, `-serve` uses the current directory. By default, it binds all IPv4
interfaces, chooses an available port from 60000, and generates a random access
token. The initial token URL is exchanged for a token-scoped HTTP-only cookie
and then removed from the address bar.

> [!WARNING]
> Server-hosted mode uses token-protected but unencrypted HTTP. The token limits
> access; it does not encrypt URLs, file names, uploads, downloads, or cookies.
> Use this mode only on a trusted network or private VPN. Prefer the default
> loopback SSH/SFTP mode for sensitive data.

## Security and scope

In ordinary local and SSH/SFTP modes, the web listener is restricted to IPv4
loopback. It validates the exact `Host` header and requires the exact local
`Origin` for state-changing requests. Active content such as HTML, SVG, and
JavaScript is downloaded rather than rendered inline.

The starting path is a navigation boundary, not a filesystem sandbox. Parent
navigation stops there, but symlinks may lead outside it. Filesystem permissions
of the local or SSH account remain the final boundary.

The **From URL** action fetches from the machine running `open-server`; in
server-hosted mode, that is the remote machine.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
```

## License

[MIT](LICENSE)
