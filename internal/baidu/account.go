package baidu

import (
	"context"
	"encoding/json"
	"net/url"
)

// User is who the cookies belong to.
type User struct {
	UK   int64 // the user's netdisk key
	Name string
}

// Whoami validates the login and returns the user, as the web app learns
// them from its template variables. The bdstoken comes along and is kept.
func (c *Client) Whoami(ctx context.Context) (User, error) {
	v, err := c.vars(ctx, "bdstoken", "uk", "username")
	if err != nil {
		return User{}, err
	}
	var u User
	var token string
	json.Unmarshal(v["uk"], &u.UK)
	json.Unmarshal(v["username"], &u.Name)
	json.Unmarshal(v["bdstoken"], &token)
	if u.UK == 0 || token == "" {
		return User{}, &Error{Op: "验证登录", Message: "登录无效或已过期", Err: ErrAuth}
	}
	c.mu.Lock()
	c.bdstoken = token
	c.mu.Unlock()
	return u, nil
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
