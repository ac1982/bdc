package baidu

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Live tests talk to Baidu with a real account. They run only when
// BND_LIVE_COOKIES holds the cookies, and only touch /bnd-test.
func liveClient(t *testing.T) *Client {
	cookies := os.Getenv("BND_LIVE_COOKIES")
	if cookies == "" {
		t.Skip("set BND_LIVE_COOKIES to run live tests")
	}
	c, err := New(nil, cookies, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Whoami(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLiveReadOnly(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	t.Logf("uid set: %v", c.UID != 0)
	if uk, err := c.UK(ctx); err != nil || uk == 0 {
		t.Errorf("UK: %v %v", uk, err)
	}
	if q, err := c.Quota(ctx); err != nil || q.Total == 0 {
		t.Errorf("Quota: %+v %v", q, err)
	}
	if fs, err := c.List(ctx, "/"); err != nil || len(fs) == 0 {
		t.Errorf("List: %d %v", len(fs), err)
	}
	if _, err := c.List(ctx, "/bnd-test-definitely-missing"); Code(err) != -9 {
		t.Errorf("List missing: %v", err)
	}
	if _, err := c.Meta(ctx, "/bnd-test-definitely-missing"); Code(err) != 31066 {
		t.Errorf("Meta missing: %v", err)
	}
	r, err := c.Recycled(ctx)
	t.Logf("Recycled: %d items, err=%v", len(r), err)
	s, err := c.Shares(ctx)
	t.Logf("Shares: %d, err=%v", len(s), err)
	o, err := c.OfflineTasks(ctx)
	t.Logf("OfflineTasks: %d, err=%v", len(o), err)
	if strings.Contains(os.Getenv("BND_LIVE"), "verbose") {
		for _, x := range s {
			t.Logf("  share %d %s %v", x.ID, x.Link, x.Paths)
		}
	}
}
