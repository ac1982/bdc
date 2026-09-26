package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ShareLink is a parsed https://pan.baidu.com/s/… link.
type ShareLink struct {
	Surl string // path segment after /s/, starting with "1"
	Pwd  string // extraction code, if any
}

var shareLinkRe = regexp.MustCompile(`pan\.baidu\.com/(?:s/(1[\w-]+)|share/init\?surl=([\w-]+))(?:[?&]pwd=(\w+))?`)

// ParseShareLink finds a share link in text (links are often pasted with a
// message around them). A non-empty pwd overrides one in the link.
func ParseShareLink(text, pwd string) (ShareLink, error) {
	m := shareLinkRe.FindStringSubmatch(text)
	if m == nil {
		return ShareLink{}, &Error{Op: "解析分享链接", Message: "不是百度网盘的分享链接: " + text, Err: ErrInvalid}
	}
	l := ShareLink{Surl: m[1], Pwd: m[3]}
	if l.Surl == "" {
		l.Surl = "1" + m[2]
	}
	if pwd != "" {
		l.Pwd = pwd
	}
	return l, nil
}

func (l ShareLink) url() string   { return panBase + "s/" + l.Surl }
func (l ShareLink) short() string { return l.Surl[1:] } // the id the share APIs take

// SaveShare saves everything in a share into dir, which must exist, and
// returns the saved top-level paths. It follows the web app: open the share
// page, verify the extraction code (which sets the BDCLND cookie), list the
// share's top level, and transfer it.
func (c *Client) SaveShare(ctx context.Context, link ShareLink, dir string) ([]string, error) {
	op := "转存 " + link.url()
	page, err := c.sharePage(ctx, link)
	if err != nil {
		return nil, err
	}
	referer := http.Header{"Referer": {link.url()}}
	if link.Pwd != "" {
		q := url.Values{"surl": {link.short()}, "bioc": {"1"}, "t": {strconv.FormatInt(time.Now().UnixMilli(), 10)}, "channel": {"chunlei"}}
		err := c.do(ctx, &request{op: op, path: "share/verify", query: q, write: true,
			form: url.Values{"pwd": {link.Pwd}, "vcode": {""}, "vcode_str": {""}}, header: referer}, nil)
		if Code(err) == -9 || Code(err) == -12 {
			return nil, &Error{Op: op, Code: -12, Message: "提取码错误"}
		}
		if err != nil {
			return nil, err
		}
	}

	ids, err := c.shareRoot(ctx, op, link, referer)
	if err != nil {
		return nil, err
	}
	fsids, _ := json.Marshal(ids)
	sekey, _ := url.QueryUnescape(c.cookie("BDCLND"))
	q := url.Values{"shareid": {page.ShareID.String()}, "from": {page.ShareUK.String()}, "sekey": {sekey},
		"async": {"1"}, "channel": {"chunlei"}}
	var resp transferResult
	err = c.do(ctx, &request{op: op, path: "share/transfer", query: q, write: true,
		form: url.Values{"fsidlist": {string(fsids)}, "path": {dir}}, header: referer}, &resp)
	// 文件已转存 (4) means saved here before; errno 2 is generic, and says
	// 文件已存在 for one's own share but also 转存路径不存在.
	if e, ok := errors.AsType[*Error](err); ok && (e.Code == 4 || e.Code == 2 && strings.Contains(e.Message, "已存在")) {
		e.Err = ErrExists
	}
	if err != nil {
		return nil, err
	}
	if resp.TaskID != 0 { // a big share is transferred in the background
		if resp, err = c.waitTask(ctx, op, resp.TaskID); err != nil {
			return nil, err
		}
	}
	return resp.saved(), nil
}

// transferResult is what a transfer (or its background task) reports.
type transferResult struct {
	TaskID int64 `json:"task_id"`
	Extra  struct {
		List []struct {
			To string `json:"to"`
		} `json:"list"`
	} `json:"extra"`
	List []struct { // from share/taskquery
		To string `json:"to"`
	} `json:"list"`
	Status    string `json:"status"`
	TaskErrno int    `json:"task_errno"`
}

func (r transferResult) saved() []string {
	var out []string
	for _, f := range r.Extra.List {
		out = append(out, f.To)
	}
	for _, f := range r.List {
		out = append(out, f.To)
	}
	return out
}

