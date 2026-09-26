# bdc — Baidu Netdisk CLI, built for AI agents

<p><bdi><strong>English</strong></bdi> · <a href="README.zh-CN.md">简体中文</a></p>

<p align="center"><img src="docs/images/hero.png" alt="bdc: an AI agent driving Baidu Netdisk from the terminal" width="820"></p>

**bdc** is short for **B**ai**d**u Netdisk **C**LI: a single-binary command-line client for [Baidu Netdisk (百度网盘)](https://pan.baidu.com) on macOS, Linux and Windows.

People can use it too, but it is designed so that an AI agent (Claude Code, Codex, or any tool-using LLM) can work with your netdisk on its own. It can:
- find files and read their metadata;
- download and upload whole folders;
- organise, share and save shared links.

It never has to scrape a web page, guess at text output, or get stuck on a prompt.

```console
$ bdc ls --json /Photos
{
  "command": "ls",
  "files": [
    {"ctime": "2026-09-25T22:12:40+08:00", "fsId": 1043012566118829, "isDir": false,
     "md5": "b1946ac92492d2347c6235b4d2611184", "mtime": "2026-09-25T22:12:40+08:00",
     "name": "IMG_0142.jpg", "path": "/Photos/IMG_0142.jpg", "size": 3145728}
  ],
  "ok": true
}

$ bdc download --json -o ~/Downloads "/Photos/*.jpg"
{
  "command": "download",
  "files": [
    {"file": "/Users/me/Downloads/IMG_0142.jpg", "path": "/Photos/IMG_0142.jpg",
     "size": 3145728, "status": "downloaded"}
  ],
  "ok": true,
  "summary": {"downloaded": 1}
}
```

## Why it works well for agents

| What an agent needs | What bdc does |
|---|---|
| Output it can parse | `--json` puts **exactly one JSON document** on stdout (keys sorted, sizes in bytes, RFC 3339 times, exact integer ids). Progress and logs go to stderr. |
| To know what went wrong | Every failure has `error: {kind, message, exitCode, code?}` and a [meaningful exit code](#exit-codes): *input* (fix the arguments), *auth* (log in), *failed* (retry later), and so on. |
| Never to hang | Nothing waits for input without a terminal. The three commands that ask for confirmation (`logout`, `recycle delete`, `update`) need `-y` there, and a missing `-y` is an immediate usage error, not a prompt. Other changes, such as `rm` (which moves to the recycle bin), run without asking. |
| To keep partial progress | Whatever finished before a failure is still in the document: every file of a transfer is listed with its own `status`. |
| Retries that are safe | `mkdir` of an existing directory succeeds; files already there are `skipped`. Interrupted downloads and uploads resume where they stopped. |
| No path guessing | Downloads report each file's absolute local path (`files[].file`). Wildcards are expanded by bdc against what really exists, and a pattern that matches nothing is an *input* error. |
| A person in the loop only when needed | If Baidu's risk control asks for a security check (error 132), an agent without a terminal gets `auth`/`132`. You then run the same command in a terminal, and bdc asks where to send the SMS or email code, takes it, and carries on. |
| Fewer surprises from Baidu | bdc speaks the API of Baidu's **web client**, the way a browser does, as observed in a real browser. File operations are confirmed as background tasks, so a copy is only reported when it happened. |

**Give your agent the manual:** [llms.txt](llms.txt) is a compact guide written for LLMs: quick start, output format, exit codes and the rules for each command. For example, add a line to your `AGENTS.md` or `CLAUDE.md`:

```text
To use Baidu Netdisk, run `bdc` with `--json`; read https://github.com/ac1982/bdc/blob/main/llms.txt first.
```

## Features

| Area | Commands | How |
|---|---|---|
| Account | `login`, `logout`, `who`, `users`, `su`, `quota` | Log in with your browser's cookies: read from Chrome/Edge (`--from-chrome`), or pasted with `--cookies`. Several accounts can be stored; switch with `su`. |
| Browse | `ls`, `tree`, `meta`, `search`, `cd`, `pwd` | `ls -l` adds `fsId` and md5. `md5` appears only when it is the real content md5. `search -r` searches a whole subtree. Relative paths use the directory set with `cd`. |
| Organise | `mkdir`, `cp`, `mv`, `rm` | Batches with wildcards. Missing parent directories are created. `rm` moves to the recycle bin, and each item reports whether it was done. |
| Recycle bin | `recycle list`, `recycle restore`, `recycle delete` | Restore by `fsId`. Permanent deletion needs `-y`. |
| Download | `download [-o dir] [-p conns] [--overwrite]` | Folders download recursively. Each file uses parallel ranged connections. Resumable through `.bdc-part` files, which are checked against the remote version. Existing files are skipped. |
| Upload | `upload [--policy skip\|overwrite\|rsync] <local…> <dir>` | Folders upload recursively in parallel 4 MiB blocks. Content Baidu already stores is uploaded instantly (秒传, `status: "rapid"`). Resumable. |
| Share | `share create`, `share list`, `share cancel`, `share save` | Creates links with an extraction code and an expiry. `share save "<link>?pwd=…" --to /dir` saves someone else's share into your netdisk. |
| Offline download | `offline add`, `offline list`, `offline cancel`, `offline delete` | Baidu fetches a URL or magnet link into your netdisk on its servers. |
| Settings | `config`, `config set`, `config reset` | Save directory, connections, parallel files, speed limits, proxy (http/https/socks5). |
| Interactive shell | `bdc` with no arguments, in a terminal | History, tab completion of commands and netdisk paths, and in-terminal answers to Baidu's security check. |
| Self-update | `update [--check]` | Installs the latest GitHub release for your platform. |

Every command documents its flags with `bdc <command> --help` (also as JSON with `--json`).

## Install

```sh
go install github.com/ac1982/bdc@latest    # Go 1.26+
```

Or download a build from [Releases](../../releases). On macOS, the `.pkg` installer is signed with a Developer ID and notarized by Apple, and installs `bdc` into `/usr/local/bin`. The archives (`.tar.gz`, or `.zip` for Windows) hold the same binary: put it on your `PATH`. `bdc update` installs newer releases. If bdc is in a directory you can't write to, as after the `.pkg` installer, run it with `sudo`.

## Log in

```sh
bdc login --from-chrome                 # or --from-edge; --profile "Profile 1" for another profile
bdc login --cookies "BDUSS=…; STOKEN=…"  # copied from pan.baidu.com in your browser's developer tools
bdc who --json
```

On macOS, `--from-chrome` needs Full Disk Access for the terminal. `STOKEN` is needed for saving others' shares. bdc never prints your cookies, and its interactive shell never keeps a `login` line in its history. Your own shell (zsh, bash) does record `bdc login --cookies …`, so prefer `--from-chrome`, or run `bdc login` in a terminal and paste the cookies at its prompt.

## Use it yourself

```sh
bdc ls /                                        # human output is in Chinese; the JSON is language-neutral
bdc tree --depth 2 /Documents
bdc search -r --path / report
bdc mkdir a/b && bdc cp x.txt a && bdc mv x.txt y.txt && bdc rm "old-*"
bdc upload ~/Movies/trip.mp4 ~/Photos /Backup
bdc download -o ~/Downloads /Backup/Photos
bdc share create --days 7 /Videos/trip.mp4       # prints the link and extraction code
bdc share save "https://pan.baidu.com/s/1xxxx?pwd=abcd" --to /Saved
bdc offline add --to /Downloads "magnet:?xt=…"
bdc config set --connections 16 --download-limit 10MB
```

## Exit codes

| code | `error.kind` | meaning | what an agent should do |
|---|---|---|---|
| 0 | | done | read the fields |
| 1 | `failed` | server, network or transfer failure | retry later; transfers resume |
| 2 | `input` | not found, already exists, no wildcard match, wrong extraction code | fix the input; `bdc ls --json` to look around |
| 3 | `dependency` | a permission problem: browser cookies unreadable, or `update` cannot write to bdc's directory | log in with `--cookies`; for `update`, ask the user to run `sudo bdc update` |
| 4 | `auth` | not logged in or login expired; with `code` 132, a security check | `bdc login`; for 132, ask the user to run the command in a terminal |
| 64 | `usage` | bad command line, or a confirmation needed without a terminal | see `--help`; add `-y` |
| 130 | `cancelled` | interrupted | rerun |

## Configuration

Settings and accounts live in `config.json` in the user config directory, or in `$BDC_CONFIG_DIR`:
- macOS: `~/Library/Application Support/bdc`
- Linux: `~/.config/bdc`
- Windows: `%AppData%\bdc`

`bdc config --json` shows the settings and the path; it never shows login cookies.

## Development

```sh
go vet ./... && go test -race ./...            # unit tests, an in-memory fake Baidu, offline end-to-end replay
go test ./e2e -record                          # re-record end-to-end scenarios with a real account (only touches /bdc-test)
BDC_LIVE_COOKIES="…" go test ./internal/...    # live tests of the API client and transfers
```

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) maps the code.
- [docs/baidu-api.md](docs/baidu-api.md) describes the web client's protocol as bdc speaks it: files, upload and 秒传, download signatures, shares, background tasks, and the SMS security check.

## License

[MIT](LICENSE)
