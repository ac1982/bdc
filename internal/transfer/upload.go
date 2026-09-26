package transfer

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/cenkalti/backoff/v5"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/ac1982/bdc/internal/baidu"
)

// Uploader is the part of the Baidu client that uploads use.
type Uploader interface {
	RapidUpload(ctx context.Context, path string, h baidu.Hashes, r io.ReaderAt, overwrite bool) (*baidu.File, error)
	Precreate(ctx context.Context, path string, h baidu.Hashes, overwrite bool, resumeID string) (baidu.Upload, error)
	UploadHost(ctx context.Context) (string, error)
	UploadBlock(ctx context.Context, host, path string, up baidu.Upload, seq int, data []byte) (string, error)
	CreateFile(ctx context.Context, path string, size int64, up baidu.Upload, blocks []string, overwrite bool) (baidu.File, error)
}

// Upload sends one local file to the netdisk.
type Upload struct {
	API       Uploader
	Local     string
	Remote    string // full netdisk path of the file
	Overwrite bool
	Conns     int
	Limit     *rate.Limiter
	Progress  func(n int64)
	// StateDir keeps resume records; an interrupted upload continues from them.
	StateDir string
}

// uploadRecord is the resume state of one upload.
type uploadRecord struct {
	ID     string   `json:"id"`
	Blocks []string `json:"blocks"` // md5 returned per uploaded block, "" if not yet
}

// errSessionExpired is Baidu's "block miss in superfile2": start over.
const errSessionExpired = 31363

// Run uploads the file. rapid reports that Baidu already had the content,
// so nothing was transferred.
func (u *Upload) Run(ctx context.Context) (file baidu.File, rapid bool, err error) {
	f, err := os.Open(u.Local)
	if err != nil {
		return baidu.File{}, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return baidu.File{}, false, err
	}
	h, err := hashFile(ctx, f, fi.Size())
	if err != nil {
		return baidu.File{}, false, err
	}
	recPath := filepath.Join(u.StateDir, "uploads", u.recordKey(fi)+".json")
	for attempt := 0; ; attempt++ {
		file, rapid, err = u.run(ctx, f, h, recPath)
		if baidu.Code(err) != errSessionExpired || attempt > 0 {
			break
		}
		os.Remove(recPath)
	}
	if err == nil {
		os.Remove(recPath)
	}
	return file, rapid, err
}

func (u *Upload) run(ctx context.Context, f *os.File, h baidu.Hashes, recPath string) (baidu.File, bool, error) {
	hit, err := u.API.RapidUpload(ctx, u.Remote, h, f, u.Overwrite)
	if err != nil {
		return baidu.File{}, false, err
	}
	if hit != nil { // Baidu already had the content
		u.progress(h.Size)
		return *hit, true, nil
	}
	rec := loadUploadRecord(recPath, len(h.Blocks))
	up, err := u.API.Precreate(ctx, u.Remote, h, u.Overwrite, rec.ID)
	if err != nil {
		return baidu.File{}, false, err
	}
	if up.ID != rec.ID {
		rec = &uploadRecord{ID: up.ID, Blocks: make([]string, len(h.Blocks))}
	}
	host, err := u.API.UploadHost(ctx)
	if err != nil {
		return baidu.File{}, false, err
	}

	var mu sync.Mutex
	bs := baidu.BlockSize(h.Size)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, u.Conns))
	for i := range h.Blocks {
		off := int64(i) * bs
		n := min(bs, h.Size-off)
		if rec.Blocks[i] != "" {
			u.progress(n)
			continue
		}
		g.Go(func() error {
			data := make([]byte, n)
			if _, err := f.ReadAt(data, off); err != nil {
				return err
			}
			if err := u.wait(gctx, len(data)); err != nil {
				return err
			}
			md, err := backoff.Retry(gctx, func() (string, error) {
				return u.API.UploadBlock(gctx, host, u.Remote, up, i, data)
			}, backoff.WithMaxTries(5))
			if err != nil {
				return err
			}
			u.progress(n)
			mu.Lock()
			defer mu.Unlock()
			rec.Blocks[i] = md
			return saveJSON(recPath, rec)
		})
	}
	if err := g.Wait(); err != nil {
		return baidu.File{}, false, err
	}
	file, err := u.API.CreateFile(ctx, u.Remote, h.Size, up, rec.Blocks, u.Overwrite)
	return file, false, err
}

func (u *Upload) progress(n int64) {
	if u.Progress != nil {
		u.Progress(n)
	}
}

// wait takes n bytes from the rate limiter, in pieces no larger than its burst.
func (u *Upload) wait(ctx context.Context, n int) error {
	if u.Limit == nil {
		return nil
	}
	for n > 0 {
		k := min(n, u.Limit.Burst())
		if err := u.Limit.WaitN(ctx, k); err != nil {
			return err
		}
		n -= k
	}
	return nil
}

// recordKey identifies an upload of this version of the file to this path.
func (u *Upload) recordKey(fi os.FileInfo) string {
	abs, _ := filepath.Abs(u.Local)
	sum := sha1.Sum(fmt.Appendf(nil, "%s\x00%s\x00%d\x00%d", abs, u.Remote, fi.Size(), fi.ModTime().UnixNano()))
	return hex.EncodeToString(sum[:])
}

// hashFile computes what the upload API identifies content by, in one read.
func hashFile(ctx context.Context, r io.Reader, size int64) (baidu.Hashes, error) {
	const sliceLen = 256 << 10
	h := baidu.Hashes{Size: size}
	whole, slice, block := md5.New(), md5.New(), md5.New()
	bs := baidu.BlockSize(size)
	buf := make([]byte, 1<<20)
	var read, inBlock int64
	for {
		if err := ctx.Err(); err != nil {
			return h, err
		}
		n, err := r.Read(buf)
		p := buf[:n]
		whole.Write(p)
		if read < sliceLen {
			slice.Write(p[:min(int64(n), sliceLen-read)])
		}
		for len(p) > 0 {
			k := min(int64(len(p)), bs-inBlock)
			block.Write(p[:k])
			p, inBlock = p[k:], inBlock+k
			if inBlock == bs {
				h.Blocks = append(h.Blocks, hex.EncodeToString(block.Sum(nil)))
				block.Reset()
				inBlock = 0
			}
		}
		read += int64(n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return h, err
		}
	}
	if inBlock > 0 || len(h.Blocks) == 0 {
		h.Blocks = append(h.Blocks, hex.EncodeToString(block.Sum(nil)))
	}
	if read != size {
		return h, fmt.Errorf("文件在读取时被修改 (%d 字节, 应为 %d)", read, size)
	}
	h.ContentMD5 = hex.EncodeToString(whole.Sum(nil))
	h.SliceMD5 = hex.EncodeToString(slice.Sum(nil))
	return h, nil
}

func loadUploadRecord(path string, blocks int) *uploadRecord {
	var rec uploadRecord
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &rec) == nil && len(rec.Blocks) == blocks {
		return &rec
	}
	return &uploadRecord{Blocks: make([]string, blocks)}
}
