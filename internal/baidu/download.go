package baidu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// DownloadHeader is what file servers expect on download requests, as from
// the web app; send it with HTTP(), which carries the cookies. The link
// redirects once (d.pcs.baidu.com to a file server).
func DownloadHeader() http.Header {
	return http.Header{"User-Agent": {userAgent}, "Referer": {panBase}}
}

// DownloadURLs returns a link to the content of a file. Links expire after a
// few hours; ask again for a fresh one.
func (c *Client) DownloadURLs(ctx context.Context, fsID int64) ([]string, error) {
	op := "获取下载链接"
	v, err := c.vars(ctx, "sign1", "sign3", "timestamp")
	if err != nil {
		return nil, err
	}
	var sign1, sign3 string
	var ts int64
	json.Unmarshal(v["sign1"], &sign1)
	json.Unmarshal(v["sign3"], &sign3)
	json.Unmarshal(v["timestamp"], &ts)
	q := url.Values{
		"fidlist": {"[" + strconv.FormatInt(fsID, 10) + "]"}, "type": {"dlink"}, "vip": {"2"},
		"sign": {downloadSign(sign1, sign3)}, "timestamp": {strconv.FormatInt(ts, 10)},
	}
	var resp struct {
		Dlink []struct {
			Dlink string `json:"dlink"`
		} `json:"dlink"`
	}
	if err := c.do(ctx, &request{op: op, path: "api/download", query: q}, &resp); err != nil {
		return nil, err
	}
	var urls []string
	for _, d := range resp.Dlink {
		if d.Dlink != "" {
			urls = append(urls, d.Dlink)
		}
	}
	if len(urls) == 0 {
		return nil, &Error{Op: op, Message: "服务器没有返回下载链接"}
	}
	return urls, nil
}
