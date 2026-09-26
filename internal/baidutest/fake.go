// Package baidutest is an in-memory Baidu Netdisk that speaks the endpoints
// bnd uses, for tests that must run anywhere (CI has no account and no
// recordings). It answers the way the real service was observed to,
// including its error codes; see docs/baidu-api.md.
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

// Fake is the netdisk: files and directories by path.
type Fake struct {
	mu     sync.Mutex
	nodes  map[string]*node
	nextID int64
	blocks map[string][]byte // uploaded blocks by md5
	shares map[int64][]string

	// Requests counts requests by "host/path?method".
	Requests map[string]int
}

type node struct {
	id     int64
	dir    bool
	data   []byte
	blocks int // how many blocks the file was uploaded in
	mtime  time.Time
}

// New returns an empty netdisk (just "/").
func New() *Fake {
	f := &Fake{nodes: map[string]*node{}, blocks: map[string][]byte{}, shares: map[int64][]string{}, Requests: map[string]int{}}
	f.nodes["/"] = &node{dir: true}
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
	f.nodes[p] = &node{id: f.id(), data: data, blocks: max(blocks, 1), mtime: time.Unix(1700000000, 0)}
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
	if n := f.nodes[p]; n != nil && !n.dir {
		return n.data
	}
	return nil
}

// Exists reports whether a path exists.
func (f *Fake) Exists(p string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodes[p] != nil
}

func (f *Fake) id() int64 { f.nextID++; return 1000 + f.nextID }

