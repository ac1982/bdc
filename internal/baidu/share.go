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
	list, _ := json.Marshal(paths)
	form := url.Values{
		"path_list":    {string(list)},
		"schannel":     {"4"},
		"channel_list": {"[]"},
		"period":       {strconv.Itoa(days)},
		"pwd":          {pwd},
		"share_type":   {"9"},
	}
	var resp struct {
		ShareID    int64  `json:"shareid"`
		Link       string `json:"link"`
		ExpireTime int64  `json:"expiretime"`
	}
	if err := c.do(ctx, &request{op: "分享", url: panBase + "share/pset", form: form, ua: uaNetdisk}, &resp); err != nil {
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
	return c.do(ctx, &request{op: "取消分享", url: panBase + "share/cancel", form: url.Values{"shareid_list": {string(list)}}, ua: uaNetdisk}, nil)
}

// Shares lists the user's share links, newest first.
func (c *Client) Shares(ctx context.Context) ([]Share, error) {
	var all []Share
	for page := 1; ; page++ {
		var resp struct {
			List json.RawMessage `json:"list"` // [] or, when empty, {}
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "desc": {"1"}, "order": {"time"}}
		if err := c.do(ctx, &request{op: "列出分享", url: panBase + "share/record?" + q.Encode(), ua: uaNetdisk}, &resp); err != nil {
			return all, err
		}
		var list []struct {
			ShareID     int64  `json:"shareId"`
			Link        string `json:"shortlink"`
			Path        string `json:"typicalPath"`
			ExpiredType int    `json:"expiredType"`
			ExpiredTime int64  `json:"expiredTime"` // seconds left, 0 = never
		}
		if json.Unmarshal(resp.List, &list) != nil || len(list) == 0 {
			return all, nil
		}
		for _, r := range list {
			s := Share{ID: r.ShareID, Link: r.Link, Paths: []string{r.Path}}
			if r.ExpiredTime > 0 {
				s.Expires = time.Now().Add(time.Duration(r.ExpiredTime) * time.Second).Truncate(time.Second)
			}
			all = append(all, s)
		}
	}
}
