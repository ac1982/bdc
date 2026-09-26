package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ac1982/baidunetdisk-cli/internal/browser"
	"github.com/ac1982/baidunetdisk-cli/internal/config"
)

type loginCmd struct {
	Cookies    string `help:"浏览器中 pan.baidu.com 的 Cookie, 至少含 BDUSS; 转存还需要 STOKEN" placeholder:"\"BDUSS=…; STOKEN=…\""`
	FromChrome bool   `help:"读取 Chrome 中的登录" xor:"source"`
	FromEdge   bool   `help:"读取 Edge 中的登录" xor:"source"`
	Profile    string `help:"浏览器的用户配置, 如 \"Profile 1\"" default:"Default"`
}

type accountResult struct {
	LoggedIn bool   `json:"loggedIn"`
	UK       int64  `json:"uk,omitempty"`
	Name     string `json:"name,omitempty"`
	Workdir  string `json:"workdir,omitempty"`
	Current  bool   `json:"current,omitempty"` // in users: the active account
}

func newAccountResult(a *config.Account) accountResult {
	if a == nil {
		return accountResult{}
	}
	return accountResult{LoggedIn: true, UK: a.UK, Name: a.Name, Workdir: a.Workdir}
}

func (r accountResult) Human(w io.Writer) {
	if !r.LoggedIn {
		fmt.Fprintln(w, "未登录")
		return
	}
	fmt.Fprintf(w, "%s (uk %d), 工作目录 %s\n", r.Name, r.UK, r.Workdir)
}

func (c *loginCmd) Run(app *App) (Result, error) {
	cookies := c.Cookies
	switch {
	case c.FromChrome || c.FromEdge:
		b := browser.Chrome
		if c.FromEdge {
			b = browser.Edge
		}
		var err error
		if cookies, err = browser.Cookies(b, c.Profile); errors.Is(err, browser.ErrNoLogin) {
			return nil, withKind(Auth, err)
		} else if err != nil {
			return nil, withKind(Dependency, err)
		}
	case cookies == "" && app.interactive():
		var err error
		if cookies, err = app.ask(app.ctx, "粘贴 pan.baidu.com 的 Cookie (至少含 BDUSS): "); err != nil {
			return nil, errCancelled
		}
		if cookies == "" {
			return nil, inputf("没有输入 Cookie")
		}
	case cookies == "":
		return nil, usagef("需要 --cookies, --from-chrome 或 --from-edge")
	}

	client, err := newClient(app.cfg.Settings, cookies, app.transport)
	if err != nil {
		return nil, withKind(Input, err)
	}
	user, err := client.Whoami(app.ctx)
	if err != nil {
		return nil, err
	}
	app.cfg.Put(config.Account{UK: user.UK, Name: user.Name, Cookies: withoutShareCookies(cookies)})
	if err := app.cfg.Save(); err != nil {
		return nil, err
	}
	return newAccountResult(app.cfg.Current()), nil
}

type logoutCmd struct {
	Yes bool `short:"y" help:"不再确认"`
}

func (c *logoutCmd) Run(app *App) (Result, error) {
	acc, err := app.account()
	if err != nil {
		return nil, err
	}
	if err := app.confirm(c.Yes, fmt.Sprintf("退出帐号 %s (uk %d)?", acc.Name, acc.UK)); err != nil {
		return nil, err
	}
	app.cfg.Remove(acc.UK)
	if err := app.cfg.Save(); err != nil {
		return nil, err
	}
	return newAccountResult(app.cfg.Current()), nil
}

type whoCmd struct{}

func (c *whoCmd) Run(app *App) (Result, error) {
	return newAccountResult(app.cfg.Current()), nil
}

type usersCmd struct{}

type usersResult struct {
	Users []accountResult `json:"users"`
}

func (r usersResult) Human(w io.Writer) {
	if len(r.Users) == 0 {
		fmt.Fprintln(w, "没有已登录的帐号")
		return
	}
	rows := [][]string{{"", "UK", "用户名", "工作目录"}}
	for _, u := range r.Users {
		mark := ""
		if u.Current {
			mark = "*"
		}
		rows = append(rows, []string{mark, strconv.FormatInt(u.UK, 10), u.Name, u.Workdir})
	}
	table(w, rows)
}

func (c *usersCmd) Run(app *App) (Result, error) {
	var r usersResult
	for i := range app.cfg.Accounts {
		a := &app.cfg.Accounts[i]
		u := newAccountResult(a)
		u.Current = a.UK == app.cfg.Active
		r.Users = append(r.Users, u)
	}
	return r, nil
}

type suCmd struct {
	User string `arg:"" help:"要切换到的帐号: uk 或用户名"`
}

func (c *suCmd) Run(app *App) (Result, error) {
	for _, a := range app.cfg.Accounts {
		if strconv.FormatInt(a.UK, 10) == c.User || a.Name == c.User {
			app.cfg.Active = a.UK
			if err := app.cfg.Save(); err != nil {
				return nil, err
			}
			return newAccountResult(app.cfg.Current()), nil
		}
	}
	return nil, inputf("没有帐号 %s, 用 bdc users 查看", c.User)
}

type quotaCmd struct{}

type quotaResult struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
	Free  int64 `json:"free"`
}

func (r quotaResult) Human(w io.Writer) {
	pct := 0.0
	if r.Total > 0 {
		pct = float64(r.Used) / float64(r.Total) * 100
	}
	fmt.Fprintf(w, "已用 %s / %s (%.1f%%), 剩余 %s\n", size(r.Used), size(r.Total), pct, size(r.Free))
}

func (c *quotaCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	q, err := client.Quota(app.ctx)
	if err != nil {
		return nil, err
	}
	return quotaResult{Total: q.Total, Used: q.Used, Free: q.Total - q.Used}, nil
}

// withoutShareCookies drops BDCLND, which opens one share after its code was
// entered; kept from a browser it only gets in the way of other shares.
func withoutShareCookies(cookies string) string {
	var keep []string
	for _, part := range strings.Split(cookies, ";") {
		if part = strings.TrimSpace(part); part != "" && !strings.HasPrefix(part, "BDCLND=") {
			keep = append(keep, part)
		}
	}
	return strings.Join(keep, "; ")
}
