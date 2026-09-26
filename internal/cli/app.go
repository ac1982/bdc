// Package cli is bnd's command line: commands, output, exit codes and the
// interactive shell. It is the only package that prints.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"slices"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/mattn/go-isatty"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
	"github.com/ac1982/baidunetdisk-cli/internal/config"
)

// Version is set at build time with -ldflags "-X ...cli.Version=v1.2.3".
var Version = "dev"

// App is the state one run of bnd shares between commands: in the
// interactive shell it lives across many commands.
type App struct {
	ctx    context.Context
	cfg    *config.Config
	json   bool
	stdout io.Writer
	stderr io.Writer
	stdin  *os.File

	client *baidu.Client
}

const description = `百度网盘命令行客户端, 为人和 AI agent 而生.

  网盘路径是绝对路径, 或相对于 cd 设定的工作目录; 通配符 * ? [ ] 由 bnd 展开.
  --json: stdout 只有一份 JSON 文档 (含 ok, command, 失败时 error), 进度和日志在 stderr.
  退出码: 0 成功, 1 服务器/网络/传输失败, 2 输入有误 (不存在, 已存在, 无匹配, 提取码错误),
          3 缺少依赖或权限, 4 未登录或登录过期, 64 命令行有误或需要终端, 130 已取消.
  没有终端时不会等待输入: 需要确认的命令要加 -y.`

// runner is implemented by every command.
type runner interface {
	Run(app *App) (Result, error)
}

// Main runs bnd with the arguments after the program name and returns the exit code.
func Main(args []string) int {
	defer func() { stopCassette() }() // set once the client is built
	app := &App{ctx: context.Background(), json: hasJSONFlag(args), stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin}

	cfg, err := config.Load()
	if err != nil {
		return app.report("", nil, err)
	}
	app.cfg = cfg
	if len(args) == 0 {
		if !app.interactive() {
			return app.report("", nil, usagef("没有指定命令; 交互模式需要终端, 用 bnd --help 查看命令"))
		}
		return app.shell()
	}
	return app.exec(args)
}

// exec parses and runs one command line. Ctrl-C cancels the command.
func (a *App) exec(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.ctx = ctx
	a.json = hasJSONFlag(args)
	var root root
	var help bytes.Buffer
	parser, err := kong.New(&root,
		kong.Name("bnd"),
		kong.Description(description),
		kong.NoDefaultHelp(),
		kong.Writers(&help, a.stderr),
		kong.Exit(func(int) {}),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Vars{"version": Version},
	)
	if err != nil {
		panic(err) // the command grammar is static; this is a programming error
	}

	kctx, err := parser.Parse(args)
	var perr *kong.ParseError
	if errors.As(err, &perr) {
		kctx = perr.Context
	}
	command := commandName(kctx)
	switch {
	case errors.Is(err, errShowHelp):
		if perr := kctx.PrintUsage(false); perr != nil {
			return a.report(command, nil, perr)
		}
		return a.report("help", helpResult{help.String()}, nil)
	case errors.Is(err, errShowVersion):
		return a.report("version", versionResult{Version}, nil)
	case err != nil:
		return a.report(command, nil, usagef("%v (用 bnd %s --help 查看用法)", err, strings.TrimSpace(command)))
	}

	cmd, ok := kctx.Selected().Target.Addr().Interface().(runner)
	if !ok {
		return a.report(command, nil, usagef("%s 需要子命令, 用 bnd %s --help 查看", command, command))
	}
	res, err := cmd.Run(a)
	return a.report(command, res, err)
}

// report writes the outcome of a command and returns its exit code.
func (a *App) report(command string, res Result, err error) int {
	if a.json {
		if werr := writeJSON(a.stdout, command, res, err); werr != nil {
			fmt.Fprintln(a.stderr, "错误:", werr)
			return Failed.ExitCode()
		}
	} else {
		if res != nil {
			res.Human(a.stdout)
		}
		if err != nil {
			fmt.Fprintln(a.stderr, "错误:", err)
		}
	}
	if err != nil {
		return classify(err).ExitCode()
	}
	return 0
}

// confirm asks a yes/no question unless yes is already given. Without a
// terminal it refuses instead of waiting for an answer that cannot come.
func (a *App) confirm(yes bool, question string) error {
	if yes {
		return nil
	}
	if !a.interactive() {
		return usagef("需要确认, 没有终端时请加 -y")
	}
	fmt.Fprint(a.stderr, question+" [y/N] ")
	answer, _ := bufio.NewReader(a.stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return withKind(Cancelled, errors.New("已取消"))
}

// interactive reports whether a person is at the terminal.
func (a *App) interactive() bool {
	return isatty.IsTerminal(a.stdin.Fd()) || isatty.IsCygwinTerminal(a.stdin.Fd())
}

// account is the logged-in account, or errNotLoggedIn.
func (a *App) account() (*config.Account, error) {
	acc := a.cfg.Current()
	if acc == nil {
		return nil, errNotLoggedIn
	}
	return acc, nil
}

// baidu returns the API client of the current account.
func (a *App) baidu() (*baidu.Client, error) {
	acc, err := a.account()
	if err != nil {
		return nil, err
	}
	if a.client == nil || a.client.UID != acc.UID {
		a.client, err = newClient(a.cfg.Settings, acc.Cookies, acc.UID)
		if err != nil {
			return nil, err
		}
	}
	return a.client, nil
}

// abs resolves a netdisk path against the working directory.
func (a *App) abs(p string) string {
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	wd := "/"
	if acc := a.cfg.Current(); acc != nil && acc.Workdir != "" {
		wd = acc.Workdir
	}
	return path.Join(wd, p)
}

// hasJSONFlag finds --json among the flags; it may come before or after the command.
func hasJSONFlag(args []string) bool {
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[:i]
	}
	return slices.Contains(args, "--json")
}

// commandName is the space-separated command path, e.g. "share create".
func commandName(kctx *kong.Context) string {
	if kctx == nil || kctx.Selected() == nil {
		return ""
	}
	var names []string
	for n := kctx.Selected(); n != nil && n.Type == kong.CommandNode; n = n.Parent {
		names = append([]string{n.Name}, names...)
	}
	return strings.Join(names, " ")
}