// waitTask polls a background task until it ends.
func (c *Client) waitTask(ctx context.Context, op string, id int64) (transferResult, error) {
	for {
		var r transferResult
		if err := c.do(ctx, &request{op: op, path: "share/taskquery", query: url.Values{"taskid": {strconv.FormatInt(id, 10)}}}, &r); err != nil {
			return r, err
		}
		switch r.Status {
		case "success":
			return r, nil
		case "failed":
			return r, &Error{Op: op, Code: r.TaskErrno, Message: firstNonEmpty(codeMessage[r.TaskErrno], "转存失败")}
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// shareRoot lists the fs_ids of a share's top level, page by page.
func (c *Client) shareRoot(ctx context.Context, op string, link ShareLink, referer http.Header) ([]int64, error) {
	const pageSize = 100
	var ids []int64
	for page := 1; ; page++ {
		var resp struct {
			List []struct {
				FsID flexInt `json:"fs_id"`
			} `json:"list"`
		}
		q := url.Values{"shorturl": {link.short()}, "root": {"1"}, "web": {"5"}, "page": {strconv.Itoa(page)},
			"num": {strconv.Itoa(pageSize)}, "order": {"time"}, "desc": {"1"}, "showempty": {"0"}, "view_mode": {"1"},
			"channel": {"chunlei"}}
		if err := c.do(ctx, &request{op: op, path: "share/list", query: q, write: true, header: referer}, &resp); err != nil {
			return ids, err
		}
		for _, f := range resp.List {
			ids = append(ids, int64(f.FsID))
		}
		if len(resp.List) < pageSize {
			if len(ids) == 0 {
				return nil, &Error{Op: op, Message: "分享中没有文件", Err: ErrNotFound}
			}
			return ids, nil
		}
	}
}

// sharePageData is what the share page tells a logged-in visitor.
type sharePageData struct {
	BDSToken   string  `json:"bdstoken"`
	ShareID    flexInt `json:"shareid"`
	ShareUK    flexInt `json:"share_uk"`
	LoginState int     `json:"loginstate"`
}

// sharePage opens the share's web page (it redirects to the code entry page
// when a code is needed) and reads the state embedded in it.
func (c *Client) sharePage(ctx context.Context, link ShareLink) (sharePageData, error) {
	op := "打开分享 " + link.url()
	var html []byte
	// No retries: while Baidu's share service is down, each attempt hangs
	// for most of a minute before failing.
	err := c.do(ctx, &request{op: op, path: link.url(), header: http.Header{"X-Requested-With": nil}, raw: true, once: true}, &html)
	page := string(html)
	if e, ok := errors.AsType[*Error](err); (ok && e.Status >= 500) || strings.Contains(page, "网盘正在升级") {
		return sharePageData{}, &Error{Op: op, Message: "百度网盘的分享服务暂时不可用 (百度显示正在升级), 请稍后再试", Err: err}
	}
	if err != nil {
		return sharePageData{}, err
	}
	d, found, err := pageLocals(page)
	switch {
	case err != nil:
		return d, &Error{Op: op, Message: "无法解析分享页面", Err: err}
	case !found && (strings.Contains(page, "platform-non-found") || strings.Contains(page, "error-404")):
		return d, &Error{Op: op, Message: "分享不存在或已失效", Err: ErrNotFound}
	case !found:
		return d, &Error{Op: op, Message: "无法识别分享页面"}
	case d.LoginState == 0:
		return d, &Error{Op: op, Message: "分享页面显示未登录; 登录的 Cookie 需要包含 STOKEN", Err: ErrAuth}
	case d.ShareID == 0:
		// Baidu's error page (e.g. /wap/error?errortype=0) also shows up
		// while its share service is degraded, so this is not proof the
		// share is gone.
		return d, &Error{Op: op, Message: "百度返回了错误页: 分享可能已失效, 也可能是分享服务暂时不可用, 请稍后再试", Err: ErrNotFound}
	}
	return d, nil
}

// pageLocals finds the page state: locals.mset({…}) on today's pages,
// window.locals = {…} on older and mobile ones (after an empty {}).
func pageLocals(page string) (d sharePageData, found bool, err error) {
	for _, marker := range []string{"locals.mset(", "window.locals = "} {
		for rest := page; ; {
			i := strings.Index(rest, marker)
			if i < 0 {
				break
			}
			rest = rest[i+len(marker):]
			if !strings.HasPrefix(rest, "{") || strings.HasPrefix(rest, "{}") {
				continue
			}
			err := json.NewDecoder(strings.NewReader(rest)).Decode(&d)
			return d, true, err
		}
	}
	return d, false, nil
}
