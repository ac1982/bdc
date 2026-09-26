// Package baidu is a client for Baidu Netdisk, speaking the API of its web
// client (pan.baidu.com in a browser) as that client does: one method per
// endpoint, typed results and typed errors. It never prints and keeps no
// global state. The protocol is written up in docs/baidu-api.md.
package baidu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
)

// The web client's identity: a desktop browser on the netdisk web app.
const (
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"
	panBase   = "https://pan.baidu.com/"
	webApp    = panBase + "disk/main" // the Referer of the app's API calls
)

// Client talks to Baidu as one logged-in user.
type Client struct {
	http *http.Client

	mu   sync.Mutex
	me   *identity // see whoami
	sign *signature

	verifier Verifier   // passes security checks; nil: they fail
	checkMu  sync.Mutex // one check at a time
	passed   time.Time  // when the last check was passed
}

// identity is what the template variables say about the logged-in user.
type identity struct {
	token string // bdstoken, carried by every change
	user  User
}

// New returns a client that authenticates with cookies ("BDUSS=…; STOKEN=…; …").
// The http client may be nil; its Jar is replaced.
func New(hc *http.Client, cookies string) (*Client, error) {
	inner, _ := cookiejar.New(nil)
	jar := newestJar{inner}
	parsed, err := http.ParseCookie(cookies)
	if err != nil {
		return nil, fmt.Errorf("无法解析 Cookie: %w", err)
	}
	c := &Client{http: &http.Client{}}
	if hc != nil {
		*c.http = *hc
	}
	c.http.Jar = jar
	hasBDUSS := false
	for _, ck := range parsed {
		hasBDUSS = hasBDUSS || ck.Name == "BDUSS"
		// Secure: the login never travels over plain HTTP.
		ck.Domain, ck.Path, ck.Secure = ".baidu.com", "/", true
	}
	if !hasBDUSS {
		return nil, &Error{Op: "登录", Message: "Cookie 中没有 BDUSS", Err: ErrInvalid}
	}
	jar.SetCookies(&url.URL{Scheme: "https", Host: "baidu.com"}, parsed)
	return c, nil
}

// newestJar sends one cookie per name, the newest. The login cookies are
// stored for .baidu.com, while Baidu sets its own for pan.baidu.com: after a
// share's extraction code is verified, a stale BDCLND from the login would
// otherwise be sent too, first, and the share would refuse to open (-9).
type newestJar struct{ http.CookieJar }

func (j newestJar) Cookies(u *url.URL) []*http.Cookie {
	all := j.CookieJar.Cookies(u) // oldest first among equally specific ones
	last := map[string]int{}
	for i, c := range all {
		last[c.Name] = i
	}
	var out []*http.Cookie
	for i, c := range all {
		if last[c.Name] == i {
			out = append(out, c)
		}
	}
	return out
}

// HTTP is the underlying client, cookies included, for transferring file data.
func (c *Client) HTTP() *http.Client { return c.http }

// cookie returns the value of a cookie the jar sends to pan.baidu.com.
func (c *Client) cookie(name string) string {
	for _, ck := range c.http.Jar.Cookies(&url.URL{Scheme: "https", Host: "pan.baidu.com", Path: "/"}) {
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
	path   string     // relative to pan.baidu.com, e.g. "api/list", or a full URL
	query  url.Values // pan paths also get the web app's common parameters
	write  bool       // a change: the query carries the bdstoken, as the web app's do
	form   url.Values // x-www-form-urlencoded body
	body   []byte     // any other body, of type ctype
	ctype  string
	header http.Header // added to (or replacing) the default headers
	raw    bool        // out is *rawResponse: status and body as they are, no decoding
	once   bool        // do not retry, even a GET
}

// url is the request's full URL.
func (c *Client) url(ctx context.Context, r *request) (string, error) {
	q := url.Values{}
	for k, v := range r.query {
		q[k] = v
	}
	base := r.path
	if !strings.HasPrefix(base, "https://") {
		base = panBase + r.path
		for k, v := range map[string]string{"clienttype": "0", "app_id": "250528", "web": "1"} {
			if !q.Has(k) {
				q.Set(k, v)
			}
		}
	}
	if r.write {
		token, err := c.token(ctx)
		if err != nil {
			return "", err
		}
		q.Set("bdstoken", token)
	}
	if len(q) == 0 {
		return base, nil
	}
	return base + "?" + q.Encode(), nil
}

// do sends req and decodes the JSON response into out (which may be nil).
// Transient failures of idempotent requests are retried, and a request
// stopped by a security check is repeated once the check is passed, as the
// web app does.
func (c *Client) do(ctx context.Context, req *request, out any) error {
	sent := time.Now()
	err := c.send(ctx, req, out)
	if k := c.checkOf(err); k != nil {
		if err := c.pass(ctx, k, sent); err != nil {
			return err
		}
		return c.send(ctx, req, out)
	}
	return err
}

// send is do without the security check.
func (c *Client) send(ctx context.Context, req *request, out any) error {
	u, err := c.url(ctx, req)
	if err != nil {
		return err
	}
	body, ctype := req.body, req.ctype
	if req.form != nil {
		body, ctype = []byte(req.form.Encode()), "application/x-www-form-urlencoded; charset=UTF-8"
	}
	method := req.method
	if method == "" {
		method = http.MethodGet
		if body != nil {
			method = http.MethodPost
		}
	}
	type response struct {
		status int
		body   []byte
	}
	send := func() (response, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		hr, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return response{}, backoff.Permanent(err)
		}
		hr.Header.Set("User-Agent", userAgent)
		hr.Header.Set("Referer", webApp)
		hr.Header.Set("X-Requested-With", "XMLHttpRequest")
		if body != nil {
			hr.Header.Set("Origin", "https://pan.baidu.com")
		}
		if ctype != "" {
			hr.Header.Set("Content-Type", ctype)
		}
		for k, v := range req.header {
			hr.Header[k] = v
		}
		resp, err := c.http.Do(hr)
		if err != nil {
			if ctx.Err() != nil {
				return response{}, backoff.Permanent(ctx.Err())
			}
			return response{}, withoutQuery(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return response{}, err
		}
		if resp.StatusCode >= 500 && method == http.MethodGet && !req.once { // retried below
			return response{}, &Error{Op: req.op, Status: resp.StatusCode, Message: "HTTP " + resp.Status}
		}
		return response{resp.StatusCode, data}, nil
	}
	var resp response
	if method == http.MethodGet && !req.once {
		resp, err = backoff.Retry(ctx, send, backoff.WithMaxTries(3),
			backoff.WithBackOff(&backoff.ExponentialBackOff{InitialInterval: 500 * time.Millisecond, Multiplier: 2, MaxInterval: 4 * time.Second}))
	} else {
		resp, err = send()
	}
	if err != nil {
		if _, ok := errors.AsType[*Error](err); ok || errors.Is(err, context.Canceled) {
			return err
		}
		return &Error{Op: req.op, Err: err}
	}
	if req.raw {
		*out.(*rawResponse) = rawResponse{resp.status, resp.body}
		return nil
	}
	return decode(req.op, resp.status, resp.body, out)
}

// withoutQuery drops the query, which holds the bdstoken, from the URL a
// network error names, so that messages never show it.
func withoutQuery(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		ue.URL, _, _ = strings.Cut(ue.URL, "?")
	}
	return err
}

