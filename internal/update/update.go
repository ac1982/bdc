// Package update installs newer releases of bnd from GitHub.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/mod/semver"
)

const repo = "ac1982/baidunetdisk-cli"

// Release is a published version with the archive for this platform.
type Release struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	archive string // download URL of the archive
	sums    string // download URL of checksums.txt
}

// Latest returns the newest release.
func Latest(ctx context.Context) (*Release, error) {
	var gh struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := getJSON(ctx, "https://api.github.com/repos/"+repo+"/releases/latest", &gh); err != nil {
		return nil, fmt.Errorf("查询最新版本: %w", err)
	}
	r := &Release{Version: gh.Tag, URL: gh.URL}
	want := fmt.Sprintf("bnd-%s-%s-%s.%s", gh.Tag, runtime.GOOS, runtime.GOARCH, archiveExt())
	for _, a := range gh.Assets {
		switch a.Name {
		case want:
			r.archive = a.URL
		case "checksums.txt":
			r.sums = a.URL
		}
	}
	if r.archive == "" || r.sums == "" {
		return r, fmt.Errorf("版本 %s 没有 %s 或 checksums.txt", gh.Tag, want)
	}
	return r, nil
}

// Newer reports whether version a is newer than b. Development builds
// ("dev") are older than any release.
func Newer(a, b string) bool {
	if !semver.IsValid(b) {
		return semver.IsValid(a)
	}
	return semver.Compare(a, b) > 0
}

// Install downloads the release, checks it against checksums.txt and
// replaces the running executable.
func (r *Release) Install(ctx context.Context) error {
	archive, err := get(ctx, r.archive)
	if err != nil {
		return fmt.Errorf("下载 %s: %w", r.archive, err)
	}
	sums, err := get(ctx, r.sums)
	if err != nil {
		return fmt.Errorf("下载 checksums.txt: %w", err)
	}
	sum := sha256.Sum256(archive)
	if !hasChecksum(string(sums), hex.EncodeToString(sum[:]), path.Base(r.archive)) {
		return errors.New("校验和不符, 已放弃安装")
	}
	bin, err := extract(archive)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	return replace(exe, bin)
}

func hasChecksum(sums, sum, name string) bool {
	for _, line := range strings.Split(sums, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == sum && strings.TrimPrefix(f[1], "*") == name {
			return true
		}
	}
	return false
}

// extract returns the bnd executable inside a .tar.gz or .zip archive.
func extract(archive []byte) ([]byte, error) {
	name := "bnd"
	if runtime.GOOS == "windows" {
		name = "bnd.exe"
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == name {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, errors.New("安装包中没有 " + name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, errors.New("安装包中没有 " + name)
		}
		if path.Base(h.Name) == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
}

// replace swaps the executable at exe for bin. The old file is moved aside
// first, which also works on Windows while it is running.
func replace(exe string, bin []byte) error {
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(old, exe)
		return err
	}
	os.Remove(old) // fails harmlessly on Windows; removed on the next update
	return nil
}

func archiveExt() string {
	if runtime.GOOS == "windows" {
		return "zip"
	}
	return "tar.gz"
}

func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func getJSON(ctx context.Context, url string, v any) error {
	data, err := get(ctx, url)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
