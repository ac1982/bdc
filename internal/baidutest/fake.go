// Package baidutest is an in-memory Baidu Netdisk that speaks the endpoints
// bnd uses, for tests that must run anywhere (CI has no account and no
// recordings). Where it models something, it does so the way the real
// service was observed to behave (docs/baidu-api.md): paths ignore case,
// listings are paged, long URLs are refused, a bad login is errno -6, a
// failed batch item is errno 12 with the item's code. Endpoints it does not
// model fail loudly.
package baidutest

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cookies log in to the fake; any other BDUSS is rejected.
const Cookies = "BDUSS=fake-bduss; STOKEN=fake-stoken"

// UID and Name are the fake user's.
const (
	UID  = 42
	Name = "tester"
)

// MaxURL is the longest request URL the fake accepts, like Baidu's servers.
const MaxURL = 8192

// Fake is the netdisk.
type Fake struct {
	mu     sync.Mutex
	nodes  map[string]*node // by key(path)
	nextID int64
	blocks map[string][]byte // uploaded blocks by md5

	// Requests counts requests by "host/path?method".
	Requests map[string]int
}

type node struct {
	path   string // as created, with its case
	id     int64
	dir    bool
	data   []byte
	blocks int // how many blocks the file was uploaded in
}

// key folds a path the way Baidu compares them: case does not matter.
func key(p string) string { return strings.ToLower(p) }

// New returns an empty netdisk (just "/").
func New() *Fake {
	f := &Fake{nodes: map[string]*node{}, blocks: map[string][]byte{}, Requests: map[string]int{}}
	f.nodes["/"] = &node{path: "/", dir: true}
	return f
}

// Client is an http.Client whose every request, to any host, reaches f.
func (f *Fake) Client() *http.Client {
	return &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		f.ServeHTTP(w, r)
		return w.Result(), nil
	})}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (rt roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return rt(r) }

// Put stores a file (creating parent directories), uploaded in blocks blocks.
func (f *Fake) Put(p string, data []byte, blocks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mkdirAll(path.Dir(p))
	f.nodes[key(p)] = &node{path: p, id: f.id(), data: data, blocks: max(blocks, 1)}
}

// Mkdir creates a directory and its parents.
func (f *Fake) Mkdir(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mkdirAll(p)
}

// Data returns a file's content, or nil if there is no such file.
func (f *Fake) Data(p string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := f.nodes[key(p)]; n != nil && !n.dir {
		return n.data
	}
	return nil
}

// Exists reports whether a path exists.
func (f *Fake) Exists(p string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodes[key(p)] != nil
}

func (f *Fake) id() int64 { f.nextID++; return 1000 + f.nextID }

func (f *Fake) mkdirAll(p string) {
	for ; p != "/" && f.nodes[key(p)] == nil; p = path.Dir(p) {
		f.nodes[key(p)] = &node{path: p, id: f.id(), dir: true}
	}
}

// endpoints the fake models, by "host/path" or "host/path?method".
func (f *Fake) endpoints() map[string]func(http.ResponseWriter, *http.Request) {
	return map[string]func(http.ResponseWriter, *http.Request){
		"tieba.baidu.com/c/s/login": func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"error_code": "0", "user": map[string]any{"id": strconv.Itoa(UID), "name": Name}})
		},
		"pan.baidu.com/api/user/getinfo": func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"errno": 0, "records": []any{map[string]any{"uk": 7}}})
		},
		"pan.baidu.com/api/quota": func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"errno": 0, "total": 1 << 40, "used": 1 << 30})
		},
		"pan.baidu.com/api/list":        f.list,
		"pan.baidu.com/api/filemetas":   f.metas,
		"pan.baidu.com/api/search":      f.search,
		"pan.baidu.com/api/create":      f.create,
		"pan.baidu.com/api/filemanager": f.fileManager,
		"pan.baidu.com/api/precreate": func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"errno": 0, "return_type": 1, "uploadid": "up-" + r.Form.Get("path")})
		},
		"pcs.baidu.com/rest/2.0/pcs/file?locateupload": func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"servers": []any{map[string]string{"server": "https://up-1.pcs.baidu.com"}}})
		},
		"up-1.pcs.baidu.com/rest/2.0/pcs/superfile2?upload": f.uploadBlock,
		"pcs.baidu.com/rest/2.0/pcs/file?locatedownload":    f.locate,
		"pan.baidu.com/share/pset": func(w http.ResponseWriter, r *http.Request) {
			id := f.id()
			reply(w, map[string]any{"errno": 0, "shareid": id, "link": fmt.Sprintf("https://pan.baidu.com/s/1fake%d", id)})
		},
		"pan.baidu.com/share/cancel": func(w http.ResponseWriter, r *http.Request) { reply(w, map[string]any{"errno": 0}) },
	}
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.String()) > MaxURL {
		http.Error(w, "414 Request-URI Too Large", http.StatusRequestURITooLong)
		return
	}
	r.ParseForm()
	name := r.URL.Host + r.URL.Path
	if m := r.Form.Get("method"); m != "" {
		name += "?" + m
	}
	f.mu.Lock()
	f.Requests[name]++
	f.mu.Unlock()

	if strings.HasPrefix(r.URL.Path, "/file/") { // content behind a download link
		f.serveContent(w, r)
		return
	}
	handle, ok := f.endpoints()[name]
	switch {
	case !ok:
		http.Error(w, "baidutest: endpoint not modelled: "+name, http.StatusNotImplemented)
	case r.URL.Scheme != "https":
		http.Error(w, "plain http", http.StatusForbidden)
	case !authorized(r) && r.URL.Host == "tieba.baidu.com":
		reply(w, map[string]any{"error_code": "1", "error_msg": "用户未登录或登录失败"})
	case !authorized(r):
		reply(w, map[string]any{"errno": -6})
	default:
		f.mu.Lock()
		defer f.mu.Unlock()
		handle(w, r)
	}
}

