package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v1.1.0", "v1.0.0", true},
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "v1.2.0", false},
		{"v1.0.0", "dev", true},
		{"garbage", "dev", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestHasChecksum(t *testing.T) {
	sums := "abc  bdc-v1-linux-amd64.tar.gz\ndef *bdc-v1-darwin-arm64.tar.gz\n"
	if !hasChecksum(sums, "def", "bdc-v1-darwin-arm64.tar.gz") || hasChecksum(sums, "abc", "bdc-v1-darwin-arm64.tar.gz") {
		t.Fatal("wrong match")
	}
}

func TestExtract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz path only")
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("new binary")
	tw.WriteHeader(&tar.Header{Name: "bdc-v1/bdc", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	if bin, err := extract(buf.Bytes()); err != nil || string(bin) != "new binary" {
		t.Fatal(string(bin), err)
	}
}

// Replacing touches only the files the update made, on every system: not
// the user's files that happen to look like ours, not in a directory whose
// name looks like a pattern.
func TestReplace(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "[a]")
	os.MkdirAll(dir, 0o755)
	os.MkdirAll(filepath.Join(root, "a"), 0o755)
	exe := filepath.Join(dir, "bdc.exe")
	os.WriteFile(exe, []byte("old"), 0o755)
	mine := []string{
		exe + ".old",
		filepath.Join(dir, ".bdc-old-user-backup"),
		filepath.Join(root, "a", ".bdc-old-backup"),
	}
	for _, p := range mine {
		os.WriteFile(p, []byte("keep"), 0o600)
	}
	if runtime.GOOS != "windows" {
		os.Symlink(filepath.Join(dir, "elsewhere"), exe+".new")
	}
	tmp, err := os.CreateTemp(dir, ".bdc-update-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := replace(exe, tmp, []byte("new binary")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new binary" {
		t.Fatal(string(got))
	}
	if fi, _ := os.Stat(exe); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", fi.Mode())
	}
	for _, p := range mine {
		if b, _ := os.ReadFile(p); string(b) != "keep" {
			t.Errorf("%s was touched", p)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "elsewhere")); err == nil {
		t.Error("wrote through the user's bdc.new symlink")
	}
	left, _ := os.ReadDir(dir)
	for _, e := range left {
		if strings.HasPrefix(e.Name(), ".bdc-update-") || e.Name() != ".bdc-old-user-backup" && strings.HasPrefix(e.Name(), ".bdc-old-") {
			t.Errorf("left behind: %s", e.Name()) // the old file is not running here, so it goes too
		}
	}
}

// Without write access to the executable's directory nothing is downloaded.
func TestInstallNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory this user cannot write")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "bdc")
	os.WriteFile(exe, []byte("old"), 0o755)
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)
	r := &Release{archive: "http://127.0.0.1:1/never", sums: "http://127.0.0.1:1/never"}
	if err := r.install(context.Background(), exe); !errors.Is(err, ErrNotWritable) {
		t.Fatal(err)
	}
}
