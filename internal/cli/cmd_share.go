package cli

import (
	"cmp"
	"crypto/rand"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ac1982/bdc/internal/baidu"
)

type shareCmd struct {
	Create shareCreateCmd `cmd:"" help:"创建分享链接"`
	List   shareListCmd   `cmd:"" help:"列出我的分享"`
	Cancel shareCancelCmd `cmd:"" help:"取消分享"`
	Save   shareSaveCmd   `cmd:"" help:"把别人的分享转存到我的网盘"`
}

type shareCreateCmd struct {
	Paths []string `arg:"" help:"要分享的文件或目录, 可用通配符"`
	Pwd   string   `short:"p" help:"4 位提取码, 默认随机生成"`
	Days  int      `default:"7" help:"有效天数, 0 为永久"`
}

type shareResult struct {
	baidu.Share
	URL string `json:"url"` // link with the extraction code
}

func (r shareResult) Human(w io.Writer) {
	fmt.Fprintf(w, "链接: %s 提取码: %s\n", r.Link, r.Pwd)
	fmt.Fprintf(w, "带提取码的链接: %s\n", r.URL)
	if r.Expires.IsZero() {
		fmt.Fprintf(w, "shareId: %d, 永久有效\n", r.ID)
	} else {
		fmt.Fprintf(w, "shareId: %d, 有效期至 %s\n", r.ID, clock(r.Expires))
	}
}

func (c *shareCreateCmd) Run(app *App) (Result, error) {
	if c.Pwd == "" {
		c.Pwd = randomCode()
	}
	if len(c.Pwd) != 4 {
		return nil, inputf("提取码必须是 4 位")
	}
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	paths, err := app.expand(client, c.Paths...)
	if err != nil {
		return nil, err
	}
	s, err := client.CreateShare(app.ctx, c.Pwd, c.Days, paths...)
	if err != nil {
		return nil, err
	}
	return shareResult{s, s.Link + "?pwd=" + s.Pwd}, nil
}

func randomCode() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 4)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

type shareListCmd struct{}

type sharesResult struct {
	Shares []baidu.Share `json:"shares"`
}

func (r sharesResult) Human(w io.Writer) {
	if len(r.Shares) == 0 {
		fmt.Fprintln(w, "没有分享")
		return
	}
	rows := [][]string{{"shareId", "链接", "提取码", "到期", "路径"}}
	for _, s := range r.Shares {
		exp := "永久"
		if !s.Expires.IsZero() {
			exp = clock(s.Expires)
		}
		rows = append(rows, []string{strconv.FormatInt(s.ID, 10), s.Link, cmp.Or(s.Pwd, "-"), exp, strings.Join(s.Paths, ", ")})
	}
	table(w, rows)
}

func (c *shareListCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	shares, err := client.Shares(app.ctx)
	return sharesResult{append([]baidu.Share{}, shares...)}, err
}

type shareCancelCmd struct {
	IDs []int64 `arg:"" name:"shareId" help:"要取消的分享的 shareId"`
}

type idsResult struct {
	IDs  []int64 `json:"ids"`
	verb string
}

func (r idsResult) Human(w io.Writer) {
	for _, id := range r.IDs {
		fmt.Fprintf(w, "已%s %d\n", r.verb, id)
	}
}

func (c *shareCancelCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	if err := client.CancelShares(app.ctx, c.IDs...); err != nil {
		return nil, err
	}
	return idsResult{c.IDs, "取消分享"}, nil
}

type shareSaveCmd struct {
	Link string `arg:"" help:"分享链接, 可带 ?pwd=; 也可以是含链接的整段文字"`
	Pwd  string `arg:"" optional:"" help:"提取码"`
	To   string `help:"保存到此目录, 默认为工作目录"`
}

type savedResult struct {
	Saved []string `json:"saved"`
	Dir   string   `json:"dir"`
}

func (r savedResult) Human(w io.Writer) {
	fmt.Fprintf(w, "已转存到 %s:\n", r.Dir)
	for _, p := range r.Saved {
		fmt.Fprintln(w, " ", p)
	}
}

func (c *shareSaveCmd) Run(app *App) (Result, error) {
	link, err := baidu.ParseShareLink(c.Link, c.Pwd)
	if err != nil {
		return nil, err
	}
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	dir := app.abs(c.To)
	saved, err := client.SaveShare(app.ctx, link, dir)
	if err != nil {
		return nil, err
	}
	return savedResult{append([]string{}, saved...), dir}, nil
}
