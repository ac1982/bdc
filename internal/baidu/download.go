package baidu

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// DownloadHeader is what file servers expect on download requests; send it
// with HTTP(), which carries the cookies.
func DownloadHeader() http.Header {
	return http.Header{"User-Agent": {uaNetdisk}}
}

// DownloadURLs returns links to the content of a file, best first. They
// expire after a few hours.
func (c *Client) DownloadURLs(ctx context.Context, p string) ([]string, error) {
	q := url.Values{
		"ant": {"1"}, "apn_id": {"1_0"}, "app_id": {"250528"}, "channel": {"0"}, "check_blue": {"1"},
		"clienttype": {"17"}, "es": {"1"}, "esl": {"1"}, "freeisp": {"0"}, "method": {"locatedownload"},
		"path": {p}, "queryfree": {"0"}, "use": {"0"}, "ver": {"4.0"},
	}
	// The signature must follow the other parameters, in this order.
	t := time.Now().Unix()
	dev := devUID(c.bduss)
	raw := q.Encode() +
		"&time=" + strconv.FormatInt(t, 10) +
		"&rand=" + locateRand(c.bduss, c.UID, t, dev) +
		"&devuid=" + url.QueryEscape(dev) +
		"&cuid=" + url.QueryEscape(dev)
	var resp struct {
		URLs []struct {
			URL     string `json:"url"`
			Encrypt int    `json:"encrypt"`
		} `json:"urls"`
	}
	err := c.do(ctx, &request{op: "获取下载链接 " + p, method: "POST", url: pcsBase + "file?" + raw, ua: uaNetdisk}, &resp)
	if err != nil {
		return nil, err
	}
	var urls []string
	for _, u := range resp.URLs {
		if u.Encrypt == 0 && u.URL != "" {
			urls = append(urls, u.URL)
		}
	}
	return urls, nil
}
