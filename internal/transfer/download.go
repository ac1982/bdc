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

// record is the resume state saved beside the part file. Owner marks it as
// bnd's, so a user's file that happens to have the same name is never taken
// for a download in progress.
type record struct {
	Owner string `json:"owner"`
	ID    string `json:"id"`
	Size  int64  `json:"size"`
	Done  []bool `json:"done"` // per chunk
}

const recordOwner = "bnd download"

// ErrOccupied means a file bnd did not create sits where the download keeps
// its part file or resume record; bnd will not overwrite it.
var ErrOccupied = errors.New("文件已被占用")

// Run downloads into Dest+PartSuffix and renames it to Dest when complete.
// An earlier interrupted run of the same file is resumed.
func (d *Download) Run(ctx context.Context) error {
	part := d.Dest + PartSuffix
	j := &job{Download: d, recPath: part + ".json"}
	f, rec, err := d.open(part, j.recPath)
	if err != nil {
		return err
	}
	defer f.Close()
	j.rec = rec
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
		start += n // keep what arrived before a connection broke
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
	switch {
	case err != nil: // a broken connection: what arrived is kept, the rest is retried
	case start+n < end:
		err = io.ErrUnexpectedEOF
	default:
		if err = expectEOF(resp.Body); err != nil {
			// The whole response is suspect, not just its tail: none of it counts.
			return 0, backoff.Permanent(fmt.Errorf("下载 %d-%d: %w", start, end-1, err))
		}
	}
	return n, err
}

// expectEOF checks that r has nothing left.
func expectEOF(r io.Reader) error {
	n, err := r.Read(make([]byte, 1))
	switch {
	case n > 0:
		return errors.New("服务器返回的内容比请求的长")
	case err == io.EOF:
		return nil
	case err == nil:
		return errors.New("服务器的响应没有结束")
	}
	return err
}

// checkRange accepts only a response carrying exactly bytes [start, end) of
// a file of the expected size.
func (d *Download) checkRange(resp *http.Response, start, end int64) error {
	switch resp.StatusCode {
	case http.StatusPartialContent:
		var a, b, total int64
		if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &a, &b, &total); err != nil ||
			a != start || b != end-1 || total != d.Size {
			// Other content than asked for: retrying will not help.
			return backoff.Permanent(fmt.Errorf("下载 %d-%d: 服务器返回了不符的区间 %q", start, end-1, resp.Header.Get("Content-Range")))
		}
	case http.StatusOK: // the whole file, when that is what was asked for
		if start != 0 || end != d.Size || (resp.ContentLength >= 0 && resp.ContentLength != d.Size) {
			return backoff.Permanent(fmt.Errorf("下载 %d-%d: 服务器返回了整个文件 (%d 字节)", start, end-1, resp.ContentLength))
		}
	default: // this link failed; another may work
		return fmt.Errorf("下载 %d-%d: HTTP %s", start, end-1, resp.Status)
	}
	return nil
}

func (d *Download) chunks() int { return int((d.Size + chunkSize - 1) / chunkSize) }

func (d *Download) chunkLen(i int) int64 {
	return min(chunkSize, d.Size-int64(i)*chunkSize)
}

// open returns the part file and its record: bnd's own intact part of this
// version of the file to resume, or else a new one. Files bnd did not create
// are never overwritten.
func (d *Download) open(part, recPath string) (*os.File, *record, error) {
	var rec record
	data, recErr := os.ReadFile(recPath)
	ours := recErr == nil && json.Unmarshal(data, &rec) == nil && rec.Owner == recordOwner
	fi, partErr := os.Stat(part)
	switch {
	case ours && rec.ID == d.ID && rec.Size == d.Size && len(rec.Done) == d.chunks() && partErr == nil && fi.Size() == d.Size:
		f, err := os.OpenFile(part, os.O_RDWR, 0)
		return f, &rec, err
	case !ours && (recErr == nil || partErr == nil):
		occupied := recPath
		if partErr == nil {
			occupied = part
		}
		return nil, nil, fmt.Errorf("%w: %s 不是 bnd 的下载记录, 请移走或删除它", ErrOccupied, occupied)
	}
	// Start over. The record is written before the part is created, so a part
	// without bnd's record is never bnd's.
	if ours {
		os.Remove(part)
	}
	rec = record{Owner: recordOwner, ID: d.ID, Size: d.Size, Done: make([]bool, d.chunks())}
	if err := saveJSON(recPath, rec); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return f, &rec, f.Truncate(d.Size)
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
	// A unique temporary name: a fixed one could be someone else's file.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bnd-*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}
