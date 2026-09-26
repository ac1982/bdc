<p align="center">
  <img src="docs/images/hero.png" alt="bdc — 为 AI agent 打造的百度网盘命令行" width="860">
</p>

<p align="center">
  <b>让 AI agent 替你打理百度网盘.</b><br>
  快速、可编程的百度网盘命令行. Claude Code、Codex 以及任何会调用工具的 agent, 都能自己查找、整理、传输和分享你的文件.
</p>

<p align="center">
  <a href="https://github.com/ac1982/bdc/releases/latest"><img src="https://img.shields.io/github/v/release/ac1982/bdc?color=3b82f6" alt="Release"></a>
  <a href="https://github.com/ac1982/bdc/actions/workflows/ci.yml"><img src="https://github.com/ac1982/bdc/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/macOS%20·%20Linux%20·%20Windows-555" alt="macOS, Linux, Windows">
  <a href="LICENSE"><img src="https://img.shields.io/github/license/ac1982/bdc?color=555" alt="MIT license"></a>
</p>

<p align="center"><a href="README.md">English</a> · <b>简体中文</b></p>

---

## 看它怎么做

> **你:** 把 `~/Photos/2026` 备份到网盘的 `/Backup`, 然后给我一个一周内有效的分享链接.

```console
$ bdc upload --json ~/Photos/2026 /Backup
{"ok": true, "summary": {"rapid": 37, "uploaded": 212}, "files": [ … ]}

$ bdc share create --json --days 7 /Backup/2026
{"ok": true, "link": "https://pan.baidu.com/s/1AbCdEf", "pwd": "k7x2", "url": "https://pan.baidu.com/s/1AbCdEf?pwd=k7x2", … }
```

> **Agent:** 完成了. 249 张照片已在 `/Backup/2026`, 其中 37 张百度已有, 瞬间秒传. 分享链接: https://pan.baidu.com/s/1AbCdEf?pwd=k7x2 (7 天内有效).

agent 不用解析网页, 也不用猜. 每条命令都返回一份 JSON 和一个说明结果的退出码.

## 为什么适合 agent

<table>
<tr>
<td width="50%" valign="top">

**只说 JSON, 从不卡住**<br>
每条命令在 stdout 输出一份 JSON 文档, 错误有类型, 退出码有含义. 没人在键盘前时, 绝不会停下来等输入.

</td>
<td width="50%" valign="top">

**大文件传输, 稳稳当当**<br>
目录并行上传、下载; 百度已有的文件秒传. 传输中断后再运行一次, 会从断点继续, 已完成的自动跳过.

</td>
</tr>
<tr>
<td valign="top">

**分享, 双向都行**<br>
创建带提取码和有效期的链接, 或把别人的分享直接转存到自己的网盘.

</td>
<td valign="top">

**只在百度坚持时才需要你**<br>
百度要求安全验证时, bdc 会问验证码发到哪 (短信或邮箱), 你在终端里输入后它继续执行.

</td>
</tr>
</table>

## 安装

| | |
|---|---|
| **macOS** | 从 [Releases](https://github.com/ac1982/bdc/releases/latest) 下载对应芯片的 `.pkg`, 已签名并经过 Apple 公证. |
| **Linux · Windows** | 从 [Releases](https://github.com/ac1982/bdc/releases/latest) 下载压缩包, 把 `bdc` 放进 `PATH`. |
| **Go** | `go install github.com/ac1982/bdc@latest` |

然后用浏览器的登录状态登录一次:

```sh
bdc login --from-chrome        # 或 --from-edge; 也可以运行 bdc login 后在提示处粘贴 Cookie
```

## 交给你的 agent

在 `AGENTS.md` 或 `CLAUDE.md` 里加一行:

```text
操作百度网盘时用 `bdc` 并加 `--json`; 先读 https://github.com/ac1982/bdc/blob/main/llms.txt
```

[`llms.txt`](llms.txt) 是写给大模型的简明手册, 包括输出格式、每个退出码的含义和每个命令的规则.

## 能做什么

```text
浏览     ls · tree · meta · search · cd · pwd
整理     mkdir · cp · mv · rm · recycle list|restore|delete
传输     download · upload · offline add|list|cancel|delete
分享     share create|list|cancel|save
帐号     login · logout · who · users · su · quota
其他     config · update · 交互模式 (不带参数运行 bdc)
```

`bdc <命令> --help` 说明每个参数. 给人看的输出是中文, JSON 与语言无关.

<details>
<summary><b>退出码</b></summary>

| 退出码 | `error.kind` | 含义 |
|---|---|---|
| 0 | | 完成 |
| 1 | `failed` | 服务器、网络或传输失败: 稍后重试, 传输会续传 |
| 2 | `input` | 不存在、已存在、无匹配、提取码错误 |
| 3 | `dependency` | 权限问题: 读不到浏览器 Cookie, 或 `update` 不能写入所在目录 (用 `sudo`) |
| 4 | `auth` | 未登录; `code` 为 132 时是百度要求安全验证 (在终端里运行该命令) |
| 64 | `usage` | 命令行有误, 或没有终端时需要 `-y` (`logout`, `recycle delete`, `update`) |
| 130 | `cancelled` | 已取消 |

</details>

<details>
<summary><b>配置</b></summary>

设置和帐号保存在用户配置目录的 `config.json` 中, 也可以用 `$BDC_CONFIG_DIR` 指定:
- macOS: `~/Library/Application Support/bdc`
- Linux: `~/.config/bdc`
- Windows: `%AppData%\bdc`

`bdc config set` 可修改保存目录、连接数、并行文件数、限速和代理. bdc 从不打印你的 Cookie.

</details>

<details>
<summary><b>开发</b></summary>

```sh
go vet ./... && go test -race ./...    # 单元测试, 内存中的模拟百度, 离线端到端回放
```

[ARCHITECTURE.md](docs/ARCHITECTURE.md) 是代码地图, [baidu-api.md](docs/baidu-api.md) 记录 bdc 所说的网页版协议.

</details>
