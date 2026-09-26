package baidu

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

// Deleted is a file in the recycle bin.
type Deleted struct {
	File
	DaysLeft int `json:"daysLeft"`
}

// Recycled lists the recycle bin.
func (c *Client) Recycled(ctx context.Context) ([]Deleted, error) {
	const pageSize = 100
	var all []Deleted
	for page := 1; ; page++ {
		var resp struct {
			List []struct {
				rawFile
				LeftTime int `json:"leftTime"`
			} `json:"list"`
		}
		q := url.Values{"num": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)}}
		if err := c.do(ctx, &request{op: "列出回收站", path: "api/recycle/list/", query: q}, &resp); err != nil {
			return all, err
		}
		for _, r := range resp.List {
			all = append(all, Deleted{File: r.file(), DaysLeft: r.LeftTime})
		}
		if len(resp.List) < pageSize {
			return all, nil
		}
	}
}

// Restore moves files from the recycle bin back to where they were.
func (c *Client) Restore(ctx context.Context, fsIDs ...int64) error {
	return c.recycle(ctx, "restore", "还原", fsIDs)
}

// Purge deletes files from the recycle bin for good. Baidu may demand a
// security check (errno 132) that only the web page or the app can pass.
func (c *Client) Purge(ctx context.Context, fsIDs ...int64) error {
	return c.recycle(ctx, "delete", "彻底删除", fsIDs)
}

// recycle restores or purges, waiting for the background task a big batch
// becomes (small ones are done at once, with no task).
func (c *Client) recycle(ctx context.Context, action, op string, fsIDs []int64) error {
	list, _ := json.Marshal(fsIDs)
	return c.runTask(ctx, &request{op: op, path: "api/recycle/" + action, query: url.Values{"channel": {"chunlei"}, "async": {"1"}},
		write: true, form: url.Values{"fidlist": {string(list)}}})
}
