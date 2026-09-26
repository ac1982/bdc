package baidu

import (
	"context"
	"errors"
	"net/url"
	"time"
)

// Check is a security check (errno 132): Baidu's risk control asks the user
// to confirm a change with a code sent by SMS or email. The web app shows it
// as a dialog, then repeats the request; the client does the same through
// its Verifier.
type Check struct {
	c    *Client
	form url.Values // safetpl, saferand, safesign: which check this is
}

// Method is a way to receive the code.
type Method struct {
	Type string // "sms" or "email"
	To   string // the masked number or address
}

// Verifier passes a security check, typically by asking the user; the
// request the check stopped is repeated when it returns nil.
type Verifier func(ctx context.Context, k *Check) error

// SetVerifier makes the client pass security checks with v; without one
// they fail with ErrAuth.
func (c *Client) SetVerifier(v Verifier) { c.verifier = v }

// Methods lists the ways the account can receive a code.
func (k *Check) Methods(ctx context.Context) ([]Method, error) {
	var resp struct {
		Data map[string]any `json:"data"` // sms, email, <type>_needbind, support_type
	}
	if err := k.call(ctx, "get", "获取验证方式", nil, &resp); err != nil {
		return nil, err
	}
	types, _ := resp.Data["support_type"].([]any)
	var ms []Method
	for _, t := range types {
		typ, _ := t.(string)
		to, _ := resp.Data[typ].(string)
		if bind, _ := resp.Data[typ+"_needbind"].(float64); to != "" && bind == 0 {
			ms = append(ms, Method{Type: typ, To: to})
		}
	}
	if len(ms) == 0 {
		return nil, &Error{Op: "获取验证方式", Message: "帐号没有可用的验证方式, 请在网页上验证", Err: ErrAuth}
	}
	return ms, nil
}

// Send sends a code by the method of type typ ("sms" or "email").
func (k *Check) Send(ctx context.Context, typ string) error {
	return k.call(ctx, "send", "发送验证码", url.Values{"type": {typ}}, nil)
}

// Submit passes the check with the code received.
func (k *Check) Submit(ctx context.Context, code string) error {
	return k.call(ctx, "check", "验证", url.Values{"vcode": {code}}, nil)
}

func (k *Check) call(ctx context.Context, method, op string, form url.Values, out any) error {
	f := url.Values{}
	for _, v := range []url.Values{k.form, form} {
		for key, vs := range v {
			f[key] = vs
		}
	}
	return k.c.send(ctx, &request{op: op, path: "api/authwidget", query: url.Values{"method": {method}}, form: f}, out)
}

// checkOf is the security check that stopped a request, if the client can
// pass it.
func (c *Client) checkOf(err error) *Check {
	e, ok := errors.AsType[*Error](err)
	if !ok || e.check == nil || c.verifier == nil {
		return nil
	}
	return &Check{c: c, form: e.check}
}

// pass passes a check that stopped a request sent at sent, unless another
// request passed one since: concurrent requests are stopped together, and
// the user is asked once.
func (c *Client) pass(ctx context.Context, k *Check, sent time.Time) error {
	c.checkMu.Lock()
	defer c.checkMu.Unlock()
	if c.passed.After(sent) {
		return nil
	}
	if err := c.verifier(ctx, k); err != nil {
		return err
	}
	c.passed = time.Now()
	return nil
}
