package baidu

import (
	"context"
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
	FsID  int64     `json:"fsId"`
	Path  string    `json:"path"`
	Name  string    `json:"name"`
	Size  int64     `json:"size"`
	IsDir bool      `json:"isDir"`
	MD5   string    `json:"md5,omitempty"`
	Ctime time.Time `json:"ctime"`
	Mtime time.Time `json:"mtime"`
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
	if !f.IsDir {
		f.MD5 = realMD5(r.MD5, r.BlockList)
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
	var resp struct {
		List []rawFile `json:"list"`
	}
	err := c.do(ctx, &request{op: "获取 " + p, url: pcsURL("file", "meta", nil), param: pathList(p)}, &resp)
	if err != nil {
		return File{}, err
	}
	if len(resp.List) == 0 {
		return File{}, &Error{Op: "获取 " + p, Code: 31066, Message: codeMessage[31066]}
	}
	return resp.List[0].file(), nil
}

// Mkdir creates a directory and any missing parents.
func (c *Client) Mkdir(ctx context.Context, dir string) (File, error) {
	var r struct {
		FsID  int64 `json:"fs_id"`
		Ctime int64 `json:"ctime"`
		Mtime int64 `json:"mtime"`
	}
	q := url.Values{"path": {dir}}
	if err := c.do(ctx, &request{op: "创建目录 " + dir, method: "POST", url: pcsURL("file", "mkdir", q)}, &r); err != nil {
		return File{}, err
	}
	return File{FsID: r.FsID, Path: dir, Name: path.Base(dir), IsDir: true, Ctime: time.Unix(r.Ctime, 0), Mtime: time.Unix(r.Mtime, 0)}, nil
}

// Remove moves files and directories to the recycle bin.
func (c *Client) Remove(ctx context.Context, paths ...string) error {
	return c.do(ctx, &request{op: "删除 " + describe(paths), url: pcsURL("file", "delete", nil), param: pathList(paths...)}, nil)
}

// Rename is one source and destination of a copy or move.
type Rename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Copy copies each From to its To (a full destination path).
func (c *Client) Copy(ctx context.Context, pairs ...Rename) error {
	return c.do(ctx, &request{op: "复制 " + describeFrom(pairs), url: pcsURL("file", "copy", nil), param: map[string]any{"list": pairs}}, nil)
}

// Move moves or renames each From to its To (a full destination path).
func (c *Client) Move(ctx context.Context, pairs ...Rename) error {
	return c.do(ctx, &request{op: "移动 " + describeFrom(pairs), url: pcsURL("file", "move", nil), param: map[string]any{"list": pairs}}, nil)
}

// Search finds files under dir whose name contains keyword.
func (c *Client) Search(ctx context.Context, dir, keyword string, recursive bool) ([]File, error) {
	q := url.Values{"path": {dir}, "wd": {keyword}}
	if recursive {
		q.Set("re", "1")
	}
	var resp struct {
		List []rawFile `json:"list"`
	}
	if err := c.do(ctx, &request{op: "搜索 " + dir, url: pcsURL("file", "search", q)}, &resp); err != nil {
		return nil, err
	}
	return files(resp.List), nil
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

func pathList(paths ...string) map[string]any {
	list := make([]map[string]string, len(paths))
	for i, p := range paths {
		list[i] = map[string]string{"path": p}
	}
	return map[string]any{"list": list}
}
