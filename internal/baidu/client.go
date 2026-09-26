// Package baidu is a client for the Baidu Netdisk API: one method per
// endpoint, typed results and typed errors. It never prints and keeps no
// global state.
package baidu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v5"
)

// User agents Baidu expects. PCS endpoints want none at all.
const (
	uaNetdisk = "netdisk;P2SP;3.0.0.8;netdisk;11.12.3;ANG-AN00;android-android;10.0;JSbridge4.4.0;jointBridge;1.1.0;"
	uaBrowser = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
	uaNone    = ""
)

const (
	pcsBase = "https://pcs.baidu.com/rest/2.0/pcs/"
	panBase = "https://pan.baidu.com/"
)

// Client talks to Baidu as one logged-in user.
type Client struct {
	UID uint64 // Baidu user id; needed to sign download requests

	http  *http.Client
	bduss string
	uk    int64 // cached by UK
}

// New returns a client that authenticates with cookies ("BDUSS=…; STOKEN=…; …").
// The http client may be nil; its Jar is replaced.
func New(hc *http.Client, cookies string, uid uint64) (*Client, error) {
	jar, _ := cookiejar.New(nil)
	parsed, err := http.ParseCookie(cookies)
	if err != nil {
		return nil, fmt.Errorf("无法解析 Cookie: %w", err)
	}
	c := &Client{UID: uid, http: &http.Client{}}
	if hc != nil {
		*c.http = *hc
	}
	c.http.Jar = jar
	for _, ck := range parsed {
		if ck.Name == "BDUSS" {
			c.bduss = ck.Value
		}
		ck.Domain, ck.Path = ".baidu.com", "/"
	}
	if c.bduss == "" {
		return nil, &Error{Op: "登录", Message: "Cookie 中没有 BDUSS", Err: ErrInvalid}
	}
	jar.SetCookies(&url.URL{Scheme: "https", Host: "baidu.com"}, parsed)
	return c, nil
}

// HTTP is the underlying client, cookies included, for transferring file data.
func (c *Client) HTTP() *http.Client { return c.http }

// cookie returns the value of a cookie sent to pan.baidu.com.
func (c *Client) cookie(name string) string {
	for _, ck := range c.http.Jar.Cookies(&url.URL{Scheme: "https", Host: "pan.baidu.com"}) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

// request describes one API call.
type request struct {
	op     string     // for error messages, e.g. "删除 /a"
	method string     // GET unless a body is given
	url    string     // full URL including the query
	form   url.Values // x-www-form-urlencoded body
	param  any        // PCS batch endpoints: JSON in the multipart field "param"
	body   []byte     // any other body, of type ctype
	ctype  string
	ua     string
	header http.Header
	raw    bool // out is *[]byte: return the body as is, no error decoding
}

// do sends req and decodes the JSON response into out (which may be nil).
// Transient failures of idempotent requests are retried.
func (c *Client) do(ctx context.Context, req *request, out any) error {
	body, ctype, err := req.encode()
	if err != nil {
		return &Error{Op: req.op, Err: err}
	}
	method := req.method
	if method == "" {
		method = http.MethodGet
		if body != nil {
			method = http.MethodPost
		}
	}
	send := func() ([]byte, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		hr, err := http.NewRequestWithContext(ctx, method, req.url, rd)
		if err != nil {
			return nil, backoff.Permanent(err)
		}
		for k, v := range req.header {
			hr.Header[k] = v
		}
		hr.Header.Set("User-Agent", req.ua)
		if ctype != "" {
			hr.Header.Set("Content-Type", ctype)
		}
		resp, err := c.http.Do(hr)
		if err != nil {
			if ctx.Err() != nil {
				return nil, backoff.Permanent(ctx.Err())
			}
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 500 && method == http.MethodGet {
			return nil, fmt.Errorf("HTTP %s", resp.Status)
		}
		return data, nil
	}
	var data []byte
	if method == http.MethodGet {
		data, err = backoff.Retry(ctx, send, backoff.WithMaxTries(3),
			backoff.WithBackOff(&backoff.ExponentialBackOff{InitialInterval: 500 * time.Millisecond, Multiplier: 2, MaxInterval: 4 * time.Second}))
	} else {
		data, err = send()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return &Error{Op: req.op, Err: err}
	}
	if req.raw {
		*out.(*[]byte) = data
		return nil
	}
	return decode(req.op, data, out)
}

// decode checks Baidu's two error conventions, then unmarshals into out.
func decode(op string, data []byte, out any) error {
	var st struct {
		ErrorCode flexInt `json:"error_code"` // PCS; tieba sends it as a string
		ErrorMsg  string  `json:"error_msg"`
		Errno     flexInt `json:"errno"` // pan
		ShowMsg   string  `json:"show_msg"`
		ErrMsg    string  `json:"errmsg"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return &Error{Op: op, Message: "无法解析服务器的响应: " + snippet(data), Err: err}
	}
	code := int(st.ErrorCode)
	if code == 0 {
		code = int(st.Errno)
	}
	if code != 0 {
		msg := codeMessage[code]
		if msg == "" {
			msg = firstNonEmpty(st.ShowMsg, st.ErrorMsg, st.ErrMsg, "未知错误")
		}
		return &Error{Op: op, Code: code, Message: msg}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &Error{Op: op, Message: "无法解析服务器的响应: " + snippet(data), Err: err}
	}
	return nil
}

func (r *request) encode() (body []byte, ctype string, err error) {
	switch {
	case r.body != nil:
		return r.body, r.ctype, nil
	case r.form != nil:
		return []byte(r.form.Encode()), "application/x-www-form-urlencoded", nil
	case r.param != nil:
		p, err := json.Marshal(r.param)
		if err != nil {
			return nil, "", err
		}
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		w.WriteField("param", string(p))
		w.Close()
		return buf.Bytes(), w.FormDataContentType(), nil
	}
	return nil, "", nil
}

// pcsURL builds a PCS REST URL: pcs.baidu.com/rest/2.0/pcs/<path>?method=…&app_id=….
func pcsURL(path, method string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	q.Set("method", method)
	if q.Get("app_id") == "" {
		q.Set("app_id", "266719")
	}
	return pcsBase + path + "?" + q.Encode()
}

// flexInt decodes a JSON number or a numeric string.
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	*n = flexInt(v)
	return err
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}
