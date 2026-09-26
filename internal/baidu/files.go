package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// File is a file or directory in the netdisk.
type File struct {
	FsID  int64     `json:"fsId,omitempty"`
	Path  string    `json:"path"`
	Name  string    `json:"name"`
	Size  int64     `json:"size"`
	IsDir bool      `json:"isDir"`
	MD5   string    `json:"md5,omitempty"` // only when it is the content's md5
	Ctime time.Time `json:"ctime,omitzero"`
	Mtime time.Time `json:"mtime,omitzero"`
}

// rawFile is a file as the list, meta and search endpoints send it.
type rawFile struct {
	FsID      int64    `json:"fs_id"`
	Path      string   `json:"path"`
	Name      string   `json:"server_filename"`
	Size      int64    `json:"size"`
	IsDir     flexInt  `json:"isdir"`
	MD5       string   `json:"md5"`
	BlockList []string `json:"block_list"`
	Ctime     int64    `json:"server_ctime"`
	Mtime     int64    `json:"server_mtime"`
}

func (r rawFile) file() File {
	f := File{
		FsID:  r.FsID,
		Path:  r.Path,
		Name:  r.Name,
		Size:  r.Size,
		IsDir: r.IsDir == 1,
		Ctime: time.Unix(r.Ctime, 0),
		Mtime: time.Unix(r.Mtime, 0),
	}
	if f.Name == "" {
		f.Name = path.Base(r.Path)
	}
	// Baidu's md5 of a file stored in several blocks is not the content's md5.
	switch {
	case f.IsDir:
	case len(r.BlockList) == 1:
		f.MD5 = r.BlockList[0]
	case r.BlockList == nil && r.Size <= 4<<20: // necessarily one block
		f.MD5 = deobfuscateMD5(r.MD5)
	}
	return f
}

func files(raw []rawFile) []File {
	out := make([]File, len(raw))
	for i, r := range raw {
		out[i] = r.file()
	}
	return out
}

// List returns the entries of a directory, sorted by name.
func (c *Client) List(ctx context.Context, dir string) ([]File, error) {
	const pageSize = 1000
	var all []File
	for page := 1; ; page++ {
		var resp struct {
			List []rawFile `json:"list"`
		}
		q := url.Values{
			"dir": {dir}, "order": {"name"}, "desc": {"0"}, "clienttype": {"0"},
			"num": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)},
		}
		err := c.do(ctx, &request{op: "列出目录 " + dir, url: panBase + "api/list?" + q.Encode(), ua: uaNetdisk}, &resp)
		if err != nil {
			return all, err
		}
		all = append(all, files(resp.List)...)
		if len(resp.List) < pageSize {
			return all, nil
		}
	}
}

