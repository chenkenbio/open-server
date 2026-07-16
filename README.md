# open-server

`open-server` provides a local, loopback-only web interface for browsing local files or files reached through a secure SFTP connection.

Local paths open directly without SSH. Remote targets launch the system `ssh` client and use the standard SFTP subsystem, so existing SSH aliases, keys, agents, host-key checks, `ProxyJump`, and other `ssh_config` settings continue to work. Nothing needs to be installed or started on the remote server.

For users who cannot install the binary locally, explicit `-serve` mode can run on the server and expose a token-protected network URL. This mode uses plain HTTP and is intended only for trusted networks.

## Requirements

- Go 1.25 or newer to build from source
- An SSH account with the SFTP subsystem enabled when browsing remote files
- A local OpenSSH-compatible client when browsing remote files
- Optional: `tensorboard` on the machine that owns the files when using `-tensorboard`

## Build and run

Current prerelease version: `v0.2.0-beta.1`.

```sh
go build -o open-server ./cmd/open-server
./open-server ./local/project
./open-server lab:~/projects
```

Local directories and regular files can be opened directly. A file opens immediately with navigation rooted at its parent directory:

```sh
open-server .
open-server ./paper -latex=false
open-server /data/paper/report.pdf
open-server -local README.md
```

Absolute paths, `.`, `..`, `~`, and relative paths containing a separator are recognized as local unless they use the `host:path` SSH form. Use `-local` for an ambiguous bare name; without it, a bare name such as `work` is resolved as a saved session. LaTeX tools are enabled by default for local paths and can be disabled with `-latex=false`.

A remote target has the form `host:path`:

```text
lab:/data/project       absolute path
lab:projects            path relative to the SFTP working directory
lab:~/projects          path relative to the SFTP working directory
```

`~` and `~/path` resolve against the SFTP session's working directory, which is normally the account's home directory. `~user` is treated as a literal path. Paths are resolved through SFTP; the application does not invoke a remote shell or perform shell expansion unless TensorBoard is explicitly launched.

Open multiple local, remote, or saved targets in one process by listing them in order. Direct targets get the next free loopback port, while saved sessions prefer their remembered port. Each successful target opens in its own browser tab; a target that fails does not close the others.

```sh
open-server work lab:/data/results ./local-paper
open-server -port 61000 session1 session2
```

The local web servers bind only to IPv4 loopback, print their URLs, and normally open in the default browser. Direct local/remote targets and `-serve` start scanning at 60000, or at `-port`. New saved sessions start at 61000; afterward they try their last successful port first. Use `-no-open` to print URLs without opening a browser.

```sh
./open-server -no-open lab:/data/project
./open-server -version
```

Build versioned Linux and Darwin release artifacts for amd64 and arm64 with:

```sh
./scripts/build-release.sh
```

The script also retains stable symbolic links such as `open-server-latest-linux-arm64` and `open-server-latest-darwin-arm64`, retargeting them to the version it just built. Set `CODESIGN_IDENTITY` to a Developer ID Application identity to sign the separate Darwin amd64 and arm64 artifacts; signing and notarization credentials are never assumed.

## Saved sessions and options

Save frequently used targets under a name, list them, open them by name, or delete them:

```sh
./open-server -add work lab:~/projects
./open-server -add paper ./local-paper
./open-server -list
./open-server work
./open-server -delete work
./open-server -edit
```

`-edit` opens the saved-sessions YAML with `$VISUAL`, then `$EDITOR`, or `vim` when neither variable is set. Editor variables may include quoted arguments, such as `EDITOR="code --wait"`. If the config does not exist yet, `open-server` creates an empty valid file before opening it.

Custom options supplied to `-add` are saved with the target. Options may appear before or after the target:

```sh
./open-server -add project-tb lab:/data/project -tensorboard -py /opt/venv/bin/python -latex -title "Project results"
./open-server project-tb
./open-server project-tb -tensorboard=false   # explicit CLI options override saved values
```

Saved options include `-port`, `-rsh`, `-duration`, `-title`, `-no-open`, `-tensorboard`, `--python-interpreter`/`-py`, and `-latex`. A newly added session automatically reserves the first port from 61000 not assigned to another saved session; updating a session without `-port` preserves its reservation. After a saved session starts, its assigned port is written back automatically. If that port is unavailable next time, `open-server` skips every other saved session's reservation, falls back to a free port from 61000, and saves the replacement. Remembered ports are preferences rather than strict requirements.

