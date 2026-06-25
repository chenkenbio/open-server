# open-server

[![ci](https://github.com/chenkenbio/open-server/actions/workflows/ci.yml/badge.svg)](https://github.com/chenkenbio/open-server/actions/workflows/ci.yml)
[![license](https://img.shields.io/badge/license-GPLv3-blue.svg)](LICENSE)

Standalone HTTP file-sharing server: token-authenticated browse + drag-and-drop upload, in a single Go binary. Listings render in the classic Apache `IndexOptions FancyIndexing` style (modeled on [UCSC's hgdownload](https://hgdownload.cse.ucsc.edu/goldenpath/hg38/)).

## Security warning

By default, `open-server` is intended for temporary file sharing on trusted lab, campus, or home networks. The default mode serves plain HTTP on a TCP port. The access token limits who can browse the server, but HTTP does not encrypt URLs, headers, cookies, file names, uploads, or downloads in transit. Anyone who can observe the network path may be able to read or modify traffic, so do not expose the default mode as a public website or use it to transfer sensitive data.

For safer remote use, pass `--ssh`. SSH mode does not bind any server-side TCP port. It listens on a private Unix socket and prints an `ssh -L` command so only your local browser can reach the server through your SSH session.

## Features

- **Zero dependencies on the host** — one statically-linked binary.
- **Token auth** out of the box: random 16-byte hex token, or pass `--token` to override.
- **Smart defaults** — autodetects the LAN-side IPv4 address and picks a random port in `60000-62999` (or `5000-5999` on `midway3*` hostnames).
- **Single port or port range** via `--port 60123` or `--port 60000-60100`.
- **Multiple instances on the same host** — auth cookies are scoped per server token, so opening a second server does not invalidate the first.
- **Optional SSH-only mode** via `--ssh` — binds no server TCP port; browse through an SSH local forward to a private Unix socket.
- **Automatic timeout exit** after `--duration 7d` by default; accepts `d`, `h`, and `m` suffixes.
- **Custom page title** via `--title "Shared files"`; by default the listing title is the full served folder path, expanding `~` while preserving logical symlink names.
- **UCSC-style directory listing** — borderless table with Name / Last modified / Size, Apache-style size suffixes (`12.0K`, `2.0M`).
- **Clickable relative path breadcrumbs** — jump directly to any parent level without scrolling through the listing.
- **Sortable listing columns** — click `Name`, `Last modified`, or `Size` to toggle ascending/descending order.
- **Experimental: copy full server paths** from a right-aligned `Path` column, useful for pasting figure paths into LaTeX on the same server.
- **Current-folder uploads near the top of the listing** with per-file progress indicator and overwrite/skip prompts for filename conflicts; falls back to a plain multipart `<form>` if JavaScript is off.
- **Path-traversal protection** on both browse and upload.

## Install

Requires Go 1.26.2+.

Build from source:

```sh
git clone https://github.com/chenkenbio/open-server.git
cd open-server
make build
```

This writes `./open-server`. To install into your Go binary directory instead:

```sh
make install
```

## Usage

```sh
open-server                            # serve current directory
open-server /path/to/dir               # serve a specific directory
open-server /path/to/file.txt          # serve one file from its parent directory
open-server --port 60123               # bind a single port (fails if taken)
open-server --port 60000-60100         # bind a random port in the inclusive range
open-server --duration 12h             # exit automatically after 12 hours
open-server --token mysecret123        # use a custom token (>=8 chars)
open-server --title "Shared files"     # set the browser/listing title
open-server --ssh /path/to/dir         # serve through SSH only; no server TCP port
open-server -a 10.0.0.5 -p 7000        # custom address + single port
```

In default TCP mode, startup prints a secure link like:

```
File server ready
Open this secure link in your browser:

┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃ http://10.0.0.5:60427/?token=<32-char-hex-token>               ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
```

Visiting the link drops a token-scoped auth cookie (`open_server_token_<id>`), so subsequent navigation within the directory tree no longer needs the `?token=` parameter in the URL bar and multiple servers on the same host can stay logged in independently. Stop with `Ctrl+C`.

In SSH mode, startup prints a tunnel command and a localhost-only browser URL:

```text
File server ready
Server is listening on a private Unix socket.
Run this SSH tunnel from your local device:

  ssh -N -o ExitOnForwardFailure=yes -L 127.0.0.1:60427:/tmp/open-server-1000/run-abc123/server.sock user@server

Then open this local link in your browser:

┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃ http://127.0.0.1:60427/?token=<32-char-hex-token>              ┃
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
```

The socket is created under `$TMPDIR/open-server-[uid]/...` when `$TMPDIR` is set, otherwise under `/tmp/open-server-[uid]/...`. The socket directory is private (`0700`) and is removed on normal shutdown. A small watcher process also removes it if the main server process exits unexpectedly; stale socket directories are cleaned on the next launch.

In SSH mode, `http://server_ip:port/?token=...` cannot work because the server does not listen on a TCP port. Other regular users on the same machine cannot connect to the Unix socket unless they can access your private temp directory. Root/admin users, or another process running as your same Unix account, can still bypass application-level protections.

If `--title` is omitted, the browser title and listing header default to the full served folder path. `~` is expanded to your home directory. For `open-server .`, a valid logical `$PWD` is used so running from a symlinked directory displays that symlink path instead of the resolved target path.

Directory pages show a relative breadcrumb path below the title. Each path level is clickable and preserves the active token and sort settings, so you can jump back to any parent directory directly. The drag-and-drop upload frame is placed below this path and above the listing headers for easier access in large directories. Uploads are saved into the directory currently being viewed.

If a JavaScript upload would replace an existing filename, `open-server` asks whether to overwrite or skip that file. The dialog includes an **Apply this choice to all remaining conflicts** checkbox for multi-file uploads. Plain multipart form uploads do not overwrite existing files automatically; conflicting filenames return `409 Conflict`.

Each listed file and directory has an experimental right-aligned `Path` column with a `Copy path` button that copies the full server filesystem path. This is useful for LaTeX work on the same server: browse to a figure, copy its path, and paste it directly into your `.tex` source.

Directory listings sort by name by default. Click the `Name`, `Last modified`, or `Size` header to change sort order; clicking the active header toggles ascending/descending order. Directory entries stay grouped before files.

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-a`, `--address` | autodetected LAN IPv4 (fallback `127.0.0.1`) | IP address to bind |
| `--duration` | `7d` | server lifetime before automatic exit; accepts `d`, `h`, or `m` suffix |
| `--local-port` | `60000-62999` (`5000-5999` on `midway3*`) | local browser port or inclusive range printed by `--ssh` |
| `-p`, `--port` | `60000-62999` (`5000-5999` on `midway3*`) | single port or inclusive range |
| `--socket` | private generated path | Unix socket path for `--ssh`; parent directory must be private |
| `--ssh` | `false` | listen on a private Unix socket and print an SSH tunnel command |
| `--ssh-host` | `$USER@hostname` | SSH target printed by `--ssh` |
| `-t`, `--title` | full served folder path | browser/listing page title |
| `--token` | random 16-byte hex | access token (≥8 characters) |

## Layout

```
.
├── main.go                # entrypoint + flag parsing
├── server.go              # HTTP handlers, listener, middleware, helpers
├── server_test.go         # helper tests
├── templates.go           # HTML templates (listing + 403 page)
├── go.mod / go.sum
├── Makefile               # build / install / vet / test / tidy / clean
├── .github/workflows/     # CI (go vet + go build)
├── .gitignore
├── LICENSE                # GPLv3
└── README.md
```

## Acknowledgements

This server is a fork of the [`hey open`](https://github.com/y9c/hey) subcommand by [Chang Ye (y9c)](https://github.com/y9c). The upstream code provides the token-auth middleware, panic-recovery middleware, path-traversal protection, and the upload handler. This fork strips the QR-code printout and the cobra dependency, defaults the served path to the current working directory, replaces the elaborate HTML with a UCSC-style listing, adds drag-and-drop with per-file progress, and exposes address / port / duration / token / title flags.

## License

[GPLv3](LICENSE) — matching the GPL license of the upstream project.
