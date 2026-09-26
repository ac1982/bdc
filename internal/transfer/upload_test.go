package transfer

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
)

// fakeUploader stores blocks in memory and assembles the file on create.
type fakeUploader struct {
	mu       sync.Mutex
	rapid    bool
	expireAt int // CreateFile fails with "session expired" this many times
	blocks   map[int][]byte
	uploads  int
	created  []byte
}

func (f *fakeUploader) RapidUpload(_ context.Context, p string, h baidu.Hashes, _ io.ReaderAt, _ bool) (*baidu.File, error) {
	if f.rapid {
		return &baidu.File{Path: p, Size: h.Size}, nil
	}
	return nil, nil
}

func (f *fakeUploader) Precreate(_ context.Context, p string, h baidu.Hashes, _ bool, resume string) (baidu.Upload, error) {
	if resume != "" {
		return baidu.Upload{ID: resume}, nil
	}
	return baidu.Upload{ID: "new"}, nil
}

func (f *fakeUploader) UploadHost(context.Context) (string, error) { return "host", nil }

func (f *fakeUploader) UploadBlock(_ context.Context, _, _ string, _ baidu.Upload, seq int, data []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads++
	f.blocks[seq] = append([]byte(nil), data...)
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:]), nil
}

func (f *fakeUploader) CreateFile(_ context.Context, p string, size int64, _ baidu.Upload, blocks []string, _ bool) (baidu.File, error) {
	if f.expireAt > 0 {
		f.expireAt--
		f.blocks = map[int][]byte{}
		return baidu.File{}, &baidu.Error{Code: errSessionExpired}
	}
	f.created = nil
	for i := range blocks {
		f.created = append(f.created, f.blocks[i]...)
	}
	return baidu.File{Path: p, Size: size}, nil
}

func writeTemp(t *testing.T, content []byte) string {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUpload(t *testing.T) {
	content := payload(2*(4<<20) + 100) // three blocks
	api := &fakeUploader{blocks: map[int][]byte{}}
	var progress int64
	var mu sync.Mutex
	u := &Upload{API: api, Local: writeTemp(t, content), Remote: "/r/f", Conns: 2, StateDir: t.TempDir(),
		Progress: func(n int64) { mu.Lock(); progress += n; mu.Unlock() }}
	f, rapid, err := u.Run(context.Background())
	if err != nil || rapid || f.Size != int64(len(content)) {
		t.Fatal(f, rapid, err)
	}
	if string(api.created) != string(content) {
		t.Fatal("assembled content differs")
	}
	if progress != int64(len(content)) {
		t.Fatalf("progress %d", progress)
	}
}

func TestUploadRapid(t *testing.T) {
	api := &fakeUploader{rapid: true}
	u := &Upload{API: api, Local: writeTemp(t, []byte("hello")), Remote: "/r/f", StateDir: t.TempDir()}
	if _, rapid, err := u.Run(context.Background()); err != nil || !rapid {
		t.Fatal(rapid, err)
	}
}

func TestUploadResume(t *testing.T) {
	content := payload(2*(4<<20) + 100)
	local := writeTemp(t, content)
	api := &fakeUploader{blocks: map[int][]byte{0: content[:4<<20], 1: content[4<<20 : 8<<20]}}
	u := &Upload{API: api, Local: local, Remote: "/r/f", StateDir: t.TempDir()}
	fi, _ := os.Stat(local)
	saveJSON(filepath.Join(u.StateDir, "uploads", u.recordKey(fi)+".json"), uploadRecord{ID: "old", Blocks: []string{"m0", "m1", ""}})
	if _, _, err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.uploads != 1 {
		t.Fatalf("%d blocks uploaded, want 1", api.uploads)
	}
	if string(api.created) != string(content) {
		t.Fatal("assembled content differs")
	}
}

func TestUploadRestartsExpiredSession(t *testing.T) {
	content := payload(100)
	api := &fakeUploader{blocks: map[int][]byte{}, expireAt: 1}
	u := &Upload{API: api, Local: writeTemp(t, content), Remote: "/r/f", StateDir: t.TempDir()}
	if _, _, err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(api.created) != string(content) || api.uploads != 2 {
		t.Fatalf("uploads %d", api.uploads)
	}
}

func TestHashFile(t *testing.T) {
	content := payload(4<<20 + 5)
	h, err := hashFile(context.Background(), bytesReader(content), int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	sum := func(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }
	if h.ContentMD5 != sum(content) || h.SliceMD5 != sum(content[:256<<10]) ||
		len(h.Blocks) != 2 || h.Blocks[0] != sum(content[:4<<20]) || h.Blocks[1] != sum(content[4<<20:]) {
		t.Fatalf("%+v", h)
	}
	empty, _ := hashFile(context.Background(), bytesReader(nil), 0)
	if len(empty.Blocks) != 1 || empty.ContentMD5 != sum(nil) {
		t.Fatalf("%+v", empty)
	}
}

// Hashing stops when the upload is cancelled.
func TestHashCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hashFile(ctx, bytesReader(make([]byte, 8<<20)), 8<<20); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
