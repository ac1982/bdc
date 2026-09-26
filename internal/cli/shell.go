package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/kballard/go-shellquote"

	"github.com/ac1982/baidunetdisk-cli/internal/config"
)

// shell reads commands from the terminal until exit, quit or Ctrl-D.
func (a *App) shell() int {
	dir, _ := config.Dir()
	keys := a.keyboard() // shared with the questions commands ask
	cfg := readline.Config{
		Stdout:                 a.stderr,
		Stderr:                 a.stderr,
		HistoryFile:            filepath.Join(dir, "history"),
		DisableAutoSaveHistory: true, // saved below, but never a login: it holds the cookies
		AutoComplete:           &completer{app: a, commands: commandNames()},
		InterruptPrompt:        "^C",
		EOFPrompt:              "exit",
	}
	fmt.Fprintln(a.stderr, "bdc", Version, "交互模式. 输入 help 查看命令, exit 退出. Tab 补全命令和网盘路径.")
	for {
		// A fresh editor for each line: once its line is read it takes no
		// more input, which is left for the command's questions or the next line.
		lineCfg := cfg // readline changes its config, and each editor keeps it
		lineCfg.Stdin, lineCfg.Prompt = keys.editorInput(), a.prompt()
		rl, err := readline.NewEx(&lineCfg)
		if err != nil {
			return a.report("", nil, err)
		}
		line, err := rl.Readline()
		keys.endEdit()
		args, perr := shellquote.Split(line)
		if len(args) > 0 && args[0] == "help" {
			args = append(args[1:], "--help")
		}
		if err == nil && perr == nil && historic(args) {
			rl.SaveHistory(line)
		}
		rl.Close()
		switch {
		case errors.Is(err, readline.ErrInterrupt):
			continue
		case err != nil: // io.EOF: Ctrl-D
			return 0
		case perr != nil:
			fmt.Fprintln(a.stderr, "错误:", perr)
			continue
		case len(args) == 0:
			continue
		case args[0] == "exit" || args[0] == "quit":
			return 0
		}
		a.exec(args)
	}
}

func (a *App) prompt() string {
	if acc := a.cfg.Current(); acc != nil {
		return fmt.Sprintf("bdc:%s %s$ ", acc.Workdir, acc.Name)
	}
	return "bdc (未登录)$ "
}

// historic reports whether a command line may go into the history: a
// command that parses and runs (not help), and not a login, whose line
// holds the cookies.
func historic(args []string) bool {
	kctx, err := newParser(&root{}, io.Discard, io.Discard).Parse(args)
	return err == nil && commandName(kctx) != "login"
}

// commandNames lists the top-level commands of the grammar.
func commandNames() []string {
	var names []string
	for _, n := range newParser(&root{}, io.Discard, io.Discard).Model.Children {
		names = append(names, n.Name)
	}
	return append(names, "help", "exit")
}

// completer completes command names, then netdisk paths.
type completer struct {
	app      *App
	commands []string
}

func (c *completer) Do(line []rune, pos int) ([][]rune, int) {
	before := string(line[:pos])
	words := strings.Fields(before)
	if len(words) == 0 || strings.HasSuffix(before, " ") {
		words = append(words, "")
	}
	word := words[len(words)-1]
	var candidates []string
	if len(words) == 1 {
		candidates = c.commands
	} else if !strings.HasPrefix(word, "-") {
		candidates = c.paths(word)
	}
	var out [][]rune
	for _, cand := range candidates {
		if rest, ok := strings.CutPrefix(cand, word); ok {
			out = append(out, []rune(rest))
		}
	}
	return out, len([]rune(word))
}

// paths completes a netdisk path as typed (relative or absolute).
func (c *completer) paths(word string) []string {
	client, err := c.app.baidu()
	if err != nil {
		return nil
	}
	dir, prefix := path.Split(word)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	files, err := client.List(ctx, c.app.abs(cmp.Or(dir, ".")))
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range files {
		if strings.HasPrefix(f.Name, prefix) {
			name := dir + f.Name
			if f.IsDir {
				name += "/"
			} else {
				name += " "
			}
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