func (f *Fake) mkdirAll(p string) {
	for ; p != "/" && f.nodes[p] == nil; p = path.Dir(p) {
		f.nodes[p] = &node{id: f.id(), dir: true, mtime: time.Unix(1700000000, 0)}
	}
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	key := r.URL.Host + r.URL.Path
	if m := r.Form.Get("method"); m != "" {
		key += "?" + m
	}
	f.mu.Lock()
	f.Requests[key]++
	f.mu.Unlock()

	if strings.HasPrefix(r.URL.Path, "/file/") { // content from a download link
		f.serveContent(w, r)
		return
	}
	if r.URL.Scheme == "http" {
		http.Error(w, "plain http", http.StatusForbidden)
		return
	}
	if !f.authorized(r) {
		if r.URL.Host == "tieba.baidu.com" {
			reply(w, map[string]any{"error_code": "1", "error_msg": "用户未登录或登录失败"})
		} else {
			reply(w, map[string]any{"errno": -6})
		}
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch key {
	case "tieba.baidu.com/c/s/login":
		reply(w, map[string]any{"error_code": "0", "user": map[string]any{"id": strconv.Itoa(UID), "name": Name}})
	case "pan.baidu.com/api/user/getinfo":
		reply(w, map[string]any{"errno": 0, "records": []any{map[string]any{"uk": 7}}})
	case "pan.baidu.com/api/quota":
		reply(w, map[string]any{"errno": 0, "total": 1 << 40, "used": 1 << 30})
	case "pan.baidu.com/api/list":
		f.list(w, r)
	case "pan.baidu.com/api/filemetas":
		f.metas(w, r)
	case "pan.baidu.com/api/search":
		f.search(w, r)
	case "pan.baidu.com/api/create":
		f.create(w, r)
	case "pan.baidu.com/api/filemanager":
		f.fileManager(w, r)
	case "pan.baidu.com/api/precreate":
		reply(w, map[string]any{"errno": 0, "return_type": 1, "uploadid": "up-" + r.Form.Get("path")})
	case "pcs.baidu.com/rest/2.0/pcs/file?locateupload":
		reply(w, map[string]any{"servers": []any{map[string]string{"server": "https://up-1.pcs.baidu.com"}}})
	case "up-1.pcs.baidu.com/rest/2.0/pcs/superfile2?upload":
		f.uploadBlock(w, r)
	case "pcs.baidu.com/rest/2.0/pcs/file?locatedownload":
		f.locate(w, r)
	case "pan.baidu.com/share/pset":
		var paths []string
		json.Unmarshal([]byte(r.Form.Get("path_list")), &paths)
		id := f.id()
		f.shares[id] = paths
		reply(w, map[string]any{"errno": 0, "shareid": id, "link": fmt.Sprintf("https://pan.baidu.com/s/1fake%d", id)})
	case "pan.baidu.com/share/cancel":
		reply(w, map[string]any{"errno": 0})
	default:
		http.Error(w, "fake: no endpoint "+key, http.StatusNotFound)
	}
}

func (f *Fake) authorized(r *http.Request) bool {
	if ck, err := r.Cookie("BDUSS"); err == nil && ck.Value == "fake-bduss" {
		return true
	}
	return strings.Contains(r.Form.Get("bdusstoken"), "fake-bduss")
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (f *Fake) raw(p string) map[string]any {
	n := f.nodes[p]
	m := map[string]any{
		"fs_id": n.id, "path": p, "server_filename": path.Base(p), "isdir": b2i(n.dir),
		"size": len(n.data), "server_ctime": n.mtime.Unix(), "server_mtime": n.mtime.Unix(),
	}
	if !n.dir {
		sum := md5.Sum(n.data)
		m["md5"] = hex.EncodeToString(sum[:]) // not obfuscated; the client accepts both
		if n.blocks == 1 {
			m["block_list"] = []string{hex.EncodeToString(sum[:])}
		} else {
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

// children lists a directory, sorted by name.
func (f *Fake) children(dir string) []string {
	var out []string
	for p := range f.nodes {
		if p != "/" && path.Dir(p) == dir {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

func (f *Fake) list(w http.ResponseWriter, r *http.Request) {
	dir := r.Form.Get("dir")
	n := f.nodes[dir]
	if n == nil {
		reply(w, map[string]any{"errno": -9})
		return
	}
	list := []any{}
	if n.dir { // a file lists as empty, as on the real service
		for _, p := range f.children(dir) {
			m := f.raw(p)
			delete(m, "block_list") // list has no block_list
			list = append(list, m)
		}
	}
	reply(w, map[string]any{"errno": 0, "list": list})
}

func (f *Fake) metas(w http.ResponseWriter, r *http.Request) {
	var paths []string
	json.Unmarshal([]byte(r.Form.Get("target")), &paths)
	var info []any
	for _, p := range paths {
		if f.nodes[p] == nil {
			reply(w, map[string]any{"errno": 12, "info": []any{map[string]any{"errno": -9, "path": p}}})
			return
		}
		info = append(info, f.raw(p))
	}
	reply(w, map[string]any{"errno": 0, "info": info})
}

func (f *Fake) search(w http.ResponseWriter, r *http.Request) {
	dir, key := r.Form.Get("dir"), r.Form.Get("key")
	list := []any{}
	for p := range f.nodes {
		inDir := path.Dir(p) == dir || (r.Form.Get("recursion") == "1" && strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/"))
		if p != "/" && inDir && strings.Contains(path.Base(p), key) {
			m := f.raw(p)
			delete(m, "block_list")
			list = append(list, m)
		}
	}
	reply(w, map[string]any{"errno": 0, "list": list, "has_more": 0})
}

// create makes a directory (isdir=1) or commits uploaded blocks as a file.
func (f *Fake) create(w http.ResponseWriter, r *http.Request) {
	p := r.Form.Get("path")
	if old := f.nodes[p]; old != nil && r.Form.Get("rtype") != "3" {
		reply(w, map[string]any{"errno": -8})
		return
	}
	if r.Form.Get("isdir") == "1" {
		f.mkdirAll(p)
		reply(w, map[string]any{"errno": 0, "fs_id": f.nodes[p].id, "ctime": 1700000000, "mtime": 1700000000})
		return
	}
	var list []string
	json.Unmarshal([]byte(r.Form.Get("block_list")), &list)
	var data []byte
	for _, b := range list {
		data = append(data, f.blocks[b]...)
	}
	f.mkdirAll(path.Dir(p))
	f.nodes[p] = &node{id: f.id(), data: data, blocks: len(list), mtime: time.Unix(1700000000, 0)}
	reply(w, map[string]any{"errno": 0, "fs_id": f.nodes[p].id, "path": p, "size": len(data)})
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

func (f *Fake) fileManager(w http.ResponseWriter, r *http.Request) {
	opera := r.Form.Get("opera")
	list := r.Form.Get("filelist")
	fail := func(errno int, p string) {
		reply(w, map[string]any{"errno": 12, "info": []any{map[string]any{"errno": errno, "path": p}}})
	}
	if opera == "delete" {
		var paths []string
		json.Unmarshal([]byte(list), &paths)
		for _, p := range paths { // missing paths are silently fine, as on the real service
			for q := range f.nodes {
				if q == p || strings.HasPrefix(q, p+"/") {
					delete(f.nodes, q)
				}
			}
		}
		reply(w, map[string]any{"errno": 0})
		return
	}
	var items []struct{ Path, Dest, Newname string }
	json.Unmarshal([]byte(list), &items)
	for _, it := range items {
		to := path.Join(it.Dest, it.Newname)
		if f.nodes[it.Path] == nil {
			fail(-9, it.Path)
			return
		}
		if f.nodes[to] != nil {
			fail(-30, it.Path)
			return
		}
		f.mkdirAll(it.Dest)
		for q, n := range f.nodes {
			if q == it.Path || strings.HasPrefix(q, it.Path+"/") {
				c := *n
				c.id = f.id()
				f.nodes[to+strings.TrimPrefix(q, it.Path)] = &c
				if opera == "move" {
					delete(f.nodes, q)
				}
			}
		}
	}
	reply(w, map[string]any{"errno": 0})
}

func (f *Fake) locate(w http.ResponseWriter, r *http.Request) {
	p := r.Form.Get("path")
	if n := f.nodes[p]; n == nil || n.dir {
		reply(w, map[string]any{"error_code": 31066, "error_msg": "file does not exist"})
		return
	}
	reply(w, map[string]any{"urls": []any{
		map[string]any{"url": "https://d1.baidupcs.com/file/x?path=" + p, "encrypt": 0},
	}})
}

func (f *Fake) serveContent(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	n := f.nodes[r.URL.Query().Get("path")]
	f.mu.Unlock()
	if n == nil || r.Header.Get("User-Agent") == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(n.data))
}
