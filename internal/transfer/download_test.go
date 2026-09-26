package transfer

import (
	"bytes"
	"context"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func serve(t *testing.T, content []byte) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(content))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func payload(n int) []byte {
	b := make([]byte, n)
	r := rand.NewChaCha8([32]byte{1})
	r.Read(b)
	return b
}

func TestDownload(t *testing.T) {
	content := payload(3*chunkSize + 12345)
	good := serve(t, content)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer bad.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	var progress atomic.Int64
	d := &Download{
		Client:   http.DefaultClient,
		URLs:     func(context.Context) ([]string, error) { return []string{bad.URL, good.URL}, nil },
		Size:     int64(len(content)),
		Dest:     dest,
		Conns:    3,
		Progress: func(n int64) { progress.Add(n) },
	}
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, content) {
		t.Fatal("content differs")
	}
	if progress.Load() != int64(len(content)) {
		t.Fatalf("progress %d, want %d", progress.Load(), len(content))
	}
	if _, err := os.Stat(dest + PartSuffix + ".json"); !os.IsNotExist(err) {
		t.Fatal("resume record left behind")
	}
}

func TestDownloadResume(t *testing.T) {
	content := payload(2*chunkSize + 7)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(content))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.bin")
	// A previous run finished chunk 0 and 2.
	part := make([]byte, len(content))
	copy(part[:chunkSize], content[:chunkSize])
	copy(part[2*chunkSize:], content[2*chunkSize:])
	os.WriteFile(dest+PartSuffix, part, 0o644)
	saveJSON(dest+PartSuffix+".json", record{Size: int64(len(content)), Done: []bool{true, false, true}})

	d := &Download{Client: http.DefaultClient, Size: int64(len(content)), Dest: dest, Conns: 4,
		URLs: func(context.Context) ([]string, error) { return []string{srv.URL}, nil }}
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, content) {
		t.Fatal("content differs")
	}
	if n := requests.Load(); n != 1 {
		t.Fatalf("%d requests, want 1 (only the missing chunk)", n)
	}
}

func TestDownloadEmpty(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "empty")
	d := &Download{Client: http.DefaultClient, Dest: dest,
		URLs: func(context.Context) ([]string, error) { t.Fatal("no links needed"); return nil, nil }}
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dest); err != nil || fi.Size() != 0 {
		t.Fatal(fi, err)
	}
}

func TestDownloadRefreshesExpiredLinks(t *testing.T) {
	content := payload(1000)
	good := serve(t, content)
	var calls atomic.Int32
	d := &Download{Client: http.DefaultClient, Size: 1000, Dest: filepath.Join(t.TempDir(), "f"),
		URLs: func(context.Context) ([]string, error) {
			if calls.Add(1) == 1 {
				return []string{"http://127.0.0.1:1/expired"}, nil
			}
			return []string{good.URL}, nil
		}}
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("URLs called %d times", calls.Load())
	}
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
