package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
)

// ls

type lsCmd struct {
	Paths []string `arg:"" optional:"" help:"目录或文件, 可用通配符; 默认为工作目录"`
	Long  bool     `short:"l" help:"显示 fs_id 和 md5"`
	Sort  string   `enum:"name,size,time" default:"name" help:"排序: name, size, time"`
	Desc  bool     `help:"倒序"`
}

type listing struct {
	Dir   string // "" for files named directly on the command line
	Files []baidu.File
}

type lsResult struct {
	Files    []baidu.File `json:"files"`
	listings []listing    // the same files grouped for people
	long     bool
}

// add records files listed from dir ("" for files named directly); files
// named directly are grouped together.
func (r *lsResult) add(dir string, files ...baidu.File) {
	if r.Files == nil {
		r.Files = []baidu.File{}
	}
	r.Files = append(r.Files, files...)
	if n := len(r.listings); dir == "" && n > 0 && r.listings[n-1].Dir == "" {
		r.listings[n-1].Files = append(r.listings[n-1].Files, files...)
		return
	}
	r.listings = append(r.listings, listing{Dir: dir, Files: files})
}

func (c *lsCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	if len(c.Paths) == 0 {
		c.Paths = []string{"."}
	}
	paths, err := app.expand(client, c.Paths...)
	if err != nil {
		return nil, err
	}
	r := &lsResult{long: c.Long}
	for _, p := range paths {
		f, err := app.stat(client, p)
		if err != nil {
			return r, err
		}
		if !f.IsDir {
			r.add("", f)
			continue
		}
		files, err := client.List(app.ctx, p)
		c.sort(files)
		r.add(p, files...)
		if err != nil {
			return r, err
		}
	}
	return r, nil
}

// stat describes a path; the root, which Baidu cannot describe, is a directory.
func (a *App) stat(client *baidu.Client, p string) (baidu.File, error) {
	if p == "/" {
		return baidu.File{Path: "/", Name: "/", IsDir: true}, nil
	}
	return client.Meta(a.ctx, p)
}

func (c *lsCmd) sort(files []baidu.File) {
	slices.SortStableFunc(files, func(a, b baidu.File) int {
		var d int
		switch c.Sort {
		case "size":
			d = cmp.Compare(a.Size, b.Size)
		case "time":
			d = a.Mtime.Compare(b.Mtime)
		default:
			d = cmp.Compare(a.Name, b.Name)
		}
		if c.Desc {
			d = -d
		}
		return d
	})
}

func (r *lsResult) Human(w io.Writer) {
	for i, l := range r.listings {
		if i > 0 {
			fmt.Fprintln(w)
		}
		if l.Dir != "" && len(r.listings) > 1 {
			fmt.Fprintf(w, "%s:\n", l.Dir)
		}
		fileTable(w, l.Files, r.long, l.Dir == "")
	}
}

// fileTable prints files as a table with a summary line.
func fileTable(w io.Writer, files []baidu.File, long, fullPath bool) {
	var rows [][]string
	var total int64
	dirs := 0
	for _, f := range files {
		name := f.Name
		if fullPath {
			name = f.Path
		}
		sz := size(f.Size)
		if f.IsDir {
			name += "/"
			sz = "-"
			dirs++
		}
		total += f.Size
		row := []string{sz, clock(f.Mtime), name}
		if long {
			row = []string{strconv.FormatInt(f.FsID, 10), sz, clock(f.Mtime), cmp.Or(f.MD5, "-"), name}
		}
		rows = append(rows, row)
	}
	table(w, rows)
	fmt.Fprintf(w, "共 %d 个文件 (%s), %d 个目录\n", len(files)-dirs, size(total), dirs)
}

// tree

type treeCmd struct {
	Path  string `arg:"" optional:"" default:"." help:"目录"`
	Depth int    `help:"最大深度, 0 为不限" default:"0"`
}

type treeNode struct {
	baidu.File
	Children []*treeNode `json:"children,omitempty"`
}

type treeResult struct {
	Root *treeNode `json:"root"`
}

