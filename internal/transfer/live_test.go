package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
)

// TestLive exercises the write paths against Baidu with a real account, only
// under /bnd-test. It runs when BND_LIVE_COOKIES holds the cookies.
func TestLive(t *testing.T) {
	cookies := os.Getenv("BND_LIVE_COOKIES")
	if cookies == "" {
		t.Skip("set BND_LIVE_COOKIES to run live tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := baidu.New(nil, cookies, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Whoami(ctx); err != nil {
		t.Fatal(err)
	}
	root := fmt.Sprintf("/bnd-test/live-%d", time.Now().UnixNano())
	c.Remove(ctx, root)
	t.Cleanup(func() { c.Remove(context.Background(), root) })

	if _, err := c.Mkdir(ctx, root); err != nil {
		t.Fatal("mkdir:", err)
	}
	if _, err := c.Mkdir(ctx, root); !errors.Is(err, baidu.ErrExists) {
		t.Fatal("mkdir again:", err)
	}

	// A multi-block upload with unique content, so no instant upload.
	content := payload(9<<20 + 123)
	content = append(content, []byte(time.Now().String())...)
	local := writeTemp(t, content)
	up := &Upload{API: c, Local: local, Remote: root + "/big.bin", Conns: 3, StateDir: t.TempDir()}
	f, rapid, err := up.Run(ctx)
	if err != nil || rapid || f.Size != int64(len(content)) {
		t.Fatal("upload:", f, rapid, err)
	}
	// A single-block file uploaded twice: the second time is instant. (After a
	// multi-block upload Baidu stores md5(block list), so those never match.)
	small := writeTemp(t, []byte("bnd live "+time.Now().String()))
	for i, want := range []bool{false, true} {
		su := &Upload{API: c, Local: small, Remote: root + "/small" + string(rune('0'+i)), StateDir: t.TempDir()}
		if _, rapid, err := su.Run(ctx); err != nil || rapid != want {
			t.Fatal("small upload", i, rapid, err)
		}
	}
	// Existing path without overwrite must fail, not rename.
	up.Remote = root + "/big.bin"
	if _, _, err := up.Run(ctx); err == nil {
		list, _ := c.List(ctx, root)
		t.Errorf("upload onto an existing file succeeded; dir now: %v", names(list))
	} else {
		t.Logf("upload onto existing file: %v (code %d)", err, baidu.Code(err))
	}
	up.Overwrite = true
	if _, _, err := up.Run(ctx); err != nil {
		t.Error("overwrite:", err)
	}

	// Zero-byte file.
	empty := &Upload{API: c, Local: writeTemp(t, nil), Remote: root + "/empty.txt", StateDir: t.TempDir()}
	_, rapid, err = empty.Run(ctx)
	t.Logf("zero-byte upload: rapid=%v err=%v", rapid, err)

	// Download with several connections and compare.
	dest := filepath.Join(t.TempDir(), "big.bin")
	d := &Download{Client: c.HTTP(), Header: baidu.DownloadHeader(), Size: int64(len(content)), Dest: dest, Conns: 4,
		URLs: func(ctx context.Context) ([]string, error) { return c.DownloadURLs(ctx, root+"/big.bin") }}
	if err := d.Run(ctx); err != nil {
		t.Fatal("download:", err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, content) {
		t.Fatal("downloaded content differs")
	}

	// Copy, move, meta.
	if _, err := c.Copy(ctx, baidu.Rename{From: root + "/big.bin", To: root + "/copy.bin"}); err != nil {
		t.Error("copy:", err)
	}
	if _, err := c.Move(ctx, baidu.Rename{From: root + "/copy.bin", To: root + "/sub/moved.bin"}); err != nil {
		t.Error("move into a missing dir:", err)
	}
	if m, err := c.Meta(ctx, root+"/sub/moved.bin"); err != nil || m.Size != int64(len(content)) {
		t.Error("meta:", m, err)
	}

	// Share, then try to save our own share (Baidu refuses with errno 2).
	s, err := c.CreateShare(ctx, "bndt", 1, root+"/big.bin")
	if err != nil {
		t.Fatal("share:", err)
	}
	t.Logf("share: id=%d expires=%v", s.ID, s.Expires)
	link, _ := baidu.ParseShareLink(s.Link, "bndt")
	saved, err := c.SaveShare(ctx, link, root+"/sub")
	t.Logf("save own share: saved=%v err=%v", saved, err)
	bad, _ := baidu.ParseShareLink(s.Link, "zzzz")
	if _, err := c.SaveShare(ctx, bad, root); baidu.Code(err) != -12 {
		t.Errorf("wrong code: %v", err)
	}
	if err := c.CancelShares(ctx, s.ID); err != nil {
		t.Error("cancel share:", err)
	}

	if _, err := c.Remove(ctx, root+"/sub", root+"/small1"); err != nil {
		t.Error("remove:", err)
	}
	if _, err := c.Remove(ctx, root+"/missing"); !errors.Is(err, baidu.ErrNotFound) {
		t.Error("remove missing:", err)
	}
}

func names(fs []baidu.File) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}
