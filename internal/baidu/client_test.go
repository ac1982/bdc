package baidu

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ac1982/baidunetdisk-cli/internal/baidutest"
)

func TestDecode(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		code   int   // expected Baidu code
		class  error // expected errors.Is class, if any
		ok     bool
	}{
		{"pan ok", 200, `{"errno":0,"list":[]}`, 0, nil, true},
		{"pan not found", 200, `{"errno":-9}`, -9, ErrNotFound, false},
		{"pan auth", 200, `{"errno":-6}`, -6, ErrAuth, false},
		{"pcs not found on 404", 404, `{"error_code":31066,"error_msg":"file does not exist"}`, 31066, ErrNotFound, false},
		{"tieba string code", 200, `{"error_code":"110001","error_msg":"x"}`, 110001, nil, false},
		{"batch item", 200, `{"errno":12,"info":[{"errno":0},{"errno":-30,"path":"/b"}]}`, -30, ErrExists, false},
		{"info object is not a batch", 200, `{"errno":0,"return_type":2,"info":{"md5":"x"}}`, 0, nil, true},
		{"server error without code", 503, `{"message":"unavailable"}`, 0, nil, false},
		{"not JSON", 200, `<html>`, 0, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out map[string]any
			err := decode("op", c.status, []byte(c.body), &out)
			if (err == nil) != c.ok || Code(err) != c.code || (c.class != nil && !errors.Is(err, c.class)) {
				t.Errorf("decode = %v (code %d)", err, Code(err))
			}
		})
	}
}

// The fake rejects plain HTTP and checks the login, as the web app is checked.
func TestWhoamiOverHTTPS(t *testing.T) {
	f := baidutest.New()
	c, err := New(f.Client(), baidutest.Cookies)
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.Whoami(context.Background())
	if err != nil || u.UK != baidutest.UK || u.Name != baidutest.Name {
		t.Fatal(u, err)
	}
	bad, _ := New(f.Client(), "BDUSS=nope")
	if _, err := bad.Whoami(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatal(err)
	}
}

// Concurrent uploads share one client; its cached bdstoken must be safe to fill.
func TestTokenConcurrent(t *testing.T) {
	c, _ := New(baidutest.New().Client(), baidutest.Cookies)
	done := make(chan string)
	for range 8 {
		go func() { tok, _ := c.token(context.Background()); done <- tok }()
	}
	for range 8 {
		if <-done == "" {
			t.Fatal("no token")
		}
	}
}

func TestDoneItems(t *testing.T) {
	batch := []Rename{{From: "/a"}, {From: "/b"}, {From: "/c"}}
	for _, c := range []struct {
		name  string
		err   error
		froms string
	}{
		{"every item answered", &Error{Items: []BatchItem{{"/a", 0}, {"/b", -8}}}, "/a"},
		{"only the failure answered", &Error{Items: []BatchItem{{"/b", -8}}}, "/a"},
		{"a later success answered", &Error{Items: []BatchItem{{"/a", -8}, {"/c", 0}}}, "/c"},
		{"failure without a path", &Error{Items: []BatchItem{{"", -9}}}, ""},
		{"failure without a path, a success answered", &Error{Items: []BatchItem{{"/a", 0}, {"", -9}}}, "/a"},
		{"path in another case", &Error{Items: []BatchItem{{"/B", -8}}}, "/a"},
		{"no answer per item", &Error{Code: -6}, ""},
		{"not a Baidu error", context.Canceled, ""},
	} {
		var froms []string
		for _, p := range doneItems(batch, c.err) {
			froms = append(froms, p.From)
		}
		if got := strings.Join(froms, ","); got != c.froms {
			t.Errorf("%s: done %q, want %q", c.name, got, c.froms)
		}
	}
}

// While Baidu's share service is down, saving says so, without retrying.
func TestSaveShareServiceDown(t *testing.T) {
	calls := 0
	hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 500, Status: "500 Internal Server Error", Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	c, _ := New(hc, baidutest.Cookies)
	link, _ := ParseShareLink("https://pan.baidu.com/s/1abc?pwd=abcd", "")
	_, err := c.SaveShare(context.Background(), link, "/x")
	if err == nil || !strings.Contains(err.Error(), "暂时不可用") || calls != 1 {
		t.Fatalf("err %v after %d requests", err, calls)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A cookie Baidu sets replaces the login's stale one of the same name.
func TestNewestCookieWins(t *testing.T) {
	var sent []string
	hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		sent = append(sent, r.Header.Get("Cookie"))
		h := http.Header{"Content-Type": {"application/json"}}
		if len(sent) == 1 {
			h.Set("Set-Cookie", "BDCLND=fresh; Domain=.pan.baidu.com; Path=/; Secure")
		}
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(`{"errno":0}`)), Request: r}, nil
	})}
	c, _ := New(hc, "BDUSS=x; BDCLND=stale")
	for range 2 {
		c.do(context.Background(), &request{op: "t", path: "share/list"}, nil)
	}
	if strings.Contains(sent[1], "stale") || !strings.Contains(sent[1], "BDCLND=fresh") || !strings.Contains(sent[1], "BDUSS=x") {
		t.Fatalf("second request sent %q", sent[1])
	}
}