func (c *treeCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	f, err := app.stat(client, app.abs(c.Path))
	if err != nil {
		return nil, err
	}
	if !f.IsDir {
		return nil, inputf("%s 不是目录", f.Path)
	}
	root := &treeNode{File: f}
	var build func(n *treeNode, depth int) error
	build = func(n *treeNode, depth int) error {
		files, err := client.List(app.ctx, n.Path) // on failure, the pages read so far
		for _, f := range files {
			n.Children = append(n.Children, &treeNode{File: f})
		}
		if err != nil {
			return err
		}
		for _, child := range n.Children {
			if child.IsDir && (c.Depth == 0 || depth < c.Depth) {
				if err := build(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// On failure the tree built so far is still returned.
	return treeResult{root}, build(root, 1)
}

func (r treeResult) Human(w io.Writer) {
	fmt.Fprintln(w, r.Root.Path)
	var walk func(n *treeNode, prefix string)
	walk = func(n *treeNode, prefix string) {
		for i, c := range n.Children {
			branch, next := "├── ", "│   "
			if i == len(n.Children)-1 {
				branch, next = "└── ", "    "
			}
			name := c.Name
			if c.IsDir {
				name += "/"
			} else {
				name += "  " + size(c.Size)
			}
			fmt.Fprintln(w, prefix+branch+name)
			walk(c, prefix+next)
		}
	}
	walk(r.Root, "")
}

// meta

type metaCmd struct {
	Paths []string `arg:"" help:"文件或目录, 可用通配符"`
}

type filesResult struct {
	Files []baidu.File `json:"files"`
}

func (c *metaCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	paths, err := app.expand(client, c.Paths...)
	if err != nil {
		return nil, err
	}
	r := metaResult{Files: []baidu.File{}}
	for _, p := range paths {
		f, err := client.Meta(app.ctx, p)
		if err != nil {
			return r, err
		}
		r.Files = append(r.Files, f)
	}
	return r, nil
}

type metaResult filesResult

func (r metaResult) Human(w io.Writer) {
	for i, f := range r.Files {
		if i > 0 {
			fmt.Fprintln(w)
		}
		kind := "文件"
		if f.IsDir {
			kind = "目录"
		}
		rows := [][]string{{"路径", f.Path}, {"类型", kind}, {"fs_id", strconv.FormatInt(f.FsID, 10)}}
		if !f.IsDir {
			rows = append(rows, []string{"大小", fmt.Sprintf("%s (%d 字节)", size(f.Size), f.Size)}, []string{"md5", f.MD5})
		}
		rows = append(rows, []string{"创建时间", clock(f.Ctime)}, []string{"修改时间", clock(f.Mtime)})
		table(w, rows)
	}
}

// search

type searchCmd struct {
	Keyword   string `arg:"" help:"文件名中包含的文字"`
	Path      string `default:"." help:"在此目录中搜索"`
	Recursive bool   `short:"r" help:"包括子目录"`
}

type searchResult filesResult

func (r searchResult) Human(w io.Writer) { fileTable(w, r.Files, false, true) }

func (c *searchCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	files, err := client.Search(app.ctx, app.abs(c.Path), c.Keyword, c.Recursive) // on failure, the pages read so far
	slices.SortFunc(files, func(a, b baidu.File) int { return cmp.Compare(a.Path, b.Path) })
	return searchResult{Files: append([]baidu.File{}, files...)}, err
}

// cd, pwd

type cdCmd struct {
	Dir string `arg:"" optional:"" default:"/" help:"目录, 默认为根目录"`
}

type workdirResult struct {
	Workdir string `json:"workdir"`
}

func (r workdirResult) Human(w io.Writer) { fmt.Fprintln(w, r.Workdir) }

func (c *cdCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	dirs, err := app.expand(client, c.Dir)
	if err != nil {
		return nil, err
	}
	if len(dirs) > 1 {
		return nil, inputf("%s 匹配了多个目录", c.Dir)
	}
	dir := dirs[0]
	f, err := app.stat(client, dir)
	if err != nil {
		return nil, err
	}
	if !f.IsDir {
		return nil, inputf("%s 不是目录", dir)
	}
	acc, _ := app.account()
	acc.Workdir = dir
	return workdirResult{dir}, app.cfg.Save()
}

type pwdCmd struct{}

func (c *pwdCmd) Run(app *App) (Result, error) {
	acc, err := app.account()
	if err != nil {
		return nil, err
	}
	return workdirResult{acc.Workdir}, nil
}

// mkdir

type mkdirCmd struct {
	Dirs []string `arg:"" help:"要创建的目录, 上级目录会一并创建"`
}

type mkdirResult struct {
	Created  []baidu.File `json:"created"`
	Existing []string     `json:"existing"` // directories that were already there
}

func (r mkdirResult) Human(w io.Writer) {
	for _, f := range r.Created {
		fmt.Fprintln(w, "已创建", f.Path)
	}
	for _, p := range r.Existing {
		fmt.Fprintln(w, "已存在", p)
	}
}

// Run creates each directory; one that already exists is fine, so running
// it again is safe.
func (c *mkdirCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	r := mkdirResult{Created: []baidu.File{}, Existing: []string{}}
	var errs []error
	for _, d := range c.Dirs {
		p := app.abs(d)
		switch f, created, err := client.EnsureDir(app.ctx, p); {
		case err != nil:
			errs = append(errs, err)
		case created:
			r.Created = append(r.Created, f)
		default:
			r.Existing = append(r.Existing, p)
		}
	}
	return r, errors.Join(errs...)
}

// rm

type rmCmd struct {
	Paths []string `arg:"" help:"要删除的文件或目录, 可用通配符; 删除的内容进入回收站"`
}

type rmResult struct {
	Removed []string `json:"removed"`
}

func (r rmResult) Human(w io.Writer) {
	for _, p := range r.Removed {
		fmt.Fprintln(w, "已删除", p)
	}
}

func (c *rmCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	paths, err := app.expand(client, c.Paths...)
	if err != nil {
		return nil, err
	}
	removed, err := client.Remove(app.ctx, paths...)
	return rmResult{append([]string{}, removed...)}, err
}

// cp, mv

type cpCmd struct {
	Paths []string `arg:"" help:"源 … 目标. 目标是已有目录时放入其中; 否则只有一个源时作为新名字, 多个源时作为目录"`
}

type mvCmd cpCmd

type renameResult struct {
	Items []baidu.Rename `json:"items"`
	verb  string
}

func (r renameResult) Human(w io.Writer) {
	for _, it := range r.Items {
		fmt.Fprintf(w, "已%s %s → %s\n", r.verb, it.From, it.To)
	}
}

func (c *cpCmd) Run(app *App) (Result, error) { return copier.run(app, c.Paths) }
func (c *mvCmd) Run(app *App) (Result, error) { return mover.run(app, c.Paths) }

// renamer is what differs between cp and mv.
type renamer struct {
	verb string
	op   func(*baidu.Client, context.Context, ...baidu.Rename) ([]baidu.Rename, error)
	move bool
}

var (
	copier = renamer{"复制", (*baidu.Client).Copy, false}
	mover  = renamer{"移动", (*baidu.Client).Move, true}
)

func (rn renamer) run(app *App, args []string) (Result, error) {
	if len(args) < 2 {
		return nil, usagef("需要源和目标")
	}
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	srcs, err := app.expand(client, args[:len(args)-1]...)
	if err != nil {
		return nil, err
	}
	dst := app.abs(args[len(args)-1]) // the target is a name, never a pattern
	into := len(srcs) > 1 || strings.HasSuffix(args[len(args)-1], "/")
	if rn.move && len(srcs) == 1 && srcs[0] != dst && remoteKey(srcs[0]) == remoteKey(dst) {
		// Only the case changes: Baidu ignores case, so dst "exists" as the source itself.
		return rn.renameInPlace(app, client, srcs[0], dst)
	}
	if f, err := client.Meta(app.ctx, dst); err == nil {
		if !f.IsDir {
			return nil, inputf("目标已存在: %s", dst)
		}
		into = true
	} else if !errors.Is(err, baidu.ErrNotFound) {
		return nil, err
	}
	if rn.move && !into && path.Dir(srcs[0]) == path.Dir(dst) {
		return rn.renameInPlace(app, client, srcs[0], dst)
	}

	r := renameResult{Items: []baidu.Rename{}, verb: rn.verb}
	seen := map[string]string{} // by remote key
	for _, s := range srcs {
		to := dst
		if into {
			to = path.Join(dst, path.Base(s))
		}
		if other, dup := seen[remoteKey(to)]; dup {
			return nil, inputf("%s 和 %s 会%s到同一个位置 %s", other, s, rn.verb, to)
		}
		seen[remoteKey(to)] = s
		r.Items = append(r.Items, baidu.Rename{From: s, To: to})
	}
	done, err := rn.op(client, app.ctx, r.Items...)
	r.Items = append([]baidu.Rename{}, done...) // on failure, what was done anyway
	return r, err
}

// renameInPlace gives a file a new name in its directory, as the web app's
// rename does (a move cannot change only the case of a name).
func (rn renamer) renameInPlace(app *App, client *baidu.Client, src, dst string) (Result, error) {
	if err := client.RenameInPlace(app.ctx, src, path.Base(dst)); err != nil {
		return nil, err
	}
	return renameResult{Items: []baidu.Rename{{From: src, To: dst}}, verb: rn.verb}, nil
}
