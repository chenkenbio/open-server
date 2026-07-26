# open-server

**A single-binary file browser for local and SSH/SFTP paths, with JupyterLab,
TensorBoard, and LaTeX support.**

`open-server` is designed to run for one user on a personal device. It provides
browser-based access to files on that device or a remote SSH host. Remote access
uses the system `ssh` client and the standard SFTP subsystem, so existing
aliases, keys, agents, host-key checks, `ProxyJump`, and other `ssh_config`
settings remain in use. The `open-server` binary remains on the personal device
and is not installed on the remote machine.

## Functions

- Browse local paths and remote SSH/SFTP paths without installing `open-server`
  on the remote host.
- Create directories; upload, paste, import, preview, and download files; copy
  paths; sort and filter directory contents; and show or hide hidden entries.
- Open several local paths, remote paths, or saved sessions in one command.
- Launch TensorBoard for event-log directories and JupyterLab for project
  directories, including remote processes reached through SSH tunnels.
- Generate LaTeX table and figure snippets and follow rebuilt PDF files.
- Share a local path from the personal device over a trusted network.

`open-server` is a file browser, not a web editor or file-synchronization
service.

## Quick start

Prebuilt Linux and macOS binaries for amd64 and arm64 are available from
[GitHub Releases](https://github.com/chenkenbio/open-server/releases). Building
from source requires Go 1.25.12 or newer. Remote browsing also requires a local
OpenSSH-compatible client and an account whose server enables SFTP. Optional
components such as TensorBoard or JupyterLab must be installed on the machine
containing the files.

```sh
# Build from source.
go build -o open-server ./cmd/open-server

# Browse a local project.
./open-server ./project

# Browse a remote project through SSH/SFTP.
./open-server lab:~/projects

# Enable the research functions for a remote project.
./open-server lab:/data/project -latex -tensorboard -jupyter
```

The program prints a loopback URL and normally opens it in the default browser.
Use `-no-open` to print the URL without opening a tab.

Use **Close open-server** in any local or SSH/SFTP file-browser header to exit
the program and stop all of its sessions, including launched JupyterLab or
TensorBoard processes. The result page records the user-close time and advises
using Ctrl-C in the terminal if manual shutdown is needed. The control is not
available with `-serve`.

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

Saved sessions keep their configured port. If that port is already in use,
`open-server` reports that another session may be running instead of silently
changing the saved port. Use `--allow-port-change` to choose and remember
another available port, or run `open-server --edit` to assign a specific port.

The file browser uses a 14 px base font. Choose an integer size from 8 through
72 with `-fontsize`; controls and dialogs scale with it, while compact table
padding gives the text more of each row's height. The option can also be saved
with a named session:

```sh
./open-server lab:~/projects -fontsize 18
./open-server --add work lab:~/projects -fontsize 18
```

## Reconnection

SSH/SFTP sessions reconnect automatically after an unexpected drop, using
bounded retries with backoff. The local URL and port stay the same, so open
browser tabs do not need to be reloaded: file operations return a temporary
503 while the SSH connection is down, and normal browsing resumes once it
reconnects. Use `-no-reconnect` to disable this and end the session
immediately on a drop instead.

Remote TensorBoard and JupyterLab processes are not stopped when the SSH
connection drops; they keep running so a brief interruption does not lose
in-progress work, and `open-server` re-attaches their tunnels automatically
once reconnected. `-reconnect-grace` (default 30m) bounds how long they wait
for that reconnection before shutting down. Closing the session explicitly,
whether from **Close open-server** or Ctrl-C, still stops them immediately
regardless of the grace window.

## File operations

Directories can be sorted by name, modification time, or size, and hidden files
can be shown when needed. `open-server` can create directories, upload multiple
files, save a file from the clipboard, import a file from a URL, copy full
paths, and download original files. Existing destination files are not replaced
without an explicit overwrite choice.

Directory listings can be filtered in the browser with a regular expression
matched against each entry's basename. Matching is case-sensitive by default;
enable **Ignore case** when needed. While a filter is applied, an **Active**
badge and blue highlighting on the input and match count make the filtered state
obvious. The current valid filter is remembered separately for each folder for
the life of the browser tab. Use the **?** button beside the filter for syntax
notes and examples.

The compact **Actions** group has fixed copy-path and file-download subcolumns,
so each action stays vertically aligned even when another action is unavailable.
The table uses only the width its columns need instead of stretching across a
full-screen window. Permanently visible separators after **Name**, **Last
modified**, and **Size** show that those columns are adjustable. Drag a separator
to resize its column; the browser remembers the widths across directories. The
separators also support Left/Right Arrow (Shift for a larger step), Home to
restore the default, and double-click to reset. Truncated values retain their
full text as a hover tooltip.

URL imports are fetched by the personal device running `open-server`, including
in SSH/SFTP and sharing modes.

## File previews

Preview and download are separate operations. Preview uses a safe representation
when possible, while download always returns the original file as an attachment.

- HTML, HTM, and XHTML render as static documents. Relative CSS, images, fonts,
  and media can load from the same browsed directory; scripts, forms, frames,
  objects, workers, and external network connections remain disabled.
  Root-relative site paths are not remapped into the browsed directory.
- Markdown (`.md` and `.markdown`) is rendered on the server with tables, task
  lists, strikethrough, autolinks, and heading IDs. Relative images and links
  use the corresponding preview routes. Raw HTML and dangerous link schemes
  are omitted. Syntax highlighting and math rendering are not included.
- SVG opens through an `open-server` page as an image rather than as an
  interactive document. Scripts, event handlers, and external resources are
  disabled; self-contained declarative animation may still run.
- Plain text, source code, CSV/TSV, XML, JavaScript, CSS, EPS/PostScript, TeX,
  PGF, and TikZ otherwise remain inert source previews. They are not compiled
  or executed by `open-server`.
- Raster images, supported audio/video, and PDFs use the browser's native
  viewer. Unknown binary formats fall back to download.
- Preview responses are sandboxed, prevent MIME sniffing and framing, and are
  restricted to the current browser origin.

Directory listing does not open or parse preview files. HTML and SVG content is
streamed when selected; Markdown is read and parsed only when selected. There
is no preview-size limit, so unusually large Markdown files or pathological
HTML/SVG graphics can still consume browser or server CPU and memory.

PDF previews use Chrome's PDFium or Firefox's PDF.js inside the browser sandbox.
The live viewer uses the bundled PDF.js runtime with embedded PDF scripting
disabled. The server does not execute PDF content or invoke an external
renderer.

## LaTeX

LaTeX actions are enabled automatically for local targets. Add `-latex` for
SSH/SFTP or sharing sessions.

```sh
./open-server lab:/data/paper -latex
```

| File | Function |
| --- | --- |
| `.csv`, `.tsv` | Copy a `table` environment or its inner `\csvautotabular` command. |
| `.png`, `.jpg`, `.jpeg`, `.pdf` | Copy a `figure` environment or its inner `\includegraphics` command. |
| `.pdf` | Follow a compiled PDF, waiting for a completed and stable file before reloading. |

Use the **Short / Full env** switch below the LaTeX heading to choose the copied
snippet format. Full environments remain the default, and the browser remembers
the selected format while navigating between directories.

The live viewer keeps the currently displayed page across rebuilds. If a rebuilt
PDF has fewer pages, it clamps to the last available page (or page 1 before a
document is available), and that clamped page remains selected on later
rebuilds.

The generated snippets retain the full path from the active filesystem, so a
TeX build on the remote machine can use the listed artifact in place. Figure
snippets require `graphicx`; table snippets require `csvsimple`.

## TensorBoard

```sh
./open-server lab:/data/runs -tensorboard
./open-server lab:/data/runs -tensorboard -py /opt/venv/bin/python
```

TensorBoard actions are shown for every directory without scanning its
contents. When an action is selected, `open-server` checks that the directory
directly contains an `events.out.tfevents.*` file before starting TensorBoard.
In SSH/SFTP mode, it starts TensorBoard on the remote host through SSH, creates
the tunnel, and proxies it through the current session. This does not install
`open-server` on the remote host. Repeated launches reuse the running process
for that directory. Its two-column control changes from **Start** to **Open**
when ready and reveals **Stop**, so it can be reopened or stopped without
ending `open-server`. TensorBoard actions are unavailable with `-serve`.

Remote TensorBoard listens on a per-launch Unix socket inside a user-owned
`0700` runtime directory. The socket is forwarded through the same SSH session
that owns the process and also requires a random token injected by the local
proxy. The remote helper disables TensorBoard's secondary fast-loader and gRPC
data-provider listeners. If Unix-socket forwarding is unavailable, the launch
fails rather than falling back to remote TCP. The selected Python environment
and its installed TensorBoard plugins are trusted executable code. Local
TensorBoard has no equivalent boundary: it is stock `tensorboard` on loopback
with no token, which assumes a personal device. See Security and scope.

By default, `tensorboard` must be on `PATH` on the machine containing the files.
Use `-py` or `--python-interpreter` when it belongs to a virtual or Conda
environment.

## JupyterLab

```sh
./open-server lab:/data/project -jupyter
./open-server lab:/data/project -jupyter -py /opt/venv/bin/python
```

JupyterLab can be started for any directory in the session. The Python kernel
executable can be selected at launch. The `-py` or `--python-interpreter` value
is the default and also supplies the JupyterLab installation; another selected
environment only needs `ipykernel`. Other kernels already installed for Jupyter
remain available from JupyterLab's kernel menu.

JupyterLab is proxied through the current browser URL. In SSH/SFTP mode,
`open-server` launches it on the remote host through SSH and creates the tunnel
without installing `open-server` remotely or exposing the Jupyter token in the
browser. One JupyterLab instance may run for each directory. Repeated starts
reuse that instance; its selected Python executable remains fixed until it is
stopped. The two-column control changes from **Start** to **Open** when ready
and reveals **Stop**. Stopping asks for confirmation because it terminates all
kernels owned by that JupyterLab instance; after it stops, another Python
executable can be selected for the next start.

Files deleted from JupyterLab are moved to a persistent trash directory inside
the directory that was launched: `.Trash-<uid>/files/`. Matching recovery
metadata is stored in `.Trash-<uid>/info/`. Keeping trash on the working
filesystem avoids `send2trash` failures when the user cannot create a trash
directory at a mount point. The trash is hidden by default and is not removed
when the `open-server` session ends.

Remote JupyterLab uses the same private Unix-socket and proxy-injected-token
boundary. Kernel connection files and IPC endpoints stay inside the private
runtime directory; unsupported Jupyter versions fail the launch rather than
falling back to TCP. The selected Jupyter environment, extensions, kernels, and
kernel provisioners are trusted executable code.

When the `open-server` session ends, it asks Jupyter to stop every kernel, then
terminates the complete Jupyter process group and removes the temporary kernel
registration. `open-server` does not disable JupyterLab's built-in notebook
autosave; the interval configured in the selected JupyterLab environment still
applies. JupyterLab actions are unavailable with `-serve`.

### Resource use

The new table layout and column resizing are browser-only and add no server
processes, dependencies, or background work. With both application flags
disabled, there are no application controls or status requests. Enabling a flag
but leaving every application stopped also launches no child process and
generates no status polling. Reloading a directory obtains its current state
while rendering the normal listing.

Each running application adds one small `open-server` lifecycle record and one
waiting Go goroutine. A visible running control polls its in-memory status once
every 10 seconds (0.1 request/second); polling pauses while the tab is hidden.
These controller costs are normally negligible compared with the launched
Python application. Each directory started in TensorBoard or JupyterLab has its
own Python service; Jupyter kernels add their own processes, and TensorBoard's
memory use grows with event data and plugins. In SSH/SFTP mode, each application
also has an SSH tunnel and a lightweight remote keeper during the reconnect
grace period. Capacity should therefore be planned from the actual Jupyter
kernels, TensorBoard logs, and number of concurrently running directories, not
from the controls themselves.

## Sharing from a personal device

`-serve` shares a local path from the personal device with another device on a
trusted network:

```sh
./open-server -serve /data/project
./open-server -serve -address 10.0.0.5 -port 60123 /data/project
```

With no path, `-serve` uses the current directory. By default, it binds all IPv4
interfaces, chooses an available port from 60000, and generates a random access
token. The initial token URL is exchanged for a token-scoped HTTP-only cookie
and then removed from the address bar. TensorBoard and JupyterLab actions are
not available in this mode.

> [!WARNING]
> Sharing mode uses token-protected but unencrypted HTTP. The token limits
> access; it does not encrypt URLs, file names, uploads, downloads, or cookies.
> Use this mode only on a trusted network or private VPN. Prefer the default
> loopback SSH/SFTP mode for sensitive data.

## Security and scope

`open-server` assumes a single trusted OS user on the personal device. In
ordinary local and SSH/SFTP modes, the web listener is restricted to IPv4
loopback. It validates the exact `Host` header and requires the exact local
`Origin` for state-changing requests. Static HTML uses a sandboxed browser MIME
type with scripts and active capabilities disabled. SVG is loaded only in image
mode with a stricter resource policy, and Markdown is converted with raw HTML
and dangerous links disabled. JavaScript and other source formats remain inert
plain text.

Local `-tensorboard` and `-jupyter` inherit that assumption. Their outer
`open-server` proxy listens on IPv4 loopback without a token, and loopback is
not a per-user boundary: on a multi-user host, every other account can reach
the enabled services through that proxy. Local JupyterLab still requires its
own random token, but `open-server` supplies it to proxied requests; local
TensorBoard has no child-service token. The SSH/SFTP form is the supported way
to use these against a shared server, because each service is then confined to
a `0600` Unix socket in a user-owned `0700` runtime directory and additionally
requires a proxy-injected token. Uploads through SSH/SFTP are staged privately
and published with mode `0600` for the same reason.

The starting path is a navigation boundary, not a filesystem sandbox. Parent
navigation stops there, but symlinks may lead outside it. Filesystem permissions
of the local or SSH account remain the final boundary.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
```

## License

[MIT](LICENSE)
