package cli

import (
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
	Remote string `json:"path"`
	Local  string `json:"file"`
	Size   int64  `json:"size"`
	Status string `json:"status"`           // downloaded, uploaded, rapid, skipped, failed
	Reason string `json:"reason,omitempty"` // why skipped: exists, same-size
	Error  string `json:"error,omitempty"`

	err       error
	overwrite bool // upload: replace the existing target
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
}

func (r *batchResult) verb() string {
	if r.upload {
		return "上传"
	}
	return "下载"
}

// arrow describes where an item went: local → remote for uploads, and the
// other way round for downloads.
func (r *batchResult) arrow(it *item) string {
	if r.upload {
		return it.Local + " → " + it.Remote
	}
	return it.Remote + " → " + it.Local
}

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
	fmt.Fprintf(w, "%s结束: ", r.verb())
	first := true
	for _, s := range []string{"downloaded", "uploaded", "rapid", "skipped", "failed"} {
		if n := r.Summary[s]; n > 0 || (s == "failed" && first) {
			if !first {
				fmt.Fprint(w, ", ")
			}
			fmt.Fprintf(w, "%s %d", statusName[s], n)
			first = false
		}
	}
	fmt.Fprintf(w, "; 传输 %s\n", size(total))
}

var statusName = map[string]string{
	"downloaded": "已下载", "uploaded": "已上传", "rapid": "秒传", "skipped": "跳过", "failed": "失败",
}

// run executes fn for each undecided item, parallel files at a time. It
// keeps going when a file fails; the error summarises all failures.
func (r *batchResult) run(ctx context.Context, parallel int, m *meter, fn func(context.Context, *item) error) error {
	var g errgroup.Group
	g.SetLimit(max(1, parallel))
	for _, it := range r.Files {
		if it.Status != "" {
			continue // decided while planning
		}
		g.Go(func() error {
			if err := fn(ctx, it); err != nil {
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
	var total int64
	for _, p := range paths {
		err := c.plan(app.ctx, client, p, saveTo, func(it *item) {
			total += it.Size
			r.Files = append(r.Files, it)
		})
		if err != nil {
			return r, err
		}
	}

	m := app.newMeter(total, "下载")
	lim := limiter(s.DownloadLimit)
	conns := c.Conns
	if conns <= 0 {
		conns = s.Connections
	}
	err = r.run(app.ctx, s.Parallel, m, func(ctx context.Context, it *item) error {
		if err := os.MkdirAll(filepath.Dir(it.Local), 0o755); err != nil {
			return err
		}
		d := &transfer.Download{
			Client: client.HTTP(), Header: baidu.DownloadHeader(),
			URLs: func(ctx context.Context) ([]string, error) { return client.DownloadURLs(ctx, it.Remote) },
			Size: it.Size, Dest: it.Local, Conns: conns, Limit: lim, Progress: m.add,
		}
		if err := d.Run(ctx); err != nil {
			return err
		}
		it.Status = "downloaded"
		return nil
	})
	return r, err
}

// plan adds the files under remote path p, marking those already present.
func (c *downloadCmd) plan(ctx context.Context, client *baidu.Client, p, saveTo string, add func(*item)) error {
	root, err := client.Meta(ctx, p)
	if err != nil {
		return err
	}
	local := func(f baidu.File) string {
		rel := f.Path
		if !c.FullPath {
			rel = strings.TrimPrefix(f.Path, path.Dir(root.Path))
		}
		return filepath.Join(saveTo, filepath.FromSlash(rel))
	}
	visit := func(f baidu.File) error {
		dest, _ := filepath.Abs(local(f))
		if f.IsDir {
			return os.MkdirAll(dest, 0o755)
		}
		it := &item{Remote: f.Path, Local: dest, Size: f.Size}
		if _, err := os.Stat(dest); err == nil && !c.Overwrite {
			it.Status, it.Reason = "skipped", "exists"
		}
		add(it)
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
	var total int64
	for _, local := range c.Args[:len(c.Args)-1] {
		err := planUpload(local, dir, func(it *item) {
			total += it.Size
			r.Files = append(r.Files, it)
		})
		if err != nil {
			return nil, err
		}
	}
	if err := c.checkExisting(app.ctx, client, r.Files); err != nil {
		return r, err
	}

	m := app.newMeter(total, "上传")
	lim := limiter(s.UploadLimit)
	conns := c.Conns
	if conns <= 0 {
		conns = min(s.Connections, 4)
	}
	stateDir, _ := config.Dir()
	err = r.run(app.ctx, s.Parallel, m, func(ctx context.Context, it *item) error {
		u := &transfer.Upload{
			API: client, Local: it.Local, Remote: it.Remote, Overwrite: it.overwrite,
			Conns: conns, Limit: lim, Progress: m.add, StateDir: stateDir,
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

// planUpload adds the files of a local file or directory, uploaded under dir.
func planUpload(local, dir string, add func(*item)) error {
	abs, err := filepath.Abs(local)
	if err != nil {
		return err
	}
	fi, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return inputf("本地文件不存在: %s", local)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		add(&item{Local: abs, Remote: path.Join(dir, fi.Name()), Size: fi.Size()})
		return nil
	}
	base := path.Join(dir, fi.Name())
	return filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return withKind(Input, err)
		}
		if d.IsDir() {
			return nil
		}
		info, err := os.Stat(p) // follows symlinks
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
		add(&item{Local: p, Remote: path.Join(base, filepath.ToSlash(rel)), Size: info.Size()})
		return nil
	})
}

// checkExisting applies the policy to targets that already exist, listing
// each target directory once.
func (c *uploadCmd) checkExisting(ctx context.Context, client *baidu.Client, items []*item) error {
	dirs := map[string]map[string]baidu.File{}
	for _, it := range items {
		dir := path.Dir(it.Remote)
		entries, ok := dirs[dir]
		if !ok {
			files, err := client.List(ctx, dir)
			if err != nil && !errors.Is(err, baidu.ErrNotFound) {
				return err
			}
			entries = map[string]baidu.File{}
			for _, f := range files {
				entries[f.Name] = f
			}
			dirs[dir] = entries
		}
		f, exists := entries[path.Base(it.Remote)]
		switch {
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
	return nil
}