func authorized(r *http.Request) bool {
	if ck, err := r.Cookie("BDUSS"); err == nil && ck.Value == "fake-bduss" {
		return true
	}
	return strings.Contains(r.Form.Get("bdusstoken"), "fake-bduss")
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// raw is a file as the API sends it; list and search omit block_list.
func (f *Fake) raw(n *node, blocks bool) map[string]any {
	m := map[string]any{
		"fs_id": n.id, "path": n.path, "server_filename": path.Base(n.path), "isdir": b2i(n.dir),
		"size": len(n.data), "server_ctime": 1700000000, "server_mtime": 1700000000,
	}
	if !n.dir {
		sum := md5.Sum(n.data)
		m["md5"] = hex.EncodeToString(sum[:]) // plain: the client accepts plain and obfuscated
		if blocks && n.blocks == 1 {
			m["block_list"] = []string{hex.EncodeToString(sum[:])}
		} else if blocks {
			m["block_list"] = slices.Repeat([]string{"0123456789abcdef0123456789abcdef"}, n.blocks)
		}
	}
	return m
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// children of a directory, sorted by name.
func (f *Fake) children(dir string) []*node {
	var out []*node
	for k, n := range f.nodes {
		if k != "/" && path.Dir(k) == key(dir) {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b *node) int { return strings.Compare(a.path, b.path) })
	return out
}

// page returns the requested num-sized page (1-based) of all, and whether
// more follow.
func page[T any](all []T, r *http.Request) ([]T, bool) {
	num, _ := strconv.Atoi(r.Form.Get("num"))
	p, _ := strconv.Atoi(r.Form.Get("page"))
	if num <= 0 {
		num = 1000
	}
	start := min(max(p-1, 0)*num, len(all))
	end := min(start+num, len(all))
	return all[start:end], end < len(all)
}

func (f *Fake) list(w http.ResponseWriter, r *http.Request) {
	n := f.nodes[key(r.Form.Get("dir"))]
	if n == nil {
		reply(w, map[string]any{"errno": -9})
		return
	}
	list := []any{}
	if n.dir { // a file lists as empty, as on the real service
		children, _ := page(f.children(n.path), r)
		for _, c := range children {
			list = append(list, f.raw(c, false))
		}
	}
	reply(w, map[string]any{"errno": 0, "list": list})
}

func (f *Fake) metas(w http.ResponseWriter, r *http.Request) {
	var paths []string
	json.Unmarshal([]byte(r.Form.Get("target")), &paths)
	var info []any
	for _, p := range paths {
		n := f.nodes[key(p)]
		if n == nil {
			reply(w, map[string]any{"errno": 12, "info": []any{map[string]any{"errno": -9}}})
			return
		}
		info = append(info, f.raw(n, true))
	}
	reply(w, map[string]any{"errno": 0, "info": info})
}

func (f *Fake) search(w http.ResponseWriter, r *http.Request) {
	dir, word := key(r.Form.Get("dir")), strings.ToLower(r.Form.Get("key"))
	var found []*node
	for k, n := range f.nodes {
		inDir := path.Dir(k) == dir || (r.Form.Get("recursion") == "1" && strings.HasPrefix(k, strings.TrimSuffix(dir, "/")+"/"))
		if k != "/" && inDir && strings.Contains(path.Base(k), word) {
			found = append(found, n)
		}
	}
	slices.SortFunc(found, func(a, b *node) int { return strings.Compare(a.path, b.path) })
	list := []any{}
	found, more := page(found, r)
	for _, n := range found {
		list = append(list, f.raw(n, false))
	}
	reply(w, map[string]any{"errno": 0, "list": list, "has_more": b2i(more)})
}

// create makes a directory (isdir=1) or commits uploaded blocks as a file.
func (f *Fake) create(w http.ResponseWriter, r *http.Request) {
	p := r.Form.Get("path")
	if f.nodes[key(p)] != nil && r.Form.Get("rtype") != "3" {
		reply(w, map[string]any{"errno": -8})
		return
	}
	if r.Form.Get("isdir") == "1" {
		f.mkdirAll(p)
		reply(w, map[string]any{"errno": 0, "fs_id": f.nodes[key(p)].id, "ctime": 1700000000, "mtime": 1700000000})
		return
	}
	var list []string
	json.Unmarshal([]byte(r.Form.Get("block_list")), &list)
	var data []byte
	for _, b := range list {
		data = append(data, f.blocks[b]...)
	}
	f.mkdirAll(path.Dir(p))
	n := &node{path: p, id: f.id(), data: data, blocks: len(list)}
	f.nodes[key(p)] = n
	reply(w, map[string]any{"errno": 0, "fs_id": n.id, "path": p, "size": len(data)})
}

func (f *Fake) uploadBlock(w http.ResponseWriter, r *http.Request) {
	// The part has an empty filename, so read it raw rather than as a form file.
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "uploadedfile" {
		http.Error(w, "no uploadedfile", http.StatusBadRequest)
		return
	}
	data, _ := io.ReadAll(part)
	sum := md5.Sum(data)
	f.blocks[hex.EncodeToString(sum[:])] = data
	reply(w, map[string]any{"md5": hex.EncodeToString(sum[:])})
}

// fileManager deletes, copies or moves; items apply in order until one fails.
func (f *Fake) fileManager(w http.ResponseWriter, r *http.Request) {
	opera, list := r.Form.Get("opera"), r.Form.Get("filelist")
	if opera == "delete" {
		var paths []string
		json.Unmarshal([]byte(list), &paths)
		for _, p := range paths { // missing paths are silently fine, as on the real service
			for _, k := range f.subtree(p) {
				delete(f.nodes, k)
			}
		}
		reply(w, map[string]any{"errno": 0})
		return
	}
	var items []struct{ Path, Dest, Newname string }
	json.Unmarshal([]byte(list), &items)
	var info []any
	for _, it := range items {
		to := path.Join(it.Dest, it.Newname)
		errno := 0
		switch {
		case f.nodes[key(it.Path)] == nil:
			errno = -9
		case f.nodes[key(to)] != nil:
			errno = -8
		}
		info = append(info, map[string]any{"errno": errno, "path": it.Path})
		if errno != 0 {
			reply(w, map[string]any{"errno": 12, "info": info})
			return
		}
		f.mkdirAll(it.Dest)
		src := f.nodes[key(it.Path)].path
		for _, k := range f.subtree(src) { // keys collected first: the loop adds nodes
			n := *f.nodes[k]
			n.path = to + strings.TrimPrefix(n.path, src)
			n.id = f.id()
			if opera == "move" {
				delete(f.nodes, k)
			}
			f.nodes[key(n.path)] = &n
		}
	}
	reply(w, map[string]any{"errno": 0, "info": info})
}

// subtree lists the keys of p and everything under it.
func (f *Fake) subtree(p string) []string {
	var keys []string
	for k := range f.nodes {
		if k == key(p) || strings.HasPrefix(k, key(p)+"/") {
			keys = append(keys, k)
		}
	}
	return keys
}

func (f *Fake) locate(w http.ResponseWriter, r *http.Request) {
	n := f.nodes[key(r.Form.Get("path"))]
	if n == nil || n.dir {
		reply(w, map[string]any{"error_code": 31066, "error_msg": "file does not exist"})
		return
	}
	reply(w, map[string]any{"urls": []any{
		map[string]any{"url": "https://d1.baidupcs.com/file/x?path=" + n.path, "encrypt": 0},
	}})
}

func (f *Fake) serveContent(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	n := f.nodes[key(r.URL.Query().Get("path"))]
	f.mu.Unlock()
	if n == nil || r.Header.Get("User-Agent") == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(n.data))
}
