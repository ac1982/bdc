package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func (l ShareLink) url() string { return panBase + "s/" + l.Surl }

// SaveShare saves everything in a share into dir and returns the saved
// top-level paths.
func (c *Client) SaveShare(ctx context.Context, link ShareLink, dir string) ([]string, error) {
	op := "转存 " + link.url()
	page, err := c.sharePage(ctx, link, panBase+"disk/home")
	if err != nil {
		return nil, err
	}
	if link.Pwd != "" {
		q := url.Values{"shareid": {page.ShareID.String()}, "uk": {page.ShareUK.String()},
			"time": {strconv.FormatInt(time.Now().UnixMilli(), 10)}, "clienttype": {"1"}}
		err := c.do(ctx, &request{
			op:     op,
			url:    panBase + "share/verify?" + q.Encode(),
			form:   url.Values{"pwd": {link.Pwd}, "vcode": {"null"}, "vcode_str": {"null"}, "bdstoken": {page.BDSToken}},
			ua:     uaBrowser,
			header: http.Header{"Referer": {link.url()}},
		}, nil)
		if Code(err) == -9 || Code(err) == -12 {
			return nil, &Error{Op: op, Code: -12, Message: "提取码错误"}
		}
		if err != nil {
			return nil, err
		}
		// The verification cookie (BDCLND) is in the jar now; reload for fresh tokens.
		if page, err = c.sharePage(ctx, link, panBase+"share/init?surl="+link.Surl[1:]); err != nil {
			return nil, err
		}
	}

	var list struct {
		List []struct {
			FsID flexInt `json:"fs_id"`
			Name string  `json:"server_filename"`
		} `json:"list"`
	}
	q := url.Values{"shorturl": {link.Surl[1:]}, "root": {"1"}, "web": {"5"}, "app_id": {"250528"},
		"channel": {"chunlei"}, "bdstoken": {page.BDSToken}}
	if err := c.do(ctx, &request{op: op, url: panBase + "share/list?" + q.Encode(), ua: uaBrowser}, &list); err != nil {
		return nil, err
	}
	if len(list.List) == 0 {
		return nil, &Error{Op: op, Message: "分享中没有文件", Err: ErrNotFound}
	}
	ids := make([]int64, len(list.List))
	for i, f := range list.List {
		ids[i] = int64(f.FsID)
	}
	fsids, _ := json.Marshal(ids)

	q = url.Values{"app_id": {"250528"}, "channel": {"chunlei"}, "clienttype": {"0"}, "web": {"1"},
		"shareid": {page.ShareID.String()}, "from": {page.ShareUK.String()}, "bdstoken": {page.BDSToken},
		"filename": {list.List[0].Name}}
	var resp struct {
		Info []struct {
			Path string `json:"path"`
		} `json:"info"`
		Extra struct {
			List []struct {
				To string `json:"to"`
			} `json:"list"`
		} `json:"extra"`
	}
	err = c.do(ctx, &request{
		op:     op,
		url:    panBase + "share/transfer?" + q.Encode(),
		form:   url.Values{"fsidlist": {string(fsids)}, "path": {dir}},
		ua:     uaBrowser,
		header: http.Header{"Referer": {link.url()}},
	}, &resp)
	if e, ok := errors.AsType[*Error](err); ok && e.Code == 2 {
		e.Err = ErrExists // here errno 2 means 文件已存在 (e.g. one's own share)
	}
	if err != nil {
		return nil, err
	}
	var saved []string
	for _, f := range resp.Extra.List {
		saved = append(saved, f.To)
	}
	if len(saved) == 0 {
		for _, f := range resp.Info {
			saved = append(saved, f.Path)
		}
	}
	return saved, nil
}

// sharePageData is what the share page tells a logged-in visitor.
type sharePageData struct {
	BDSToken   string  `json:"bdstoken"`
	ShareID    flexInt `json:"shareid"`
	ShareUK    flexInt `json:"share_uk"`
	LoginState int     `json:"loginstate"`
}

func (n flexInt) String() string { return strconv.FormatInt(int64(n), 10) }

// sharePage loads the share's web page and reads its embedded state.
func (c *Client) sharePage(ctx context.Context, link ShareLink, referer string) (sharePageData, error) {
	op := "打开分享 " + link.url()
	var html []byte
	err := c.do(ctx, &request{op: op, url: link.url(), ua: uaNetdisk, header: http.Header{"Referer": {referer}}, raw: true}, &html)
	if err != nil {
		return sharePageData{}, err
	}
	page := string(html)
	for rest := page; ; {
		i := strings.Index(rest, "window.locals = ")
		if i < 0 {
			break
		}
		rest = rest[i+len("window.locals = "):]
		if !strings.HasPrefix(rest, "{") || strings.HasPrefix(rest, "{}") {
			continue
		}
		var d sharePageData
		if err := json.NewDecoder(strings.NewReader(rest)).Decode(&d); err != nil {
			return d, &Error{Op: op, Message: "无法解析分享页面", Err: err}
		}
		if d.LoginState == 0 || d.BDSToken == "" {
			return d, &Error{Op: op, Message: "分享页面显示未登录; 登录的 Cookie 需要包含 STOKEN", Err: ErrAuth}
		}
		if d.ShareID == 0 {
			return d, &Error{Op: op, Message: "分享不存在或已失效", Err: ErrNotFound}
		}
		return d, nil
	}
	if strings.Contains(page, "platform-non-found") || strings.Contains(page, "error-404") {
		return sharePageData{}, &Error{Op: op, Message: "分享不存在或已失效", Err: ErrNotFound}
	}
	return sharePageData{}, &Error{Op: op, Message: fmt.Sprintf("无法识别分享页面 (%d 字节)", len(html))}
}
