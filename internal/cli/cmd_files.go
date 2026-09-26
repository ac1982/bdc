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
	listings []listing
	long     bool
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
	r := lsResult{Files: []baidu.File{}, long: c.Long}
	var loose []baidu.File
	for _, p := range paths {
		files, err := client.List(app.ctx, p)
		if errors.Is(err, baidu.ErrNotFound) {
			// not a directory: maybe a file
			f, merr := client.Meta(app.ctx, p)
			if merr != nil {
				return r, err
			}
			loose = append(loose, f)
			continue
		}
		if err != nil {
			return r, err
		}
		c.sort(files)
		r.listings = append(r.listings, listing{Dir: p, Files: files})
	}
	if len(loose) > 0 {
		c.sort(loose)
		r.listings = append([]listing{{Files: loose}}, r.listings...)
	}
	for _, l := range r.listings {
		r.Files = append(r.Files, l.Files...)
	}
	return r, nil
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

func (r lsResult) Human(w io.Writer) {
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
	dir := app.abs(c.Path)
	root := &treeNode{File: baidu.File{Path: dir, Name: path.Base(dir), IsDir: true}}
	var build func(n *treeNode, depth int) error
	build = func(n *treeNode, depth int) error {
		files, err := client.List(app.ctx, n.Path)
		if err != nil {
			return err
		}
		for _, f := range files {
			child := &treeNode{File: f}
			n.Children = append(n.Children, child)
			if f.IsDir && (c.Depth == 0 || depth < c.Depth) {
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
	files, err := client.Search(app.ctx, app.abs(c.Path), c.Keyword, c.Recursive)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(files, func(a, b baidu.File) int { return cmp.Compare(a.Path, b.Path) })
	return searchResult{Files: append([]baidu.File{}, files...)}, nil
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
	if dir != "/" {
		f, err := client.Meta(app.ctx, dir)
		if err != nil {
			return nil, err
		}
		if !f.IsDir {
			return nil, inputf("%s 不是目录", dir)
		}
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
	Created []baidu.File `json:"created"`
}

func (r mkdirResult) Human(w io.Writer) {
	for _, f := range r.Created {
		fmt.Fprintln(w, "已创建", f.Path)
	}
}

func (c *mkdirCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	r := mkdirResult{Created: []baidu.File{}}
	var errs []error
	for _, d := range c.Dirs {
		f, err := client.Mkdir(app.ctx, app.abs(d))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		r.Created = append(r.Created, f)
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
	if err := client.Remove(app.ctx, paths...); err != nil {
		return nil, err
	}
	return rmResult{paths}, nil
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

func (c *cpCmd) Run(app *App) (Result, error) {
	return rename(app, c.Paths, "复制", (*baidu.Client).Copy)
}
func (c *mvCmd) Run(app *App) (Result, error) {
	return rename(app, c.Paths, "移动", (*baidu.Client).Move)
}

type renameOp func(*baidu.Client, context.Context, ...baidu.Rename) error

func rename(app *App, args []string, verb string, op renameOp) (Result, error) {
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
	dst := app.abs(args[len(args)-1])
	if hasGlob(dst) {
		return nil, inputf("目标不能含通配符: %s", dst)
	}
	into := len(srcs) > 1 || strings.HasSuffix(args[len(args)-1], "/")
	if f, err := client.Meta(app.ctx, dst); err == nil {
		if !f.IsDir {
			return nil, inputf("目标已存在: %s", dst)
		}
		into = true
	} else if !errors.Is(err, baidu.ErrNotFound) {
		return nil, err
	}

	r := renameResult{Items: []baidu.Rename{}, verb: verb}
	for _, s := range srcs {
		to := dst
		if into {
			to = path.Join(dst, path.Base(s))
		}
		r.Items = append(r.Items, baidu.Rename{From: s, To: to})
	}
	if err := op(client, app.ctx, r.Items...); err != nil {
		return nil, err
	}
	return r, nil
}
