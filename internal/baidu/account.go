package baidu

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// User is who the cookies belong to.
type User struct {
	UID  uint64 // Baidu user id
	Name string
}

// Whoami validates the login and returns the user. Baidu exposes the uid
// only through the tieba client's login endpoint.
func (c *Client) Whoami(ctx context.Context) (User, error) {
	ms := strconv.FormatInt(time.Now().UnixMilli(), 10)
	params := map[string]string{
		"bdusstoken":  c.bduss + "|null",
		"channel_id":  "",
		"channel_uid": "",
		"stErrorNums": "0",
		"subapp_type": "mini",
		"timestamp":   ms,
	}
	for k, v := range tiebaDevice {
		params[k] = v
	}
	form := url.Values{"sign": {tiebaSign(params)}}
	for k, v := range params {
		form.Set(k, v)
	}
	var resp struct {
		User struct {
			ID   flexInt `json:"id"`
			Name string  `json:"name"`
		} `json:"user"`
	}
	err := c.do(ctx, &request{
		op:     "验证登录",
		url:    "http://tieba.baidu.com/c/s/login",
		form:   form,
		ua:     "bdtb for Android 6.9.2.1",
		header: http.Header{"Net": {"1"}, "Client_logid": {ms}},
	}, &resp)
	if e, ok := errors.AsType[*Error](err); ok && e.Code != 0 {
		e.Err = ErrAuth // any tieba error means the BDUSS is not valid
	}
	if err != nil {
		return User{}, err
	}
	if resp.User.ID == 0 {
		return User{}, &Error{Op: "验证登录", Message: "BDUSS 无效或已过期", Err: ErrAuth}
	}
	c.UID = uint64(resp.User.ID)
	return User{UID: c.UID, Name: resp.User.Name}, nil
}

// UK returns the user's netdisk key, which upload requests are keyed on.
func (c *Client) UK(ctx context.Context) (int64, error) {
	if c.uk != 0 {
		return c.uk, nil
	}
	var resp struct {
		Records []struct {
			UK int64 `json:"uk"`
		} `json:"records"`
	}
	err := c.do(ctx, &request{op: "获取用户信息", url: panBase + "api/user/getinfo?need_selfinfo=1", ua: uaNone}, &resp)
	if err != nil {
		return 0, err
	}
	if len(resp.Records) == 0 {
		return 0, &Error{Op: "获取用户信息", Message: "没有用户信息, 登录的 Cookie 可能缺少 STOKEN", Err: ErrAuth}
	}
	c.uk = resp.Records[0].UK
	return c.uk, nil
}

// Quota is the netdisk capacity in bytes.
type Quota struct {
	Total int64 `json:"quota"`
	Used  int64 `json:"used"`
}

func (c *Client) Quota(ctx context.Context) (Quota, error) {
	var q Quota
	err := c.do(ctx, &request{op: "获取容量", url: pcsURL("quota", "info", nil)}, &q)
	return q, err
}