Sessions use human-editable YAML in the platform's user configuration directory at `open-server/sessions/saved-sessions.yaml`. On Linux this is normally `~/.config/open-server/sessions/saved-sessions.yaml`; on macOS it is normally `~/Library/Application Support/open-server/sessions/saved-sessions.yaml`; on Windows it is under `%AppData%\open-server\sessions\saved-sessions.yaml`. Reusing a name replaces its target and saved options. Local targets are expanded and stored as machine-specific absolute paths.

```yaml
version: 1
sessions:
  project-tb:
    target: lab:/data/project
    options:
      port: 61000
      title: Project results
      tensorboard: true
      python-interpreter: /opt/venv/bin/python
      latex: true
```

## Serve mode

Run `open-server` on a remote machine and expose its files to another device when a local installation is unavailable:

```sh
open-server -serve /data/project
open-server -serve -port 60123 /data/project
open-server -serve -address 10.0.0.5 /data/project
open-server -serve -token mysecret123 /data/project
```

With no path, `-serve` serves the current directory. A directory opens its listing; a single-file path opens that file directly while rooting navigation at its parent directory. By default it binds all IPv4 interfaces, displays an auto-detected reachable address, scans for an available port starting at 60000, and generates a random 32-character token. Supplying `-address` binds only that address or hostname. The initial token URL is exchanged for a per-instance HTTP-only cookie and then removed from the address bar.

`-serve` is token-protected but uses plain, unencrypted HTTP. The token limits who can use the interface, but it does not encrypt URLs, file names, uploads, downloads, or cookies. `open-server` prints this warning at startup and repeats it inside the web interface. Use it only on a trusted lab, campus, home, or private VPN network. Prefer ordinary loopback SSH/SFTP mode for untrusted networks or sensitive data.

## TensorBoard mode

Enable TensorBoard launch actions for listed folders containing `events.out.tfevents.*` files:

```sh
open-server ./runs -tensorboard
open-server lab:/data/runs -tensorboard
open-server lab:/data/runs -tensorboard -py /opt/venv/bin/python
open-server -serve -tensorboard /data/runs
```

Clicking **Launch** opens TensorBoard's Scalars tab in a new browser tab for that folder. `open-server` uses a shallow filename check, so the event files must be directly inside the folder. For a local target, TensorBoard runs locally. In SSH/SFTP mode, `open-server` starts `tensorboard` on the remote host, binds it to a randomly selected high loopback port, creates an SSH tunnel to an OS-assigned local loopback port, and proxies it under the existing local URL. Port conflicts are retried with fresh ports. In `-serve` mode, TensorBoard runs on the server's loopback interface and is exposed only through the token-protected `open-server` proxy.

By default, the external `tensorboard` command must be available on `PATH` on the machine containing the files. If TensorBoard is installed in a virtual or Conda environment, pass its interpreter with `--python-interpreter /path/to/python` or `-py /path/to/python`; `open-server` then runs that external interpreter as `python -m tensorboard.main`. Python and TensorBoard are never embedded or added as Go dependencies.

Each event-log folder gets at most one supervised TensorBoard process per `open-server` session. Repeated or simultaneous **Launch** clicks reuse the existing process and proxy; a failed launch remains retryable. Local-target and `-serve` sessions terminate the full local process group; SSH/SFTP sessions keep a control pipe to a remote supervisor that terminates and reaps TensorBoard when the tunnel closes. The cleanup runs on normal shutdown, Ctrl-C, session timeout, and startup failure. The interpreter option is saved by `-add` like the other custom options.

## LaTeX mode

LaTeX tools are enabled automatically for local targets and remain opt-in with `-latex` for SSH/SFTP and `-serve` sessions. They add a **LaTeX tools** group containing three optional columns:

```sh
open-server ./paper
open-server lab:/data/paper -latex
```

- **Table** appears only for `.csv` and `.tsv`. The **Table** button copies a complete `table` environment using `\csvautotabular`; TSV uses `separator=tab`. The suggested `\label` line is commented out by default. Add `\usepackage{csvsimple}` to the document preamble.
- **Figure** appears only for `.png`, `.jpg`, `.jpeg`, and `.pdf`. The **Figure** button copies a complete `figure` environment whose image defaults to `width=1.00\textwidth`. The suggested `\label` line is commented out by default. Add `\usepackage{graphicx}` to the preamble.
- **Preview** appears only for `.pdf`. The **Preview** button opens the PDF in a new tab and checks it every two seconds. A compiling PDF is not loaded until it has a completed `%%EOF` marker, and a changed file reloads only after its size and modification time remain stable across two polls.

