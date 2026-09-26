package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	sums := "abc  bnd-v1-linux-amd64.tar.gz\ndef *bnd-v1-darwin-arm64.tar.gz\n"
	if !hasChecksum(sums, "def", "bnd-v1-darwin-arm64.tar.gz") || hasChecksum(sums, "abc", "bnd-v1-darwin-arm64.tar.gz") {
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
	tw.WriteHeader(&tar.Header{Name: "bnd-v1/bnd", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	bin, err := extract(buf.Bytes())
	if err != nil || string(bin) != "new binary" {
		t.Fatal(string(bin), err)
	}
	exe := filepath.Join(t.TempDir(), "bnd")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := replace(exe, bin); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new binary" {
		t.Fatal(string(got))
	}
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatal("old binary left behind")
	}
}
