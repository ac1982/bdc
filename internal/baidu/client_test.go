package baidu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

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
		{"one failed", &Error{Items: []BatchItem{{"/b", -8}}}, "/a,/c"},
		{"path in another case", &Error{Items: []BatchItem{{"/B", -8}}}, "/a,/c"},
		{"all failed", &Error{Items: []BatchItem{{"/a", -8}, {"/b", -9}, {"/c", -8}}}, ""},
		{"a failure without a path", &Error{Items: []BatchItem{{"", -9}}}, ""},
		{"a synchronous answer, done items included", &Error{Code: 132, Items: []BatchItem{{"/a", 0}, {"/b", 132}}}, "/a,/c"},
		{"no item said to fail", &Error{Code: 12, Items: []BatchItem{{"/a", 0}}}, ""},
		{"no answer per item", &Error{Code: -6}, ""},
		{"not a Baidu error", context.Canceled, ""},
	} {
		var froms []string
		for _, p := range doneItems(batch, func(r Rename) string { return r.From }, c.err) {
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

// A share link that does not exist: Baidu answers 404, claiming gzip for a
// plain body.
func TestSaveMissingShare(t *testing.T) {
	hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		h := http.Header{"Content-Type": {"text/html"}}
		if r.Header.Get("Accept-Encoding") != "identity" {
			h.Set("Content-Encoding", "gzip")
		}
		return &http.Response{StatusCode: 404, Header: h, Body: io.NopCloser(strings.NewReader("<!DOCTYPE html>error-404")), Request: r}, nil
	})}
	c, _ := New(hc, baidutest.Cookies)
	link, _ := ParseShareLink("https://pan.baidu.com/s/1nope", "")
	if _, err := c.SaveShare(context.Background(), link, "/x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// A network error names the URL, but not its query: that holds the bdstoken.
func TestNetworkErrorHidesToken(t *testing.T) {
	fake := baidutest.New().Client().Transport
	hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/create" {
			return nil, errors.New("connection reset")
		}
		return fake.RoundTrip(r)
	})}
	c, _ := New(hc, baidutest.Cookies)
	_, err := c.Mkdir(context.Background(), "/x")
	if err == nil || strings.Contains(err.Error(), "bdstoken") || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("err = %v", err)
	}
}

// Requests stopped by a security check together are repeated after one check.
func TestSecurityCheckOnce(t *testing.T) {
	f := baidutest.New()
	f.Guarded = true
	c, _ := New(f.Client(), baidutest.Cookies)
	var checks atomic.Int32
	c.SetVerifier(func(ctx context.Context, k *Check) error {
		checks.Add(1)
		ms, err := k.Methods(ctx)
		if err != nil || len(ms) != 1 || ms[0].Type != "sms" {
			return fmt.Errorf("methods %v: %w", ms, err)
		}
		if err := k.Send(ctx, ms[0].Type); err != nil {
			return err
		}
		return k.Submit(ctx, baidutest.VerifyCode)
	})
	var g errgroup.Group
	for i := range 4 {
		g.Go(func() error { _, err := c.Mkdir(context.Background(), fmt.Sprint("/d", i)); return err })
	}
	if err := g.Wait(); err != nil || checks.Load() != 1 {
		t.Fatalf("err %v, %d checks", err, checks.Load())
	}
}

// Without a verifier the check is an auth error.
func TestSecurityCheckWithoutVerifier(t *testing.T) {
	f := baidutest.New()
	f.Guarded = true
	c, _ := New(f.Client(), baidutest.Cookies)
	if _, err := c.Mkdir(context.Background(), "/d"); !errors.Is(err, ErrAuth) || Code(err) != 132 {
		t.Fatal(err)
	}
}

// A request waiting for another's check can be cancelled.
func TestSecurityCheckWaitCancelled(t *testing.T) {
	f := baidutest.New()
	f.Guarded = true
	c, _ := New(f.Client(), baidutest.Cookies)
	asking, release := make(chan struct{}), make(chan struct{})
	c.SetVerifier(func(ctx context.Context, k *Check) error {
		close(asking)
		<-release // the person takes their time
		return errors.New("gave up")
	})
	defer close(release)
	go c.Mkdir(context.Background(), "/a")
	<-asking
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Mkdir(ctx, "/b"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
