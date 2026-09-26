# bnd

<p><a href="README.md">English</a> · <bdi><strong>简体中文</strong></bdi></p>

百度网盘的命令行客户端, 为人和 AI agent 而生: macOS, Linux, Windows 上各一个二进制; `--json` 时每条命令输出一份 JSON 文档; 退出码有意义; 没有终端时从不等待输入.

```
$ bnd ls /Photos
-       2026-09-25 22:12:39  2026-09/
3.00MB  2026-09-25 22:12:40  IMG_0142.jpg
共 1 个文件 (3.00MB), 1 个目录

$ bnd download -o ~/Downloads "/Photos/*.jpg"
已下载 /Photos/IMG_0142.jpg → /Users/me/Downloads/IMG_0142.jpg
下载结束: 已下载 1; 传输 3.00MB
```

## 安装

从 [Releases](../../releases) 下载对应系统的压缩包, 把 `bnd` 放到 `PATH` 中; 或者自己编译:

```sh
go install github.com/ac1982/baidunetdisk-cli@latest   # Go 1.26+; 二进制名为 baidunetdisk-cli
go build -o bnd .                                       # 在克隆的仓库中
```

之后用 `bnd update` 安装新版本.

## 登录

```sh
bnd login --from-chrome              # 或 --from-edge; 其他用户配置用 --profile "Profile 1"
bnd login --cookies "BDUSS=…; STOKEN=…"
bnd who
```

macOS 上 `--from-chrome` 需要在 系统设置 → 隐私与安全性 → 完全磁盘访问权限 中允许终端程序. 也可以在浏览器开发者工具中复制 pan.baidu.com 的 Cookie; 转存别人的分享需要 `STOKEN`.

## 使用

```sh
bnd ls /                                   # 列出目录; -l 显示 fs_id 和 md5; --sort size|time
bnd tree --depth 2 /文档
bnd search -r --path / 报告                 # 按文件名搜索
bnd cd /视频 && bnd pwd                     # 相对路径基于工作目录
bnd mkdir a/b && bnd cp x.txt a && bnd mv x.txt y.txt && bnd rm "旧-*"
bnd upload ~/Movies/trip.mp4 ~/Photos /备份            # 目录递归上传
bnd download -o ~/Downloads /备份/Photos               # 目录递归下载
bnd share create --days 7 /视频/trip.mp4               # 显示链接和提取码
bnd share save "https://pan.baidu.com/s/1xxxx?pwd=abcd" --to /转存
bnd recycle list && bnd recycle restore <fs_id>
bnd offline add --to /下载 "magnet:?xt=…"
bnd config set --connections 16 --download-limit 10MB
```

在终端中不带参数运行 `bnd` 进入交互模式, 有历史记录, Tab 补全网盘路径. `bnd <命令> --help` 说明每个选项.

传输可断点续传: 中断的下载或上传, 再次运行同一命令时从中断处继续. 已存在的文件默认跳过 (下载用 `--overwrite`, 上传用 `--policy overwrite|rsync` 改变). 百度已有的内容秒传.

## 给 AI agent 和脚本

完整说明见 [llms.txt](llms.txt). 简要:

- `--json` (放在命令前后都可以) 使 stdout 只输出一份 JSON 文档, 含 `ok`, `command`, 失败时含 `error: {kind, message, exitCode, code?}`. 进度和日志在 stderr. 失败之前完成的部分仍在文档中.
- 没有终端时不会等待输入: `logout`, `recycle delete|clear`, `update` 需要 `-y`.
- 退出码:

| 退出码 | `error.kind` | 含义 |
|---|---|---|
| 0 | | 成功 |
| 1 | `failed` | 服务器, 网络或传输失败 |
| 2 | `input` | 不存在, 已存在, 通配符无匹配, 提取码错误 |
| 3 | `dependency` | 无法读取浏览器的 Cookie (权限) |
| 4 | `auth` | 未登录或登录过期 |
| 64 | `usage` | 命令行有误, 或没有终端时需要确认 |
| 130 | `cancelled` | 已中断 |

## 配置

设置和帐号保存在用户配置目录的 `config.json` 中 (macOS 为 `~/Library/Application Support/bnd`, Linux 为 `~/.config/bnd`, Windows 为 `%AppData%\bnd`), 或 `$BND_CONFIG_DIR`. `bnd config` 显示设置, 从不显示登录的 Cookie.

## 开发

```sh
go vet ./... && go test -race ./...    # 单元测试和离线的端到端回放
go test ./e2e -record                  # 用真实帐号重新录制端到端场景 (只动 /bnd-test)
BND_LIVE_COOKIES="…" go test ./internal/...   # API 客户端和传输的真实帐号测试
```

[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) 是代码的地图; [docs/baidu-api.md](docs/baidu-api.md) 记录百度的接口协议.

## 许可

[MIT](LICENSE)
