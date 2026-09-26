package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/ac1982/bdc/internal/baidu"
	"github.com/ac1982/bdc/internal/config"
	"github.com/ac1982/bdc/internal/transfer"
)

// item is one file of a download or upload and what happened to it.
type item struct {
	Remote string `json:"path,omitempty"`
	Local  string `json:"file,omitempty"`
	Size   int64  `json:"size"`
	Status string `json:"status"`           // downloaded, uploaded, rapid, skipped, failed
	Reason string `json:"reason,omitempty"` // why skipped: exists, same-size
	Error  string `json:"error,omitempty"`

	err       error
	fsID      int64  // download: the remote file
	version   string // download: identifies the remote version, for resuming
	overwrite bool   // upload: replace the existing target
}

// fail marks the item failed.
func (it *item) fail(err error) {
	it.Status, it.Error, it.err = "failed", err.Error(), err
}

// batchResult is the outcome of a download or upload.
type batchResult struct {
	Files   []*item        `json:"files"`
	Summary map[string]int `json:"summary"` // files per status
	upload  bool
	dirs    []string // directories to create before the files go in parallel
	mkdir   func(string) error
	planned map[string]*item // by the key of every file an item will write
}

// add plans an item. An item that would write a file another item already
// claimed fails: two files cannot land in one place. A download also claims
// its part file and resume record.
func (r *batchResult) add(it *item) {
	if r.planned == nil {
		r.planned = map[string]*item{}
	}
	claims := []string{remoteKey(it.Remote)}
	if !r.upload {
		claims = []string{localKey(it.Local), localKey(it.Local + transfer.PartSuffix), localKey(it.Local + transfer.PartSuffix + ".json")}
	}
	for _, k := range claims {
		if other, dup := r.planned[k]; dup {
			it.fail(inputf("%s 与 %s 会写到同一个位置: %s", r.source(it), r.source(other), r.target(other)))
			r.Files = append(r.Files, it)
			return
		}
	}
	for _, k := range claims {
		r.planned[k] = it
	}
	r.Files = append(r.Files, it)
}

// source and target are where an item comes from and goes to.
func (r *batchResult) source(it *item) string {
	if r.upload {
		return it.Local
	}
	return it.Remote
}

func (r *batchResult) target(it *item) string {
	if r.upload {
		return it.Remote
	}
	return it.Local
}

func (r *batchResult) verb() string {
	if r.upload {
		return "上传"
	}
	return "下载"
}

// arrow describes where an item went.
func (r *batchResult) arrow(it *item) string { return r.source(it) + " → " + r.target(it) }

func (r *batchResult) Human(w io.Writer) {
	if len(r.Files) == 0 {
		return
	}
	var total int64
	for _, it := range r.Files {
		if it.Status != "failed" && it.Status != "skipped" {
			total += it.Size
		}
	}
	var counts []string
	for _, s := range []string{"downloaded", "uploaded", "rapid", "skipped", "failed"} {
		if n := r.Summary[s]; n > 0 {
			counts = append(counts, fmt.Sprintf("%s %d", statusName[s], n))
		}
	}
	fmt.Fprintf(w, "%s结束: %s; 传输 %s\n", r.verb(), strings.Join(counts, ", "), size(total))
}

var statusName = map[string]string{
	"downloaded": "已下载", "uploaded": "已上传", "rapid": "秒传", "skipped": "跳过", "failed": "失败",
}

// run transfers each item not decided while planning, the configured number
// of files at a time, showing progress. fn reports bytes to progress. It keeps
// going when a file fails; the error summarises all failures.
func (r *batchResult) run(app *App, fn func(ctx context.Context, it *item, progress func(int64)) error) error {
	var todo []*item
	var total int64
	for _, it := range r.Files {
		if it.Status == "" {
			todo = append(todo, it)
			total += it.Size
		}
	}
	for _, d := range r.dirs {
		if err := r.mkdir(d); err != nil {
			it := &item{Local: d}
			if r.upload {
				it = &item{Remote: d}
			}
			it.fail(err)
			r.Files = append(r.Files, it)
		}
	}
	m := app.newMeter(total, r.verb())
	var g errgroup.Group
	g.SetLimit(max(1, app.cfg.Settings.Parallel))
	for _, it := range todo {
		g.Go(func() error {
			if err := app.ctx.Err(); err != nil { // cancelled: the rest is not started
				it.fail(err)
				return nil
			}
			if err := fn(app.ctx, it, m.add); err != nil {
				it.fail(err)
				m.logf("失败 %s: %v", r.arrow(it), err)
			} else {
				m.logf("%s %s", statusName[it.Status], r.arrow(it))
			}
			return nil
		})
	}
	g.Wait()
	m.done()
	r.Summary = map[string]int{}
	var errs []error
	for _, it := range r.Files {
		r.Summary[it.Status]++
		if it.err != nil {
			errs = append(errs, it.err)
		}
	}
	if len(errs) > 0 {
		return &batchError{total: len(r.Files), errs: errs}
	}
	return nil
}

