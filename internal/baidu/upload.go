package baidu

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"net/url"
	"path"
	"strconv"
	"strings"
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

// Upload is an upload session opened by Precreate.
type Upload struct {
	ID    string // upload id; pass it to Precreate again to resume
	Rapid *File  // set when Baidu already had the content and the file is done
}

// Precreate opens an upload session for path. If Baidu already stores the
// content (identified by hashes and a sample read from r), the file is
// created at once and Upload.Rapid is set. resumeID continues an earlier session.
func (c *Client) Precreate(ctx context.Context, p string, h Hashes, r io.ReaderAt, overwrite bool, resumeID string) (Upload, error) {
	uk, err := c.UK(ctx)
	if err != nil {
		return Upload{}, err
	}
	now := time.Now().Unix()
	off := dataOffset(uk, h.ContentMD5, now, h.Size)
	sample := make([]byte, 4096)
	n, err := r.ReadAt(sample, off)
	if err != nil && err != io.EOF {
		return Upload{}, &Error{Op: "上传 " + p, Err: err}
	}
	blocks, _ := json.Marshal(h.Blocks)
	ts := strconv.FormatInt(now, 10)
	form := url.Values{
		"path":         {p},
		"target_path":  {path.Dir(p) + "/"},
		"size":         {strconv.FormatInt(h.Size, 10)},
		"isdir":        {"0"},
		"autoinit":     {"1"},
		"rtype":        {rtype(overwrite)},
		"checkexist":   {"0"},
		"mode":         {"1"},
		"local_mtime":  {ts},
		"local_ctime":  {ts},
		"data_time":    {ts},
		"content-md5":  {h.ContentMD5},
		"slice-md5":    {h.SliceMD5},
		"block_list":   {string(blocks)},
		"data_offset":  {strconv.FormatInt(off, 10)},
		"data_length":  {strconv.Itoa(n)},
		"data_content": {base64.RawStdEncoding.EncodeToString(sample[:n])},
	}
	if resumeID != "" {
		form.Set("uploadid", resumeID)
	}
	var resp struct {
		ReturnType int     `json:"return_type"`
		UploadID   string  `json:"uploadid"`
		Info       rawFile `json:"info"`
	}
	if err := c.do(ctx, &request{op: "上传 " + p, url: panBase + "api/precreate", form: form, ua: uaNetdisk}, &resp); err != nil {
		return Upload{}, err
	}
	if resp.ReturnType == 2 {
		f := resp.Info.file()
		f.MD5 = h.ContentMD5
		return Upload{Rapid: &f}, nil
	}
	return Upload{ID: resp.UploadID}, nil
}

// UploadHost picks a server to send blocks to.
func (c *Client) UploadHost(ctx context.Context) (string, error) {
	var resp struct {
		Servers []struct {
			Server string `json:"server"`
		} `json:"servers"`
	}
	q := url.Values{"app_id": {"250528"}, "upload_version": {"2.0"}}
	if err := c.do(ctx, &request{op: "获取上传服务器", url: pcsURL("file", "locateupload", q)}, &resp); err != nil {
		return "", err
	}
	var hosts []string
	for _, s := range resp.Servers {
		// regional nodes such as bjdd-ct11.pcs.baidu.com; skip the generic ones
		if u, err := url.Parse(s.Server); err == nil && u.Scheme == "https" && strings.Contains(u.Host, "-") {
			hosts = append(hosts, u.Host)
		}
	}
	if len(hosts) == 0 {
		return "pcs.baidu.com", nil
	}
	return hosts[rand.IntN(len(hosts))], nil
}

// UploadBlock sends block seq of the session and returns its md5.
func (c *Client) UploadBlock(ctx context.Context, host, p string, up Upload, seq int, offset int64, data []byte) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("uploadedfile", "")
	part.Write(data)
	w.Close()
	q := url.Values{
		"app_id": {"266719"}, "method": {"upload"}, "type": {"tmpfile"}, "vip": {"1"},
		"path": {p}, "uploadid": {up.ID},
		"partseq": {strconv.Itoa(seq)}, "partoffset": {strconv.FormatInt(offset, 10)},
	}
	var resp struct {
		MD5 string `json:"md5"`
	}
	err := c.do(ctx, &request{
		op:     "上传 " + p,
		method: "POST",
		url:    "https://" + host + "/rest/2.0/pcs/superfile2?" + q.Encode(),
		body:   body.Bytes(),
		ctype:  w.FormDataContentType(),
	}, &resp)
	return resp.MD5, err
}

// CreateFile commits the uploaded blocks (their md5s in order) as the file.
func (c *Client) CreateFile(ctx context.Context, p string, size int64, up Upload, blocks []string, overwrite bool) (File, error) {
	list, _ := json.Marshal(blocks)
	form := url.Values{
		"path":        {p},
		"target_path": {path.Dir(p)},
		"size":        {strconv.FormatInt(size, 10)},
		"isdir":       {"0"},
		"rtype":       {rtype(overwrite)},
		"uploadid":    {up.ID},
		"block_list":  {string(list)},
	}
	var resp rawFile
	if err := c.do(ctx, &request{op: "上传 " + p, url: panBase + "api/create", form: form, ua: uaNetdisk}, &resp); err != nil {
		return File{}, err
	}
	return resp.file(), nil
}

// rtype tells Baidu what to do when the path is taken: 3 overwrites, 0 fails.
func rtype(overwrite bool) string {
	if overwrite {
		return "3"
	}
	return "0"
}
