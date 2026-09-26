// Package transfer moves file data: parallel ranged downloads and chunked
// uploads, both resumable. It knows nothing about Baidu's API; callers
// supply URLs and upload functions.
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
	URLs  func(ctx context.Context) ([]string, error)
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
	Size int64  `json:"size"`
	Done []bool `json:"done"` // per chunk
}

// Run downloads into Dest+PartSuffix and renames it to Dest when complete.
// An earlier interrupted run of the same file is resumed.
func (d *Download) Run(ctx context.Context) error {
	part := d.Dest + PartSuffix
	f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(d.Size); err != nil {
		return err
	}
	j := &job{Download: d, f: f, recPath: part + ".json"}
	j.rec = d.loadRecord(j.recPath)
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
	if resp.StatusCode != http.StatusPartialContent && !(resp.StatusCode == http.StatusOK && start == 0 && end == d.Size) {
		return 0, fmt.Errorf("下载 %d-%d: HTTP %s", start, end-1, resp.Status)
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

func (d *Download) chunkLen(i int) int64 {
	return min(chunkSize, d.Size-int64(i)*chunkSize)
}

// loadRecord returns the saved record if it belongs to this file, else a fresh one.
func (d *Download) loadRecord(path string) *record {
	chunks := int((d.Size + chunkSize - 1) / chunkSize)
	var rec record
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &rec) == nil &&
		rec.Size == d.Size && len(rec.Done) == chunks {
		return &rec
	}
	return &record{Size: d.Size, Done: make([]bool, chunks)}
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
