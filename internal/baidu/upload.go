package baidu

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/url"
	"path"
	"strconv"
	"time"
)

// BlockSize is the size of upload blocks for a file of the given size.
func BlockSize(size int64) int64 {
	switch {
	case size >= 32<<30:
		return 64 << 20
	case size >= 8<<30:
		return 16 << 20
	}
	return 4 << 20
}

// Hashes identify a local file's content to the upload API.
type Hashes struct {
	Size       int64
	ContentMD5 string   // whole file
	SliceMD5   string   // first 256 KiB
	Blocks     []string // md5 of each BlockSize block
}

// RapidUpload creates the file at once if Baidu already stores its content
// (秒传), and reports whether it did. Baidu checks a 256 KiB sample of the
// content, read from r; smaller files are not tried, as the web app does not.
func (c *Client) RapidUpload(ctx context.Context, p string, h Hashes, r io.ReaderAt, overwrite bool) (*File, error) {
	if h.Size < rapidWindow {
		return nil, nil
	}
	me, err := c.whoami(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	content := obfuscateMD5(h.ContentMD5)
	off := rapidOffset(me.user.UK, content, now, h.Size)
	sample := make([]byte, rapidWindow)
	n, err := r.ReadAt(sample, off)
	if err != nil && err != io.EOF {
		return nil, &Error{Op: "上传 " + p, Err: err}
	}
	ts := strconv.FormatInt(now, 10)
	form := url.Values{
		"path":           {p},
		"content-length": {strconv.FormatInt(h.Size, 10)},
		"content-md5":    {content},
		"slice-md5":      {obfuscateMD5(h.SliceMD5)},
		"target_path":    {path.Dir(p) + "/"},
		"local_mtime":    {ts},
		"data_time":      {ts},
		"data_offset":    {strconv.FormatInt(off, 10)},
		"data_content":   {base64.StdEncoding.EncodeToString(sample[:n])},
	}
	var resp struct {
		Info rawFile `json:"info"`
	}
	err = c.do(ctx, &request{op: "上传 " + p, path: "api/rapidupload", query: url.Values{"rtype": {rtype(overwrite)}, "channel": {"chunlei"}}, write: true, form: form}, &resp)
	if Code(err) == 404 { // content unknown to Baidu
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f := resp.Info.file()
	f.MD5 = h.ContentMD5
	return &f, nil
}

// Upload is an upload session opened by Precreate.
type Upload struct {
	ID string // upload id; pass it to Precreate again to resume
}

// Precreate opens an upload session for the file's blocks, or resumes the
// session resumeID.
func (c *Client) Precreate(ctx context.Context, p string, h Hashes, overwrite bool, resumeID string) (Upload, error) {
	blocks, _ := json.Marshal(h.Blocks)
	form := url.Values{
		"path": {p}, "target_path": {path.Dir(p) + "/"}, "autoinit": {"1"},
		"block_list": {string(blocks)}, "local_mtime": {strconv.FormatInt(time.Now().Unix(), 10)},
	}
	if resumeID != "" {
		form.Set("uploadid", resumeID)
	}
	var resp struct {
		UploadID string `json:"uploadid"`
	}
	err := c.do(ctx, &request{op: "上传 " + p, path: "api/precreate", query: url.Values{"rtype": {rtype(overwrite)}, "channel": {"chunlei"}}, write: true, form: form}, &resp)
	return Upload{ID: resp.UploadID}, err
}

// UploadHost picks the server to send blocks to.
func (c *Client) UploadHost(ctx context.Context) (string, error) {
	var resp struct {
		Server []string `json:"server"`
	}
	if err := c.do(ctx, &request{op: "获取上传服务器", path: "https://d.pcs.baidu.com/rest/2.0/pcs/file", query: url.Values{"method": {"locateupload"}}}, &resp); err != nil {
		return "", err
	}
	if len(resp.Server) == 0 {
		return "c.pcs.baidu.com", nil
	}
	return resp.Server[0], nil
}

// UploadBlock sends block seq of the session and returns its md5.
func (c *Client) UploadBlock(ctx context.Context, host, p string, up Upload, seq int, data []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", "blob")
	part.Write(data)
	w.Close()
	q := url.Values{
		"method": {"upload"}, "app_id": {"250528"}, "channel": {"chunlei"}, "web": {"1"}, "clienttype": {"0"},
		"path": {p}, "uploadid": {up.ID}, "uploadsign": {"0"}, "partseq": {strconv.Itoa(seq)},
	}
	var resp struct {
		MD5 string `json:"md5"`
	}
	err := c.do(ctx, &request{op: "上传 " + p, method: "POST", path: "https://" + host + "/rest/2.0/pcs/superfile2", query: q,
		body: body.Bytes(), ctype: w.FormDataContentType()}, &resp)
	return resp.MD5, err
}

// CreateFile commits the uploaded blocks (their md5s in order) as the file.
func (c *Client) CreateFile(ctx context.Context, p string, size int64, up Upload, blocks []string, overwrite bool) (File, error) {
	list, _ := json.Marshal(blocks)
	form := url.Values{
		"path": {p}, "size": {strconv.FormatInt(size, 10)}, "uploadid": {up.ID},
		"block_list": {string(list)}, "target_path": {path.Dir(p) + "/"},
		"local_mtime": {strconv.FormatInt(time.Now().Unix(), 10)},
	}
	var resp rawFile
	err := c.do(ctx, &request{op: "上传 " + p, path: "api/create", query: url.Values{"isdir": {"0"}, "rtype": {rtype(overwrite)}, "channel": {"chunlei"}}, write: true, form: form}, &resp)
	if err != nil {
		return File{}, err
	}
	return resp.file(), nil
}

// rtype tells Baidu what to do when the path is taken: 3 overwrites, 0 fails.
// (The web app sends 1, which renames the new file.)
func rtype(overwrite bool) string {
	if overwrite {
		return "3"
	}
	return "0"
}