// deepest drops directories that are ancestors of others in the list;
// parent is path.Dir or filepath.Dir.
func deepest(dirs []string, parent func(string) string) []string {
	ancestor := map[string]bool{}
	for _, d := range dirs {
		for p := parent(d); p != d; d, p = p, parent(p) {
			ancestor[p] = true
		}
	}
	var out []string
	for _, d := range dirs {
		if !ancestor[d] && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

// batchError reports the failed files of a batch; errors.Is and As see each
// file's error, so the batch is classified by them.
type batchError struct {
	total int
	errs  []error
}

func (e *batchError) Error() string {
	return fmt.Sprintf("%d/%d 个文件失败, 第一个: %v", len(e.errs), e.total, e.errs[0])
}

func (e *batchError) Unwrap() []error { return e.errs }

func limiter(bytesPerSec int64) *rate.Limiter {
	if bytesPerSec <= 0 {
		return nil
	}
	return rate.NewLimiter(rate.Limit(bytesPerSec), int(min(bytesPerSec, 1<<20)))
}

// download

type downloadCmd struct {
	Paths     []string `arg:"" help:"网盘中的文件或目录, 可用通配符; 目录会递归下载"`
	SaveTo    string   `name:"saveto" short:"o" type:"path" help:"保存到此目录, 默认为设置中的 save-dir"`
	FullPath  bool     `help:"在保存目录中保留文件在网盘中的完整路径"`
	Overwrite bool     `help:"覆盖本地已存在的文件, 默认跳过"`
	Conns     int      `short:"p" help:"每个文件的连接数, 默认为设置中的 connections"`
}

func (c *downloadCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	s := app.cfg.Settings
	saveTo := c.SaveTo
	if saveTo == "" {
		saveTo = s.SaveDir
	}
	r := &batchResult{Files: []*item{}, mkdir: func(d string) error { return os.MkdirAll(d, 0o755) }}
	for _, arg := range c.Paths { // each argument fails on its own
		paths, err := app.expand(client, arg)
		if err == nil {
			for _, p := range paths {
				if err = c.plan(app.ctx, client, p, saveTo, r); err != nil {
					break
				}
			}
		}
		if err != nil {
			it := &item{Remote: app.abs(arg)}
			it.fail(err)
			r.Files = append(r.Files, it)
		}
	}

	lim := limiter(s.DownloadLimit)
	conns := cmp.Or(c.Conns, s.Connections)
	err = r.run(app, func(ctx context.Context, it *item, progress func(int64)) error {
		if err := os.MkdirAll(filepath.Dir(it.Local), 0o755); err != nil {
			return err
		}
		d := &transfer.Download{
			Client: client.HTTP(), Header: baidu.DownloadHeader(),
			URLs: func(ctx context.Context) ([]string, error) { return client.DownloadURLs(ctx, it.fsID) },
			ID:   it.version, Size: it.Size, Dest: it.Local, Conns: conns, Limit: lim, Progress: progress,
		}
		if err := d.Run(ctx); errors.Is(err, transfer.ErrOccupied) {
			return withKind(Input, err)
		} else if err != nil {
			return err
		}
		it.Status = "downloaded"
		return nil
	})
	return r, err
}

// plan adds the files under remote path p, marking those already on disk.
// Nothing is written yet; an error means p could not be planned at all.
func (c *downloadCmd) plan(ctx context.Context, client *baidu.Client, p, saveTo string, r *batchResult) error {
	root, err := client.Meta(ctx, p)
	if err != nil {
		return err
	}
	// Directories are created with the files in them; only empty ones need
	// creating on their own.
	var empty []string
	emptyDest := map[string]string{}
	visit := func(f baidu.File) error {
		delete(emptyDest, path.Dir(f.Path)) // the parent has something in it
		rel := f.Path
		if !c.FullPath {
			rel = strings.TrimPrefix(f.Path, path.Dir(root.Path))
		}
		dest, err := filepath.Abs(filepath.Join(saveTo, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if f.IsDir {
			empty = append(empty, f.Path)
			emptyDest[f.Path] = dest
			return nil
		}
		it := &item{Remote: f.Path, Local: dest, Size: f.Size, fsID: f.FsID, version: fmt.Sprint(f.FsID, "@", f.Mtime.Unix())}
		if _, err := os.Stat(dest); err == nil && !c.Overwrite {
			it.Status, it.Reason = "skipped", "exists"
		}
		r.add(it)
		return nil
	}
	if err := visit(root); err != nil || !root.IsDir {
		return err
	}
	if err := client.Walk(ctx, root.Path, visit); err != nil {
		return err
	}
	for _, d := range empty {
		if dest, ok := emptyDest[d]; ok {
			r.dirs = append(r.dirs, dest)
		}
	}
	return nil
}

// upload

type uploadCmd struct {
	Args   []string `arg:"" name:"local… dir" help:"本地文件或目录 (目录递归上传), 最后是网盘中的目标目录"`
	Policy string   `enum:"skip,overwrite,rsync" default:"skip" help:"目标已存在时: skip 跳过, overwrite 覆盖, rsync 大小相同才跳过"`
	Conns  int      `short:"p" help:"每个文件同时上传的分块数, 默认为设置中的 connections"`
}

func (c *uploadCmd) Run(app *App) (Result, error) {
	if len(c.Args) < 2 {
		return nil, usagef("需要本地文件和网盘目录")
	}
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	s := app.cfg.Settings
	dir := app.abs(c.Args[len(c.Args)-1])
	r := &batchResult{Files: []*item{}, upload: true, mkdir: func(d string) error {
		_, _, err := client.EnsureDir(app.ctx, d)
		return err
	}}
	for _, local := range c.Args[:len(c.Args)-1] {
		planUpload(local, dir, r)
	}
	c.checkExisting(app.ctx, client, r.Files)

	// Make the files' directories first: files that create the same new parent
	// at once make Baidu fail one of them (-8). Best effort; committing a file
	// creates its parents anyway, and reports its own failure.
	var parents []string
	for _, it := range r.Files {
		if it.Status == "" {
			parents = append(parents, path.Dir(it.Remote))
		}
	}
	for _, d := range deepest(parents, path.Dir) {
		client.Mkdir(app.ctx, d)
	}

	lim := limiter(s.UploadLimit)
	conns := cmp.Or(c.Conns, s.Connections)
	stateDir, _ := config.Dir()
	err = r.run(app, func(ctx context.Context, it *item, progress func(int64)) error {
		u := &transfer.Upload{
			API: client, Local: it.Local, Remote: it.Remote, Overwrite: it.overwrite,
			Conns: conns, Limit: lim, Progress: progress, StateDir: stateDir,
		}
		_, rapid, err := u.Run(ctx)
		if err != nil {
			return err
		}
		it.Status = "uploaded"
		if rapid {
			it.Status = "rapid"
		}
		return nil
	})
	return r, err
}

// planUpload adds the files of a local file or directory, uploaded under
// dir. What cannot be read is added as failed; symlinked directories are
// skipped rather than followed (they may loop).
func planUpload(local, dir string, r *batchResult) {
	fail := func(local string, err error) {
		it := &item{Local: local}
		it.fail(err)
		r.Files = append(r.Files, it)
	}
	abs, err := filepath.Abs(local)
	if err != nil {
		fail(local, err)
		return
	}
	fi, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		fail(abs, inputf("本地文件不存在: %s", local))
		return
	}
	if err != nil {
		fail(abs, err)
		return
	}
	if !fi.IsDir() {
		r.add(&item{Local: abs, Remote: path.Join(dir, fi.Name()), Size: fi.Size()})
		return
	}
	base := path.Join(dir, fi.Name())
	filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			fail(p, err)
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
		if d.IsDir() {
			if entries, err := os.ReadDir(p); err == nil && len(entries) == 0 {
				r.dirs = append(r.dirs, path.Join(base, filepath.ToSlash(rel))) // empty: nothing else creates it
			}
			return nil
		}
		remote := path.Join(base, filepath.ToSlash(rel))
		info, err := os.Stat(p) // follows symlinks
		switch {
		case err != nil:
			fail(p, err)
		case info.IsDir():
			r.Files = append(r.Files, &item{Local: p, Remote: remote, Status: "skipped", Reason: "symlink"})
		default:
			r.add(&item{Local: p, Remote: remote, Size: info.Size()})
		}
		return nil
	})
}

// checkExisting applies the policy to targets that already exist, listing
// each target directory once. Items whose directory cannot be listed fail.
func (c *uploadCmd) checkExisting(ctx context.Context, client *baidu.Client, items []*item) {
	type listing struct {
		entries map[string]baidu.File
		err     error
	}
	dirs := map[string]listing{}
	for _, it := range items {
		if it.Status != "" {
			continue
		}
		dir := path.Dir(it.Remote)
		l, ok := dirs[remoteKey(dir)]
		if !ok {
			files, err := client.List(ctx, dir)
			if errors.Is(err, baidu.ErrNotFound) {
				err = nil
			}
			l = listing{entries: map[string]baidu.File{}, err: err}
			for _, f := range files {
				l.entries[remoteKey(f.Name)] = f
			}
			dirs[remoteKey(dir)] = l
		}
		f, exists := l.entries[remoteKey(path.Base(it.Remote))]
		switch {
		case l.err != nil:
			it.fail(l.err)
		case !exists:
		case f.IsDir:
			it.fail(inputf("网盘中已有同名目录: %s", it.Remote))
		case c.Policy == "overwrite", c.Policy == "rsync" && f.Size != it.Size:
			it.overwrite = true
		case c.Policy == "rsync":
			it.Status, it.Reason = "skipped", "same-size"
		default:
			it.Status, it.Reason = "skipped", "exists"
		}
	}
}
