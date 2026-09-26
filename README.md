<p align="center">
  <img src="docs/images/hero.png" alt="bdc — Baidu Netdisk CLI for AI agents" width="860">
</p>

<p align="center">
  <b>Let your AI agent run your Baidu Netdisk.</b><br>
  A fast, scriptable command line for 百度网盘. Claude Code, Codex and any tool-using agent can find, move, transfer and share your files on their own.
</p>

<p align="center">
  <a href="https://github.com/ac1982/bdc/releases/latest"><img src="https://img.shields.io/github/v/release/ac1982/bdc?color=3b82f6" alt="Release"></a>
  <a href="https://github.com/ac1982/bdc/actions/workflows/ci.yml"><img src="https://github.com/ac1982/bdc/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/macOS%20·%20Linux%20·%20Windows-555" alt="macOS, Linux, Windows">
  <a href="LICENSE"><img src="https://img.shields.io/github/license/ac1982/bdc?color=555" alt="MIT license"></a>
</p>

<p align="center"><b>English</b> · <a href="README.zh-CN.md">简体中文</a></p>

---

## See it work

> **You:** Back up `~/Photos/2026` to `/Backup` on my netdisk, then send me a share link that expires in a week.

```console
$ bdc upload --json ~/Photos/2026 /Backup
{"ok": true, "summary": {"rapid": 37, "uploaded": 212}, "files": [ … ]}

$ bdc share create --json --days 7 /Backup/2026
{"ok": true, "link": "https://pan.baidu.com/s/1AbCdEf", "pwd": "k7x2", "url": "https://pan.baidu.com/s/1AbCdEf?pwd=k7x2", … }
```

> **Agent:** Done. 249 photos are in `/Backup/2026`; 37 of them were already stored on Baidu and went up instantly. Here's the link: https://pan.baidu.com/s/1AbCdEf?pwd=k7x2 (valid for 7 days).

The agent needs no screen scraping and no guessing. Every command returns one JSON document and an exit code that says what happened.

## Why it works for agents

<table>
<tr>
<td width="50%" valign="top">

**Speaks JSON, never hangs**<br>
Every command returns one JSON document on stdout, with typed errors and meaningful exit codes. It never stops at a prompt when no one is at the keyboard.

</td>
<td width="50%" valign="top">

**Big transfers, done right**<br>
Folders upload and download in parallel. Files Baidu already has upload instantly (秒传). Run an interrupted transfer again and it picks up where it stopped, skipping what is already done.

</td>
</tr>
<tr>
<td valign="top">

**Shares, both ways**<br>
Create links with an extraction code and an expiry, or save someone else's share straight into your drive.

</td>
<td valign="top">

**You step in only when Baidu insists**<br>
If Baidu asks for a security check, bdc asks where to send the code (SMS or email), takes it in the terminal, and carries on.

</td>
</tr>
</table>

## Install

| | |
|---|---|
| **macOS** | Download the `.pkg` for your Mac from [Releases](https://github.com/ac1982/bdc/releases/latest). It is signed and notarized by Apple. |
| **Linux · Windows** | Download the archive from [Releases](https://github.com/ac1982/bdc/releases/latest) and put `bdc` on your `PATH`. |
| **Go** | `go install github.com/ac1982/bdc@latest` |

Then log in once, with your browser's session:

```sh
bdc login --from-chrome        # or --from-edge, or paste cookies at the prompt of `bdc login`
```

## Hand it to your agent

Add one line to your `AGENTS.md` or `CLAUDE.md`:

```text
For Baidu Netdisk, use `bdc` with `--json`. Read https://github.com/ac1982/bdc/blob/main/llms.txt first.
```

[`llms.txt`](llms.txt) is a short manual written for models: the output format, what each exit code means, and the rules for every command.

## What it can do

```text
Browse     ls · tree · meta · search · cd · pwd
Organise   mkdir · cp · mv · rm · recycle list|restore|delete
Transfer   download · upload · offline add|list|cancel|delete
Share      share create|list|cancel|save
Account    login · logout · who · users · su · quota
More       config · update · an interactive shell (run bdc with no arguments)
```

`bdc <command> --help` explains every flag. Human output is in Chinese; the JSON is language-neutral.

<details>
<summary><b>Exit codes</b></summary>

| code | `error.kind` | meaning |
|---|---|---|
| 0 | | done |
| 1 | `failed` | server, network or transfer failure: retry later, transfers resume |
| 2 | `input` | not found, already exists, no match, wrong extraction code |
| 3 | `dependency` | a permission problem: browser cookies unreadable, or `update` can't write its directory (use `sudo`) |
| 4 | `auth` | not logged in; with `code` 132, Baidu wants a security check (run the command in a terminal) |
| 64 | `usage` | bad command line, or `-y` needed without a terminal (`logout`, `recycle delete`, `update`) |
| 130 | `cancelled` | interrupted |

</details>

<details>
<summary><b>Configuration</b></summary>

Settings and accounts live in `config.json` in the user config directory, or in `$BDC_CONFIG_DIR`:
- macOS: `~/Library/Application Support/bdc`
- Linux: `~/.config/bdc`
- Windows: `%AppData%\bdc`

`bdc config set` changes the save directory, connections, parallel files, speed limits and proxy. bdc never prints your cookies.

</details>

<details>
<summary><b>Development</b></summary>

```sh
go vet ./... && go test -race ./...    # unit tests, an in-memory fake Baidu, offline end-to-end replay
```

[ARCHITECTURE.md](docs/ARCHITECTURE.md) maps the code. [baidu-api.md](docs/baidu-api.md) documents the web-client protocol bdc speaks.

</details>
