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

func TestExtractAndReplace(t *testing.T) {
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
	bin, err := extract(buf.Bytes())
	if err != nil || string(bin) != "new binary" {
		t.Fatal(string(bin), err)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "bdc")
	os.WriteFile(exe, []byte("old"), 0o755)
	// Files that happen to have the names earlier versions used are the user's.
	os.WriteFile(exe+".old", []byte("backup"), 0o600)
	os.Symlink(filepath.Join(dir, "elsewhere"), exe+".new")
	tmp, err := os.CreateTemp(dir, ".bdc-update-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := replace(exe, tmp, bin); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new binary" {
		t.Fatal(string(got))
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", fi.Mode())
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "backup" {
		t.Error("the user's bdc.old was touched")
	}
	if _, err := os.Stat(filepath.Join(dir, "elsewhere")); err == nil {
		t.Error("wrote through the user's bdc.new symlink")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".bdc-*")); len(left) > 0 {
		t.Errorf("left behind: %v", left)
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
