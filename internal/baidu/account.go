package baidu

import (
	"context"
	"net/url"
)

// User is who the cookies belong to.
type User struct {
	UK   int64 // the user's netdisk key
	Name string
}

// Whoami validates the login and returns the user, as the web app learns
// them from its template variables.
func (c *Client) Whoami(ctx context.Context) (User, error) {
	me, err := c.whoami(ctx)
	if err != nil {
		return User{}, err
	}
	return me.user, nil
}

// Quota is the netdisk capacity in bytes.
type Quota struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}

func (c *Client) Quota(ctx context.Context) (Quota, error) {
	var q Quota
	err := c.do(ctx, &request{op: "获取容量", path: "api/quota", query: url.Values{"checkfree": {"1"}}}, &q)
	return q, err
}
