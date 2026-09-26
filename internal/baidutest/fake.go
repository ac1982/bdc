// Package baidutest is an in-memory Baidu Netdisk that speaks the web app's
// API as bdc uses it, for tests that must run anywhere (CI has no account and
// no recordings). Where it models something, it does so the way the real
// service was observed to behave (docs/baidu-api.md): paths ignore case,
// listings are paged, long URLs are refused, changes need the bdstoken, a bad
// login is errno -6, a failed batch item is errno 12 with the item's code.
// Endpoints it does not model fail loudly.
package baidutest

import (
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"encoding/base64"
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

// UK and Name are the fake user's.
const (
	UK   = 42
	Name = "tester"
)

// MaxURL is the longest request URL the fake accepts, like Baidu's servers.
const MaxURL = 8192

const (
	token        = "fake-token"
	sign1, sign3 = "fake-sign1", "fake-sign3"
)

// Fake is the netdisk.
type Fake struct {
	mu      sync.Mutex
	nodes   map[string]*node // by key(path)
	nextID  int64
	blocks  map[string][]byte // uploaded blocks by md5
	recycle map[int64][]*node // removed subtrees by the fs_id of their top
	tasks   map[int64]string  // offline tasks: id → source URL

	// Requests counts requests by "host/path?method".
	Requests map[string]int

	// Fail, if set, may answer a modelled endpoint instead of the fake (return
	// non-nil): tests inject failures with it. n counts calls to the endpoint.
	Fail func(endpoint string, n int) any
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
	f := &Fake{nodes: map[string]*node{}, blocks: map[string][]byte{}, recycle: map[int64][]*node{},
		tasks: map[int64]string{}, Requests: map[string]int{}}
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

// Name returns the stored name of a path, with its case.
func (f *Fake) Name(p string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := f.nodes[key(p)]; n != nil {
		return path.Base(n.path)
	}
	return ""
}

func (f *Fake) id() int64 { f.nextID++; return 1000 + f.nextID }

func (f *Fake) mkdirAll(p string) {
	for ; p != "/" && f.nodes[key(p)] == nil; p = path.Dir(p) {
		f.nodes[key(p)] = &node{path: p, id: f.id(), dir: true}
	}
}

// endpoint is a modelled API; write marks those that need the bdstoken.
type endpoint struct {
	handle func(http.ResponseWriter, *http.Request)
	write  bool
}

// endpoints the fake models, by "host/path" or "host/path?method".
func (f *Fake) endpoints() map[string]endpoint {
	return map[string]endpoint{
		"pan.baidu.com/api/gettemplatevariable": {handle: f.vars},
		"pan.baidu.com/api/quota": {handle: func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"errno": 0, "total": 1 << 40, "used": 1 << 30})
		}},
		"pan.baidu.com/api/list":        {handle: f.list},
		"pan.baidu.com/api/filemetas":   {handle: f.metas},
		"pan.baidu.com/api/search":      {handle: f.search},
		"pan.baidu.com/api/create":      {handle: f.create, write: true},
		"pan.baidu.com/api/filemanager": {handle: f.fileManager, write: true},
		"pan.baidu.com/api/rapidupload": {handle: f.rapidUpload, write: true},
		"pan.baidu.com/api/precreate": {write: true, handle: func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"errno": 0, "return_type": 1, "uploadid": "up-" + r.Form.Get("path")})
		}},
		"d.pcs.baidu.com/rest/2.0/pcs/file?locateupload": {handle: func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"error_code": 0, "server": []string{"up-1.pcs.baidu.com"}})
		}},
		"up-1.pcs.baidu.com/rest/2.0/pcs/superfile2?upload": {handle: f.uploadBlock},
		"pan.baidu.com/api/download":                        {handle: f.download},
		"pan.baidu.com/share/pset": {write: true, handle: func(w http.ResponseWriter, r *http.Request) {
			id := f.id()
			reply(w, map[string]any{"errno": 0, "shareid": id, "link": fmt.Sprintf("https://pan.baidu.com/s/1fake%d", id)})
		}},
		"pan.baidu.com/share/cancel": {write: true, handle: func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"errno": 0})
		}},
		"pan.baidu.com/share/record": {handle: func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"errno": 0, "list": []any{}})
		}},
		"pan.baidu.com/api/recycle/list/":   {handle: f.recycleList},
		"pan.baidu.com/api/recycle/restore": {handle: f.restore, write: true},
		"pan.baidu.com/api/recycle/delete": {write: true, handle: func(w http.ResponseWriter, r *http.Request) {
			reply(w, map[string]any{"errno": 132, "verify_scene": 2}) // as the real one answered, even the web app
		}},
		"pan.baidu.com/rest/2.0/services/cloud_dl?add_task":    {handle: f.addTask, write: true},
		"pan.baidu.com/rest/2.0/services/cloud_dl?list_task":   {handle: f.listTasks, write: true},
		"pan.baidu.com/rest/2.0/services/cloud_dl?delete_task": {handle: f.deleteTask, write: true},
	}
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.String()) > MaxURL {
		http.Error(w, "414 Request-URI Too Large", http.StatusRequestURITooLong)
		return
	}
	if r.URL.Host == "d.pcs.baidu.com" && strings.HasPrefix(r.URL.Path, "/file/") { // a download link
		http.Redirect(w, r, "https://d1.baidupcs.com"+r.URL.RequestURI(), http.StatusFound)
		return
	}
	if strings.HasSuffix(r.URL.Host, ".baidupcs.com") {
		f.serveContent(w, r)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.ParseForm()
	}
	name := r.URL.Host + r.URL.Path
	if m := r.URL.Query().Get("method"); m != "" {
		name += "?" + m
	}
	f.mu.Lock()
	f.Requests[name]++
	calls := f.Requests[name]
	f.mu.Unlock()

	ep, ok := f.endpoints()[name]
	switch {
	case !ok:
		http.Error(w, "baidutest: endpoint not modelled: "+name, http.StatusNotImplemented)
	case r.URL.Scheme != "https":
		http.Error(w, "plain http", http.StatusForbidden)
	case !authorized(r):
		reply(w, map[string]any{"errno": -6})
	case ep.write && r.URL.Query().Get("bdstoken") != token:
		reply(w, map[string]any{"errno": -6, "show_msg": "missing bdstoken"})
	default:
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.Fail != nil {
			if v := f.Fail(name, calls); v != nil {
				reply(w, v)
				return
			}
		}
		ep.handle(w, r)
	}
}