// rawResponse is a response handed back undecoded.
type rawResponse struct {
	Status int
	Body   []byte
}

// vars reads the web app's template variables (token, uk, user name, the
// download signature's inputs, …).
func (c *Client) vars(ctx context.Context, fields ...string) (map[string]json.RawMessage, error) {
	f, _ := json.Marshal(fields)
	var resp struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	err := c.do(ctx, &request{op: "获取帐号信息", path: "api/gettemplatevariable", query: url.Values{"fields": {string(f)}}}, &resp)
	return resp.Result, err
}

// whoami fetches the user and the bdstoken once, from the template variables.
func (c *Client) whoami(ctx context.Context) (*identity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.me != nil {
		return c.me, nil
	}
	v, err := c.vars(ctx, "bdstoken", "uk", "username")
	if err != nil {
		return nil, err
	}
	var me identity
	json.Unmarshal(v["bdstoken"], &me.token)
	json.Unmarshal(v["uk"], &me.user.UK)
	json.Unmarshal(v["username"], &me.user.Name)
	if me.token == "" || me.user.UK == 0 {
		return nil, &Error{Op: "验证登录", Message: "登录无效或已过期", Err: ErrAuth}
	}
	c.me = &me
	return c.me, nil
}

// token is the bdstoken every change carries.
func (c *Client) token(ctx context.Context) (string, error) {
	me, err := c.whoami(ctx)
	if err != nil {
		return "", err
	}
	return me.token, nil
}

// decode turns a response into out or an error: Baidu's own error codes
// first (error_code, errno, and per-item errors of batch calls), then the
// HTTP status, since a failed call may carry no code at all.
func decode(op string, status int, data []byte, out any) error {
	var st struct {
		ErrorCode flexInt         `json:"error_code"`
		ErrorMsg  string          `json:"error_msg"`
		Errno     flexInt         `json:"errno"`
		ShowMsg   string          `json:"show_msg"`
		ErrMsg    string          `json:"errmsg"`
		Info      json.RawMessage `json:"info"` // per item in batch calls; other shapes elsewhere
		Check     struct {
			Tpl  string `json:"safetpl"`
			Rand string `json:"saferand"`
			Sign string `json:"safesign"`
		} `json:"authwidget"` // a security check (errno 132)
	}
	jsonErr := json.Unmarshal(data, &st)
	code := int(st.ErrorCode)
	if code == 0 {
		code = int(st.Errno)
	}
	var items []BatchItem
	if code == errBatch && json.Unmarshal(st.Info, &items) == nil { // a batch call failed; the item says why
		for _, it := range items {
			if it.Errno != 0 {
				code = int(it.Errno)
				if it.Path != "" {
					op += " (" + it.Path + ")"
				}
				break
			}
		}
	}
	switch {
	case code != 0:
		msg := codeMessage[code]
		if msg == "" {
			msg = firstNonEmpty(st.ShowMsg, st.ErrorMsg, st.ErrMsg, "未知错误")
		}
		e := &Error{Op: op, Code: code, Message: msg, Items: items}
		if st.Check.Sign != "" {
			e.check = url.Values{"safetpl": {st.Check.Tpl}, "saferand": {st.Check.Rand}, "safesign": {st.Check.Sign}}
		}
		return e
	case status < 200 || status > 299:
		return &Error{Op: op, Status: status, Message: fmt.Sprintf("HTTP %d: %s", status, snippet(data))}
	case jsonErr != nil:
		return &Error{Op: op, Message: "无法解析服务器的响应: " + snippet(data), Err: jsonErr}
	case out == nil:
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &Error{Op: op, Message: "无法解析服务器的响应: " + snippet(data), Err: err}
	}
	return nil
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

func (n flexInt) String() string { return strconv.FormatInt(int64(n), 10) }

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
