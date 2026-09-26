package baidu

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

// Share is a share link the user created.
type Share struct {
	ID      int64     `json:"shareId"`
	Link    string    `json:"link"`
	Pwd     string    `json:"pwd,omitempty"`
	Expires time.Time `json:"expires,omitzero"` // zero: never
	Paths   []string  `json:"paths,omitempty"`
}

// CreateShare shares paths with a 4-character extraction code for days
// (0 = forever).
func (c *Client) CreateShare(ctx context.Context, pwd string, days int, paths ...string) (Share, error) {
	// The web app shares by fs_id; looking them up also catches missing paths
	// and a bad login, which pset would answer with unrelated errors.
	fs, err := c.Metas(ctx, paths...)
	if err != nil {
		return Share{}, err
	}
	ids := make([]int64, len(fs))
	for i, f := range fs {
		ids[i] = f.FsID
	}
	fids, _ := json.Marshal(ids)
	form := url.Values{
		"fid_list": {string(fids)}, "period": {strconv.Itoa(days)}, "pwd": {pwd},
		"schannel": {"4"}, "channel_list": {"[]"}, "public": {"0"}, "is_knowledge": {"0"},
		"eflag_disable": {"true"}, "linkOrQrcode": {"link"},
	}
	var resp struct {
		ShareID    int64  `json:"shareid"`
		Link       string `json:"link"`
		ExpireTime int64  `json:"expiretime"`
	}
	if err := c.do(ctx, &request{op: "分享", path: "share/pset", query: url.Values{"channel": {"chunlei"}}, write: true, form: form}, &resp); err != nil {
		return Share{}, err
	}
	if resp.Link == "" {
		return Share{}, &Error{Op: "分享", Message: "服务器没有返回链接"}
	}
	s := Share{ID: resp.ShareID, Link: resp.Link, Pwd: pwd, Paths: paths}
	if days > 0 && resp.ExpireTime > 0 {
		s.Expires = time.Unix(resp.ExpireTime, 0)
	}
	return s, nil
}

// CancelShares cancels share links by id.
func (c *Client) CancelShares(ctx context.Context, ids ...int64) error {
	list, _ := json.Marshal(ids)
	return c.do(ctx, &request{op: "取消分享", path: "share/cancel", query: url.Values{"channel": {"chunlei"}}, write: true,
		form: url.Values{"shareid_list": {string(list)}}}, nil)
}

// Shares lists the user's share links, newest first.
func (c *Client) Shares(ctx context.Context) ([]Share, error) {
	const pageSize = 100
	var all []Share
	for page := 1; ; page++ {
		var resp struct {
			List []struct {
				ShareID  int64  `json:"shareId"`
				Link     string `json:"shortlink"`
				Path     string `json:"typicalPath"`
				Passwd   string `json:"passwd"`
				ExpireAt int64  `json:"lastExpireTime"` // unix time; 0 for shares that never expire
			} `json:"list"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "num": {strconv.Itoa(pageSize)}, "order": {"ctime"}, "desc": {"1"}, "is_batch": {"1"}}
		if err := c.do(ctx, &request{op: "列出分享", path: "share/record", query: q}, &resp); err != nil {
			return all, err
		}
		for _, r := range resp.List {
			s := Share{ID: r.ShareID, Link: r.Link, Pwd: r.Passwd, Paths: []string{r.Path}}
			if r.ExpireAt > 0 {
				s.Expires = time.Unix(r.ExpireAt, 0)
			}
			all = append(all, s)
		}
		if len(resp.List) < pageSize {
			return all, nil
		}
	}
}
