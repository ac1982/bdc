// Package transfer moves file data between disk and netdisk: parallel ranged
// downloads and chunked uploads, both resumable. The downloader is generic
// (callers supply the links); uploads speak Baidu's upload protocol through
// the Uploader interface.
package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// PartSuffix marks a download in progress; its resume record is PartSuffix+".json".
const PartSuffix = ".bnd-part"

const chunkSize = 4 << 20

// Download fetches one remote file with several ranged connections.
type Download struct {
	Client *http.Client
	Header http.Header
	// URLs returns candidate links for the file. It is called again when all
	// links fail, since they expire.
	URLs func(ctx context.Context) ([]string, error)
	// ID identifies this version of the remote file (e.g. fs id and mtime);
	// a leftover part of a different version is not resumed.
	ID    string
	Size  int64
	Dest  string
	Conns int
	// Limit, if set, caps the speed; share one limiter to cap all downloads.
	Limit *rate.Limiter
	// Progress, if set, is called with the number of bytes just written.
	Progress func(n int64)
}

// record is the resume state saved beside the part file.
type record struct {
	ID   string `json:"id"`
	Size int64  `json:"size"`
	Done []bool `json:"done"` // per chunk
}

// Run downloads into Dest+PartSuffix and renames it to Dest when complete.
// An earlier interrupted run of the same file is resumed.
func (d *Download) Run(ctx context.Context) error {
	part := d.Dest + PartSuffix
	j := &job{Download: d, recPath: part + ".json"}
	j.rec = d.resumable(part, j.recPath)
	flags := os.O_RDWR | os.O_CREATE
	if j.rec == nil {
		// Start over; drop the old record first so a crash cannot pair it with the new part.
		if err := os.Remove(j.recPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		j.rec = &record{ID: d.ID, Size: d.Size, Done: make([]bool, d.chunks())}
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(d.Size); err != nil {
		return err
	}
	j.f = f
	for i, done := range j.rec.Done {
		if done && d.Progress != nil {
			d.Progress(d.chunkLen(i))
		}
	}
	if d.Size > 0 {
		if _, err := j.refresh(ctx); err != nil {
			return err
		}
	}

	g, gctx := errgroup.WithContext(ctx)
	for w := range max(1, d.Conns) {
		g.Go(func() error {
			for i := j.take(); i >= 0; i = j.take() {
				if err := j.fetchChunk(gctx, i, w); err != nil {
					return err
				}
				if err := j.finish(i); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	os.Remove(j.recPath)
	return os.Rename(part, d.Dest)
}

// job is the shared state of one running download.
type job struct {
	*Download
	f       *os.File
	recPath string

	mu   sync.Mutex
	rec  *record
	next int      // first chunk not yet handed out
	urls []string // current links
}

// take hands out the next unfinished chunk, or -1 when none are left.
func (j *job) take() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	for ; j.next < len(j.rec.Done); j.next++ {
		if !j.rec.Done[j.next] {
			j.next++
			return j.next - 1
		}
	}
	return -1
}

// finish records chunk i as done.
func (j *job) finish(i int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.rec.Done[i] = true
	return saveJSON(j.recPath, j.rec)
}

func (j *job) links() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.urls
}

// refresh asks for fresh links; they expire.
func (j *job) refresh(ctx context.Context) ([]string, error) {
	urls, err := j.URLs(ctx)
	if err == nil && len(urls) == 0 {
		err = errors.New("没有可用的下载链接")
	}
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	j.urls = urls
	j.mu.Unlock()
	return urls, nil
}

// fetchChunk downloads chunk i, trying the links in turn (each worker starts
// at a different one) and refreshing them after all have failed.
func (j *job) fetchChunk(ctx context.Context, i, worker int) error {
	start := int64(i) * chunkSize
	end := start + j.chunkLen(i)
	attempt := 0
	op := func() (struct{}, error) {
		links := j.links()
		if attempt > 0 && attempt%len(links) == 0 {
			var err error
			if links, err = j.refresh(ctx); err != nil {
				return struct{}{}, err
			}
		}
		link := links[(worker+attempt)%len(links)]
		attempt++
		n, err := j.fetchRange(ctx, j.f, link, start, end)
		start += n // keep what arrived before a failure
		return struct{}{}, err
	}
	_, err := backoff.Retry(ctx, op, backoff.WithMaxTries(10),
		backoff.WithBackOff(&backoff.ExponentialBackOff{InitialInterval: time.Second, Multiplier: 2, MaxInterval: 30 * time.Second}))
	return err
}

// fetchRange writes bytes [start, end) of the link at their offset in f and
// returns how many were written.
func (d *Download) fetchRange(ctx context.Context, f *os.File, link string, start, end int64) (int64, error) {
	if start >= end {
		return 0, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return 0, backoff.Permanent(err)
	}
	for k, v := range d.Header {
		req.Header[k] = v
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
	resp, err := d.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, backoff.Permanent(ctx.Err())
		}
		return 0, err
	}
	defer resp.Body.Close()
	if err := d.checkRange(resp, start, end); err != nil {
		return 0, err
	}
	var body io.Reader = io.LimitReader(resp.Body, end-start)
	if d.Limit != nil {
		body = &limitedReader{ctx: ctx, r: body, lim: d.Limit}
	}
	w := &offsetWriter{f: f, off: start, progress: d.Progress}
	n, err := io.Copy(w, body)
	if err == nil && start+n < end {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

// checkRange accepts only a response carrying exactly bytes [start, end) of
// a file of the expected size.
func (d *Download) checkRange(resp *http.Response, start, end int64) error {
	switch resp.StatusCode {
	case http.StatusPartialContent:
		var a, b, total int64
		if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &a, &b, &total); err != nil ||
			a != start || b != end-1 || total != d.Size {
			return fmt.Errorf("下载 %d-%d: 服务器返回了不符的区间 %q", start, end-1, resp.Header.Get("Content-Range"))
		}
	case http.StatusOK: // the whole file, when that is what was asked for
		if start != 0 || end != d.Size || (resp.ContentLength >= 0 && resp.ContentLength != d.Size) {
			return fmt.Errorf("下载 %d-%d: 服务器返回了整个文件 (%d 字节)", start, end-1, resp.ContentLength)
		}
	default:
		return fmt.Errorf("下载 %d-%d: HTTP %s", start, end-1, resp.Status)
	}
	return nil
}

func (d *Download) chunks() int { return int((d.Size + chunkSize - 1) / chunkSize) }

func (d *Download) chunkLen(i int) int64 {
	return min(chunkSize, d.Size-int64(i)*chunkSize)
}

// resumable returns the saved record if it belongs to this version of the
// file and its part file is intact, or nil to start over.
func (d *Download) resumable(part, recPath string) *record {
	var rec record
	data, err := os.ReadFile(recPath)
	if err != nil || json.Unmarshal(data, &rec) != nil ||
		rec.ID != d.ID || rec.Size != d.Size || len(rec.Done) != d.chunks() {
		return nil
	}
	if fi, err := os.Stat(part); err != nil || fi.Size() != d.Size {
		return nil
	}
	return &rec
}

type offsetWriter struct {
	f        *os.File
	off      int64
	progress func(int64)
}

func (w *offsetWriter) Write(p []byte) (int, error) {
	n, err := w.f.WriteAt(p, w.off)
	w.off += int64(n)
	if w.progress != nil && n > 0 {
		w.progress(int64(n))
	}
	return n, err
}

type limitedReader struct {
	ctx context.Context
	r   io.Reader
	lim *rate.Limiter
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if b := l.lim.Burst(); len(p) > b {
		p = p[:b]
	}
	n, err := l.r.Read(p)
	if n > 0 {
		if werr := l.lim.WaitN(l.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}

// saveJSON writes v to path atomically, creating the directory.
func saveJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
