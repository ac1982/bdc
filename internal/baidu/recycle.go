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
		if err := c.do(ctx, &request{op: "列出回收站", url: panBase + "api/recycle/list?" + q.Encode(), ua: uaNetdisk}, &resp); err != nil {
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
	list := make([]map[string]int64, len(fsIDs))
	for i, id := range fsIDs {
		list[i] = map[string]int64{"fs_id": id}
	}
	return c.do(ctx, &request{op: "还原", url: pcsURL("file", "restore", nil), param: map[string]any{"list": list}}, nil)
}

// Purge deletes files from the recycle bin for good.
func (c *Client) Purge(ctx context.Context, fsIDs ...int64) error {
	list, _ := json.Marshal(fsIDs)
	return c.do(ctx, &request{op: "彻底删除", url: panBase + "api/recycle/delete", form: url.Values{"fidlist": {string(list)}}, ua: uaNetdisk}, nil)
}

// EmptyRecycleBin deletes everything in the recycle bin for good.
func (c *Client) EmptyRecycleBin(ctx context.Context) error {
	return c.do(ctx, &request{op: "清空回收站", url: pcsURL("file", "delete", url.Values{"type": {"recycle"}})}, nil)
}