// Walk calls fn for every entry under dir, depth first, parents before
// children. Returning SkipDir from fn for a directory skips its contents.
func (c *Client) Walk(ctx context.Context, dir string, fn func(File) error) error {
	entries, err := c.List(ctx, dir)
	if err != nil {
		return err
	}
	for _, f := range entries {
		err := fn(f)
		if errors.Is(err, SkipDir) {
			continue
		}
		if err != nil {
			return err
		}
		if f.IsDir {
			if err := c.Walk(ctx, f.Path, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// SkipDir tells Walk not to descend into a directory, like fs.SkipDir.
//
//lint:ignore ST1012 named after fs.SkipDir
var SkipDir = errors.New("skip this directory")

// Meta describes one file or directory.
func (c *Client) Meta(ctx context.Context, p string) (File, error) {
	fs, err := c.Metas(ctx, p)
	if err != nil {
		return File{}, err
	}
	return fs[0], nil
}

// Metas describes several files or directories; all must exist. Paths go
// in the URL, so long lists are split into requests that fit.
func (c *Client) Metas(ctx context.Context, paths ...string) ([]File, error) {
	var all []File
	for _, batch := range splitBy(paths, func(b []string) bool {
		target, _ := json.Marshal(b)
		return len(url.QueryEscape(string(target))) <= maxURLParam
	}) {
		target, _ := json.Marshal(batch)
		q := url.Values{"target": {string(target)}, "dlink": {"0"}, "blocks": {"1"}}
		var resp struct {
			Info []rawFile `json:"info"`
		}
		op := "获取 " + describe(batch)
		if err := c.do(ctx, &request{op: op, url: panBase + "api/filemetas?" + q.Encode(), ua: uaNetdisk}, &resp); err != nil {
			return all, err
		}
		if len(resp.Info) != len(batch) {
			return all, &Error{Op: op, Message: "服务器返回的条目数不符"}
		}
		all = append(all, files(resp.Info)...)
	}
	return all, nil
}

// maxURLParam keeps request URLs well below the ~8 KB servers accept.
const maxURLParam = 6000

// maxBatch is how many items one file-manager call takes (Baidu allows 999).
const maxBatch = 500

// splitBy cuts items into consecutive batches, each as long as fits allows
// (but at least one item).
func splitBy[T any](items []T, fits func([]T) bool) [][]T {
	var out [][]T
	for len(items) > 0 {
		n := 1
		for n < len(items) && fits(items[:n+1]) {
			n++
		}
		out = append(out, items[:n])
		items = items[n:]
	}
	return out
}

func byCount[T any](n int) func([]T) bool { return func(b []T) bool { return len(b) <= n } }

// Mkdir creates a directory and any missing parents.
func (c *Client) Mkdir(ctx context.Context, dir string) (File, error) {
	var r struct {
		FsID  int64 `json:"fs_id"`
		Ctime int64 `json:"ctime"`
		Mtime int64 `json:"mtime"`
	}
	form := url.Values{"path": {dir}, "isdir": {"1"}, "rtype": {"0"}}
	if err := c.do(ctx, &request{op: "创建目录 " + dir, url: panBase + "api/create?a=commit", form: form, ua: uaNetdisk}, &r); err != nil {
		return File{}, err
	}
	return File{FsID: r.FsID, Path: dir, Name: path.Base(dir), IsDir: true, Ctime: time.Unix(r.Ctime, 0), Mtime: time.Unix(r.Mtime, 0)}, nil
}

// Remove moves files and directories to the recycle bin and returns the
// paths removed, which on failure may be some of them. Every path must
// exist: Baidu itself would silently accept missing ones.
func (c *Client) Remove(ctx context.Context, paths ...string) ([]string, error) {
	if _, err := c.Metas(ctx, paths...); err != nil {
		return nil, err
	}
	var done []string
	for _, batch := range splitBy(paths, byCount[string](maxBatch)) {
		if err := c.fileManager(ctx, "delete", "删除 "+describe(batch), batch); err != nil {
			return done, err
		}
		done = append(done, batch...)
	}
	return done, nil
}

// Rename is one source and destination of a copy or move.
type Rename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Copy copies each From to its To (a full destination path), creating
// missing parent directories. It returns the pairs that were done, which on
// failure may be some of them.
func (c *Client) Copy(ctx context.Context, pairs ...Rename) ([]Rename, error) {
	return c.renameAll(ctx, "copy", "复制", pairs)
}

// Move moves or renames each From to its To (a full destination path),
// creating missing parent directories. It returns the pairs that were done.
func (c *Client) Move(ctx context.Context, pairs ...Rename) ([]Rename, error) {
	return c.renameAll(ctx, "move", "移动", pairs)
}

func (c *Client) renameAll(ctx context.Context, opera, verb string, pairs []Rename) ([]Rename, error) {
	var done []Rename
	for _, batch := range splitBy(pairs, byCount[Rename](maxBatch)) {
		if err := c.fileManager(ctx, opera, verb+" "+describeFrom(batch), moves(batch)); err != nil {
			return append(done, doneItems(batch, err)...), err
		}
		done = append(done, batch...)
	}
	return done, nil
}

// doneItems finds the pairs of a failed batch that were done anyway, from
// Baidu's answer per item: those it reports with errno 0, and those before
// the first failure, since it applies items in order. Without an answer per
// item (e.g. a network error) nothing is known to be done.
func doneItems(batch []Rename, err error) []Rename {
	e, ok := errors.AsType[*Error](err)
	if !ok || len(e.Items) == 0 {
		return nil
	}
	errno := map[string]int{}
	for _, it := range e.Items {
		errno[it.Path] = int(it.Errno)
	}
	var done []Rename
	failed := false
	for _, p := range batch {
		code, answered := errno[p.From]
		failed = failed || (answered && code != 0)
		if (answered && code == 0) || (!answered && !failed) {
			done = append(done, p)
		}
	}
	return done
}

func moves(pairs []Rename) []map[string]string {
	list := make([]map[string]string, len(pairs))
	for i, p := range pairs {
		list[i] = map[string]string{"path": p.From, "dest": path.Dir(p.To), "newname": path.Base(p.To)}
	}
	return list
}

// fileManager runs a batch delete, copy or move; an existing target fails it.
func (c *Client) fileManager(ctx context.Context, opera, op string, list any) error {
	data, _ := json.Marshal(list)
	q := url.Values{"opera": {opera}, "async": {"0"}, "onnest": {"fail"}}
	return c.do(ctx, &request{op: op, url: panBase + "api/filemanager?" + q.Encode(), form: url.Values{"filelist": {string(data)}}, ua: uaNetdisk}, nil)
}

// Search finds files under dir whose name contains keyword.
func (c *Client) Search(ctx context.Context, dir, keyword string, recursive bool) ([]File, error) {
	var all []File
	for page := 1; ; page++ {
		q := url.Values{"key": {keyword}, "dir": {dir}, "num": {"500"}, "page": {strconv.Itoa(page)}}
		if recursive {
			q.Set("recursion", "1")
		}
		var resp struct {
			List    []rawFile `json:"list"`
			HasMore int       `json:"has_more"`
		}
		if err := c.do(ctx, &request{op: "搜索 " + dir, url: panBase + "api/search?" + q.Encode(), ua: uaNetdisk}, &resp); err != nil {
			return all, err
		}
		all = append(all, files(resp.List)...)
		if resp.HasMore == 0 || len(resp.List) == 0 {
			return all, nil
		}
	}
}

// describe names the paths of a batch in an error message.
func describe(paths []string) string {
	if len(paths) > 3 {
		return strings.Join(paths[:3], ", ") + fmt.Sprintf(" 等 %d 项", len(paths))
	}
	return strings.Join(paths, ", ")
}

func describeFrom(pairs []Rename) string {
	from := make([]string, len(pairs))
	for i, p := range pairs {
		from[i] = p.From
	}
	return describe(from)
}
