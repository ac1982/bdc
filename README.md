# bnd

<p><bdi><strong>English</strong></bdi> · <a href="README.zh-CN.md">简体中文</a></p>

A command-line client for Baidu Netdisk (百度网盘), built for people and AI agents alike: one binary for macOS, Linux and Windows, one JSON document per command with `--json`, meaningful exit codes, and no prompts without a terminal.

```
$ bnd ls /Photos
-       2026-09-25 22:12:39  2026-09/
3.00MB  2026-09-25 22:12:40  IMG_0142.jpg
共 1 个文件 (3.00MB), 1 个目录

$ bnd download -o ~/Downloads "/Photos/*.jpg"
已下载 /Photos/IMG_0142.jpg → /Users/me/Downloads/IMG_0142.jpg
下载结束: 已下载 1; 传输 3.00MB
```

Human output is in Chinese; the JSON is language-neutral.

## Install

Download the archive for your system from [Releases](../../releases) and put `bnd` on your `PATH`, or build it:

```sh
go install github.com/ac1982/baidunetdisk-cli@latest   # Go 1.26+; the binary is named baidunetdisk-cli
go build -o bnd .                                       # from a clone
```

`bnd update` installs newer releases.

## Log in

```sh
bnd login --from-chrome              # or --from-edge; --profile "Profile 1" for another profile
bnd login --cookies "BDUSS=…; STOKEN=…"
bnd who
```

`--from-chrome` needs Full Disk Access for the terminal on macOS. Cookies can also be copied from the browser's developer tools on pan.baidu.com; `STOKEN` is needed for saving others' shares.

## Use

```sh
bnd ls /                                   # list; -l adds fs_id and md5; --sort size|time
bnd tree --depth 2 /Documents
bnd search -r --path / report              # by file name
bnd cd /Videos && bnd pwd                  # relative paths use the working directory
bnd mkdir a/b && bnd cp x.txt a && bnd mv x.txt y.txt && bnd rm "old-*"
bnd upload ~/Movies/trip.mp4 ~/Photos /Backup        # folders upload recursively
bnd download -o ~/Downloads /Backup/Photos            # folders download recursively
bnd share create --days 7 /Videos/trip.mp4            # prints the link and extraction code
bnd share save "https://pan.baidu.com/s/1xxxx?pwd=abcd" --to /Saved
bnd recycle list && bnd recycle restore <fs_id>
bnd offline add --to /Downloads "magnet:?xt=…"
bnd config set --connections 16 --download-limit 10MB
```

Run `bnd` without arguments in a terminal for an interactive shell with history and tab completion of netdisk paths. `bnd <command> --help` documents every flag.

Transfers are resumable: an interrupted download or upload continues where it stopped the next time you run the same command. Files already present are skipped (`download --overwrite`, `upload --policy overwrite|rsync` to change that). Content Baidu already stores is uploaded instantly (秒传).

## For AI agents and scripts

[llms.txt](llms.txt) is the full guide. In short:

- `--json` (before or after the command) puts exactly one JSON document on stdout, with `ok`, `command` and, on failure, `error: {kind, message, exitCode, code?}`. Progress and logs go to stderr. Whatever finished before a failure is still in the document.
- Nothing waits for input without a terminal: `logout`, `recycle delete|clear` and `update` need `-y`.
- Exit codes:

| code | `error.kind` | meaning |
|---|---|---|
| 0 | | done |
| 1 | `failed` | server, network or transfer failure |
| 2 | `input` | not found, already exists, no wildcard match, wrong extraction code |
| 3 | `dependency` | browser cookies unreadable (permissions) |
| 4 | `auth` | not logged in, or the login expired |
| 64 | `usage` | bad command line, or a confirmation needed without a terminal |
| 130 | `cancelled` | interrupted |

## Configuration

Settings and accounts live in `config.json` in the user config directory (`~/Library/Application Support/bnd` on macOS, `~/.config/bnd` on Linux, `%AppData%\bnd` on Windows), or in `$BND_CONFIG_DIR`. `bnd config` shows the settings; it never prints login cookies.

## Development

```sh
go vet ./... && go test -race ./...    # unit tests and offline end-to-end replay
go test ./e2e -record                  # re-record end-to-end scenarios with a real account (only touches /bnd-test)
BND_LIVE_COOKIES="…" go test ./internal/...   # live tests of the API client and transfers
```

[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) maps the code; [docs/baidu-api.md](docs/baidu-api.md) describes Baidu's protocol.

## License

[MIT](LICENSE)