EPS and SVG are not enabled by default because their support depends on the TeX engine, conversion tools, or additional packages. PNG, JPEG, and PDF are the portable defaults for modern PDF-producing workflows.

## File-browser actions

The path toolbar contains:

- **Create folder** — creates one validated child directory in the current path
- **Show hidden items** / **Hide hidden items** — dot-prefixed files and folders are hidden by default
- **Copy current path** — copies the full path of the directory being viewed

Directory and file rows provide a compact **Path** button that copies the full path. File rows use an accessible download icon instead of a text button. Eligible TensorBoard event-log directories provide **Launch** when enabled, and file rows show the applicable LaTeX actions.

## Command-line reference

```text
Usage:
  open-server [options] target [target ...]
  open-server -serve [options] [local-path]
  open-server -add name target
  open-server -delete name
  open-server -list
  open-server -edit
  -add string
        save or update a named session
  -address string
        reachable IPv4 address or hostname for serve mode (default auto-detected)
  -delete string
        delete a named session
  -duration duration
        session duration (default 7d; for example 2h)
  -edit
        edit the saved sessions config
  -latex
        show LaTeX table, figure, and live-PDF actions (default for local targets)
  -list
        list saved sessions
  -local
        interpret every target as a local path
  -no-open
        do not open a browser automatically
  -port int
        starting HTTP port (direct targets default to 60000; saved sessions remember theirs)
  -py string
        Python interpreter containing TensorBoard (shorthand)
  -python-interpreter string
        Python interpreter containing TensorBoard
  -rsh string
        OpenSSH executable or compatible wrapper (default "ssh")
  -serve
        expose this machine's path over token-protected plain HTTP
  -tensorboard
        show TensorBoard launch actions for event-log folders
  -title string
        browser page title
  -token string
        access token for serve mode (minimum 8 characters; default random)
  -v
        print the version and exit
  -version
        print the version and exit
```

Press Ctrl-C to end every session in the process. Each session ends automatically after 7 days by default; use `-duration` to change this. A duration of `0` disables automatic expiry.

If `-rsh` points to a wrapper, it must accept normal OpenSSH arguments and replace itself with the SSH process—for example, with `exec ssh "$@"`—so `open-server` can monitor and stop connections reliably.

## Features

- Local-path and SSH/SFTP browsing through a loopback-only web interface
- Multiple isolated sessions with ordered next-free port allocation
- Standard SSH/SFTP protections with no remote installation
- Optional token-protected server-hosted mode for devices without a local installation
- Named local or SFTP shortcuts with persisted custom options in human-editable YAML
- Directory navigation with breadcrumbs and name, size, and modified-time sorting
- Hidden-item toggle, current-path copying, and folder creation
- Symlink navigation, including links whose targets are outside the starting directory
- Safe inline previews, ranged downloads, and live PDF refresh in LaTeX mode
- Drag-and-drop and streaming multi-file uploads
- Batch overwrite confirmation and one-shot clipboard uploads for files of any type
- URL fetching, streamed through the active filesystem backend
- Per-folder TensorBoard launch with dynamic loopback ports and SSH tunneling
- LaTeX table and figure snippet copying

## Safety and scope

The browser namespace is rooted at the exact logical starting path. Parent navigation and breadcrumbs stop at that boundary, and direct paths to ancestors or siblings are rejected. Symlinks are followed normally, including links that point outside the starting directory; the root is a navigation boundary, not a filesystem sandbox. Permissions on the authenticated account remain the final security boundary.

In ordinary local and SSH/SFTP modes, every listener is restricted to IPv4 loopback, has no path token, validates the exact `Host` header, and requires the exact local `Origin` for state-changing requests. In `-serve` mode, the generated token is additionally required. Active content such as HTML, SVG, and JavaScript is served as an attachment rather than rendered inline.

New-file uploads use OpenSSH's atomic hard-link extension. Overwrites use its atomic POSIX-rename extension. If the SFTP server lacks the required extension, the operation is refused rather than risking an unsafe overwrite or an unconfirmed new-file publication.

The **From URL** action runs on the device hosting `open-server`: the local device in ordinary local or SSH/SFTP mode, but the remote server in `-serve` mode. Anyone with access to the interface can therefore make that device fetch an HTTP(S) URL reachable from its network position.

## Development

The HTTP behavior tests run against both a temporary local filesystem and an in-process SFTP server.

```sh
go test ./...
go test -race ./...
go vet ./...
```