func authorized(r *http.Request) bool {
	ck, err := r.Cookie("BDUSS")
	return err == nil && ck.Value == "fake-bduss"
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (f *Fake) vars(w http.ResponseWriter, r *http.Request) {
	var fields []string
	json.Unmarshal([]byte(r.Form.Get("fields")), &fields)
	all := map[string]any{"bdstoken": token, "uk": UK, "username": Name, "sign1": sign1, "sign3": sign3, "timestamp": 1700000000}
	result := map[string]any{}
	for _, k := range fields {
		if v, ok := all[k]; ok {
			result[k] = v
		}
	}
	reply(w, map[string]any{"errno": 0, "result": result})
}

// raw is a file as the API sends it; list and search omit block_list.
func (f *Fake) raw(n *node, blocks bool) map[string]any {
	m := map[string]any{
		"fs_id": n.id, "path": n.path, "server_filename": path.Base(n.path), "isdir": b2i(n.dir),
		"size": len(n.data), "server_ctime": 1700000000, "server_mtime": 1700000000,
	}
	if !n.dir {
		m["md5"] = md5hex(n.data) // plain: the client accepts plain and obfuscated
		if blocks && n.blocks == 1 {
			m["block_list"] = []string{md5hex(n.data)}
		} else if blocks {
			m["block_list"] = slices.Repeat([]string{"0123456789abcdef0123456789abcdef"}, n.blocks)
		}
	}
	return m
}

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
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
	if f.nodes[key(p)] != nil && r.URL.Query().Get("rtype") != "3" && r.Form.Get("rtype") != "3" {
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

// rapidUpload creates the file when some stored file has the content.
func (f *Fake) rapidUpload(w http.ResponseWriter, r *http.Request) {
	want := deobfuscate(r.Form.Get("content-md5"))
	for _, n := range f.nodes {
		if n.dir || md5hex(n.data) != want {
			continue
		}
		p := r.Form.Get("path")
		if f.nodes[key(p)] != nil && r.URL.Query().Get("rtype") != "3" {
			reply(w, map[string]any{"errno": -8})
			return
		}
		f.mkdirAll(path.Dir(p))
		c := &node{path: p, id: f.id(), data: n.data, blocks: 1}
		f.nodes[key(p)] = c
		reply(w, map[string]any{"errno": 0, "info": map[string]any{"fs_id": c.id, "path": p, "size": len(c.data)}})
		return
	}
	reply(w, map[string]any{"errno": 404})
}

// deobfuscate undoes the md5 obfuscation of Baidu's APIs (see the baidu package).
func deobfuscate(raw string) string {
	const digits = "0123456789abcdef"
	if len(raw) != 32 || strings.ContainsRune(digits, rune(raw[9])) {
		return raw
	}
	s := []byte(raw)
	s[9] = digits[raw[9]-'g']
	o := make([]byte, 32)
	for i, c := range s {
		o[i] = digits[strings.IndexByte(digits, c)^(i&15)]
	}
	return string(o[8:16]) + string(o[0:8]) + string(o[24:32]) + string(o[16:24])
}

func (f *Fake) uploadBlock(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "file" {
		http.Error(w, "no file part", http.StatusBadRequest)
		return
	}
	data, _ := io.ReadAll(part)
	f.blocks[md5hex(data)] = data
	reply(w, map[string]any{"md5": md5hex(data)})
}

// fileManager deletes, renames, copies or moves; items apply in order until
// one fails (errno 12 and each processed item's errno).
func (f *Fake) fileManager(w http.ResponseWriter, r *http.Request) {
	opera, list := r.URL.Query().Get("opera"), r.Form.Get("filelist")
	if opera == "delete" {
		var paths []string
		json.Unmarshal([]byte(list), &paths)
		for _, p := range paths { // missing paths are silently fine, as on the real service
			if top := f.nodes[key(p)]; top != nil {
				var removed []*node
				for _, k := range f.subtree(p) {
					removed = append(removed, f.nodes[k])
					delete(f.nodes, k)
				}
				f.recycle[top.id] = removed
			}
		}
		reply(w, map[string]any{"errno": 0})
		return
	}
	var items []struct{ Path, Dest, Newname string }
	json.Unmarshal([]byte(list), &items)
	var info []any
	for _, it := range items {
		dest := it.Dest
		if opera == "rename" {
			dest = path.Dir(it.Path)
		}
		to := path.Join(dest, it.Newname)
		errno := 0
		switch {
		case f.nodes[key(it.Path)] == nil:
			errno = -9
		case f.nodes[key(to)] != nil && !(opera == "rename" && key(to) == key(it.Path)):
			errno = -8
		}
		info = append(info, map[string]any{"errno": errno, "path": it.Path})
		if errno != 0 {
			reply(w, map[string]any{"errno": 12, "info": info})
			return
		}
		f.mkdirAll(dest)
		src := f.nodes[key(it.Path)].path
		for _, k := range f.subtree(src) { // keys collected first: the loop adds nodes
			n := *f.nodes[k]
			n.path = to + strings.TrimPrefix(n.path, src)
			if opera == "copy" {
				n.id = f.id()
			} else {
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

// download answers dlinks for fs ids, if the request is signed.
func (f *Fake) download(w http.ResponseWriter, r *http.Request) {
	c, _ := rc4.NewCipher([]byte(sign3))
	want := []byte(sign1)
	c.XORKeyStream(want, want)
	if r.Form.Get("sign") != base64.StdEncoding.EncodeToString(want) {
		reply(w, map[string]any{"errno": 113}) // 签名错误
		return
	}
	var ids []int64
	json.Unmarshal([]byte(r.Form.Get("fidlist")), &ids)
	var dlinks []any
	for _, id := range ids {
		for _, n := range f.nodes {
			if n.id == id && !n.dir {
				dlinks = append(dlinks, map[string]any{"fs_id": strconv.FormatInt(id, 10), "dlink": "https://d.pcs.baidu.com/file/x?fid=" + strconv.FormatInt(id, 10)})
			}
		}
	}
	reply(w, map[string]any{"errno": 0, "dlink": dlinks})
}

// serveContent serves a file behind a download link, in ranges.
func (f *Fake) serveContent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("fid"), 10, 64)
	f.mu.Lock()
	var data []byte
	found := false
	for _, n := range f.nodes {
		if n.id == id && !n.dir {
			data, found = n.data, true
		}
	}
	f.mu.Unlock()
	if !found || r.Header.Get("User-Agent") == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

func (f *Fake) recycleList(w http.ResponseWriter, r *http.Request) {
	list := []any{}
	for id, nodes := range f.recycle {
		m := f.raw(nodes[0], false)
		m["fs_id"], m["leftTime"] = id, 10
		list = append(list, m)
	}
	reply(w, map[string]any{"errno": 0, "list": list})
}

func (f *Fake) restore(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	json.Unmarshal([]byte(r.Form.Get("fidlist")), &ids)
	for _, id := range ids {
		for _, n := range f.recycle[id] {
			f.nodes[key(n.path)] = n
		}
		delete(f.recycle, id)
	}
	reply(w, map[string]any{"errno": 0, "faillist": []any{}})
}

func (f *Fake) addTask(w http.ResponseWriter, r *http.Request) {
	id := f.id()
	f.tasks[id] = r.Form.Get("source_url")
	reply(w, map[string]any{"task_id": id})
}

func (f *Fake) listTasks(w http.ResponseWriter, r *http.Request) {
	list := []any{}
	for id, src := range f.tasks {
		list = append(list, map[string]any{"task_id": strconv.FormatInt(id, 10), "source_url": src, "status": "1"})
	}
	reply(w, map[string]any{"task_info": list, "total": len(list)})
}

func (f *Fake) deleteTask(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.Form.Get("task_id"), 10, 64)
	delete(f.tasks, id)
	reply(w, map[string]any{"request_id": 1})
}
