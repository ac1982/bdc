package baidu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// DownloadHeader is what file servers expect on download requests, as from
// the web app; send it with HTTP(), which carries the cookies. The link
// redirects once (d.pcs.baidu.com to a file server).
func DownloadHeader() http.Header {
	return http.Header{"User-Agent": {userAgent}, "Referer": {panBase}}
}

// signature signs download requests; the web app gets it from the template
// variables. It is reused for a while and refreshed when Baidu rejects it.
type signature struct {
	sign, timestamp string
	fetched         time.Time
}

// signatureTTL is how long a download signature is reused.
const signatureTTL = 5 * time.Minute

func (c *Client) signature(ctx context.Context, fresh bool) (*signature, error) {
	c.mu.Lock()
	s := c.sign
	c.mu.Unlock()
	if s != nil && !fresh && time.Since(s.fetched) < signatureTTL {
		return s, nil
	}
	v, err := c.vars(ctx, "sign1", "sign3", "timestamp")
	if err != nil {
		return nil, err
	}
	var sign1, sign3 string
	var ts int64
	json.Unmarshal(v["sign1"], &sign1)
	json.Unmarshal(v["sign3"], &sign3)
	json.Unmarshal(v["timestamp"], &ts)
	s = &signature{sign: downloadSign(sign1, sign3), timestamp: strconv.FormatInt(ts, 10), fetched: time.Now()}
	c.mu.Lock()
	c.sign = s
	c.mu.Unlock()
	return s, nil
}

// DownloadURLs returns a link to the content of a file. Links expire after a
// few hours; ask again for a fresh one.
func (c *Client) DownloadURLs(ctx context.Context, fsID int64) ([]string, error) {
	op := "获取下载链接"
	var resp struct {
		Dlink []struct {
			Dlink string `json:"dlink"`
		} `json:"dlink"`
	}
	for attempt := 0; ; attempt++ {
		s, err := c.signature(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		q := url.Values{"fidlist": {"[" + strconv.FormatInt(fsID, 10) + "]"}, "type": {"dlink"}, "vip": {"2"},
			"sign": {s.sign}, "timestamp": {s.timestamp}}
		err = c.do(ctx, &request{op: op, path: "api/download", query: q}, &resp)
		if code := Code(err); (code == errPageExpired || code == errBadSign) && attempt == 0 {
			continue // the signature went stale
		}
		if err != nil {
			return nil, err
		}
		break
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

// Baidu's answers to a stale download signature.
const (
	errPageExpired = 112 // 页面已过期
	errBadSign     = 113 // 签名错误
)
