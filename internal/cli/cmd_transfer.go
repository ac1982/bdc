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
	"strings"

	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
	"github.com/ac1982/baidunetdisk-cli/internal/config"
	"github.com/ac1982/baidunetdisk-cli/internal/transfer"
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
	dirs    []string         // download: local directories to create, for empty remote ones
	planned map[string]*item // by target
}

// add plans an item. A second item for a target already planned fails:
// two files cannot land in one place.
func (r *batchResult) add(it *item) {
	if r.planned == nil {
		r.planned = map[string]*item{}
	}
	if other, dup := r.planned[r.target(it)]; dup {
		it.fail(inputf("%s 与 %s 的目标相同: %s", r.source(it), r.source(other), r.target(it)))
	} else {
		r.planned[r.target(it)] = it
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
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	m := app.newMeter(total, r.verb())
	var g errgroup.Group
	g.SetLimit(max(1, app.cfg.Settings.Parallel))
	for _, it := range todo {
		g.Go(func() error {
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
	r := &batchResult{Files: []*item{}}
	paths, err := app.expand(client, c.Paths...)
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		if err := c.plan(app.ctx, client, p, saveTo, r); err != nil {
			it := &item{Remote: p}
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
			URLs: func(ctx context.Context) ([]string, error) { return client.DownloadURLs(ctx, it.Remote) },
			ID:   it.version, Size: it.Size, Dest: it.Local, Conns: conns, Limit: lim, Progress: progress,
		}
		if err := d.Run(ctx); err != nil {
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
	visit := func(f baidu.File) error {
		rel := f.Path
		if !c.FullPath {
			rel = strings.TrimPrefix(f.Path, path.Dir(root.Path))
		}
		dest, err := filepath.Abs(filepath.Join(saveTo, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if f.IsDir {
			r.dirs = append(r.dirs, dest)
			return nil
		}
		it := &item{Remote: f.Path, Local: dest, Size: f.Size, version: fmt.Sprint(f.FsID, "@", f.Mtime.Unix())}
		if _, err := os.Stat(dest); err == nil && !c.Overwrite {
			it.Status, it.Reason = "skipped", "exists"
		}
		r.add(it)
		return nil
	}
	if err := visit(root); err != nil || !root.IsDir {
		return err
	}
	return client.Walk(ctx, root.Path, visit)
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
	r := &batchResult{Files: []*item{}, upload: true}
	for _, local := range c.Args[:len(c.Args)-1] {
		planUpload(local, dir, r)
	}
	c.checkExisting(app.ctx, client, r.Files)

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
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
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
		l, ok := dirs[dir]
		if !ok {
			files, err := client.List(ctx, dir)
			if errors.Is(err, baidu.ErrNotFound) {
				err = nil
			}
			l = listing{entries: map[string]baidu.File{}, err: err}
			for _, f := range files {
				l.entries[f.Name] = f
			}
			dirs[dir] = l
		}
		f, exists := l.entries[path.Base(it.Remote)]
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
