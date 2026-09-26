package transfer

import (
	"bytes"
	"context"
	"errors"
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
	saveJSON(dest+PartSuffix+".json", record{Owner: recordOwner, ID: "v1", Size: int64(len(content)), Done: []bool{true, false, true}})

	d := &Download{Client: http.DefaultClient, ID: "v1", Size: int64(len(content)), Dest: dest, Conns: 4,
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

// A record is only trusted with its own part file and the same remote version.
func TestDownloadDiscardsStaleRecords(t *testing.T) {
	content := payload(chunkSize + 10)
	srv := serve(t, content)
	for name, setup := range map[string]func(dest string){
		"part missing": func(dest string) {},
		"part truncated": func(dest string) {
			os.WriteFile(dest+PartSuffix, content[:10], 0o644)
		},
		"other version": func(dest string) {
			os.WriteFile(dest+PartSuffix, make([]byte, len(content)), 0o644)
			saveJSON(dest+PartSuffix+".json", record{Owner: recordOwner, ID: "old", Size: int64(len(content)), Done: []bool{true, true}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "f")
			saveJSON(dest+PartSuffix+".json", record{Owner: recordOwner, ID: "v2", Size: int64(len(content)), Done: []bool{true, true}})
			setup(dest)
			d := &Download{Client: http.DefaultClient, ID: "v2", Size: int64(len(content)), Dest: dest,
				URLs: func(context.Context) ([]string, error) { return []string{srv.URL}, nil }}
			if err := d.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(dest); !bytes.Equal(got, content) {
				t.Fatal("content differs")
			}
		})
	}
}

func TestDownloadRejectsWrongRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 4-7/8")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("EFGH"))
	}))
	defer srv.Close()
	d := &Download{Client: http.DefaultClient, Size: 8, Dest: filepath.Join(t.TempDir(), "f")}
	f, _ := os.Create(d.Dest)
	defer f.Close()
	if _, err := d.fetchRange(context.Background(), f, srv.URL, 0, 4); err == nil {
		t.Fatal("accepted a range that was not asked for")
	}
}

// Files bdc did not create are never overwritten, whatever their names.
func TestDownloadLeavesOthersFilesAlone(t *testing.T) {
	srv := serve(t, []byte("new content"))
	for name, setup := range map[string]func(dest string) string{
		"user's part file": func(dest string) string {
			os.WriteFile(dest+PartSuffix, []byte("mine"), 0o644)
			return dest + PartSuffix
		},
		"user's json file": func(dest string) string {
			os.WriteFile(dest+PartSuffix+".json", []byte(`{"id":"x"}`), 0o644)
			return dest + PartSuffix + ".json"
		},
	} {
		t.Run(name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "f")
			theirs := setup(dest)
			before, _ := os.ReadFile(theirs)
			d := &Download{Client: http.DefaultClient, Size: 11, Dest: dest,
				URLs: func(context.Context) ([]string, error) { return []string{srv.URL}, nil }}
			if err := d.Run(context.Background()); !errors.Is(err, ErrOccupied) {
				t.Fatalf("err = %v", err)
			}
			if after, _ := os.ReadFile(theirs); !bytes.Equal(before, after) {
				t.Fatal("their file changed")
			}
		})
	}
}

// A response longer than asked for must fail the download, through the
// retries, and leave no finished file.
func TestDownloadRunRejectsLongBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		w.Write([]byte("abcdefgh"))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "f")
	d := &Download{Client: http.DefaultClient, Size: 4, Dest: dest, retryDelay: time.Millisecond,
		URLs: func(context.Context) ([]string, error) { return []string{srv.URL}, nil }}
	if err := d.Run(context.Background()); err == nil {
		t.Fatal("download of an overlong response succeeded")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("a file was installed")
	}
}

// Temporary files have unique names, so a user's file is never clobbered.
func TestDownloadKeepsUserTmpFile(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "f")
	os.WriteFile(dest+PartSuffix+".json.tmp", []byte("mine"), 0o644)
	d := &Download{Client: http.DefaultClient, Size: 3, Dest: dest,
		URLs: func(context.Context) ([]string, error) { return []string{serve(t, []byte("abc")).URL}, nil }}
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest + PartSuffix + ".json.tmp"); string(got) != "mine" {
		t.Fatalf("user's file is now %q", got)
	}
}

// A link that sends the wrong thing is passed over for the next one.
func TestDownloadSkipsBadLink(t *testing.T) {
	content := []byte("good content")
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(append(content, "and more"...)) // longer than the file
	}))
	defer bad.Close()
	good := serve(t, content)
	dest := filepath.Join(t.TempDir(), "f")
	d := &Download{Client: http.DefaultClient, Size: int64(len(content)), Dest: dest, retryDelay: time.Millisecond,
		URLs: func(context.Context) ([]string, error) { return []string{bad.URL, good.URL}, nil }}
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, content) {
		t.Fatalf("got %q", got)
	}
}

func TestDownloadRejectsLongBody(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"200 of unknown length": func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush() // chunked: no Content-Length
			w.Write([]byte("abcdefgh"))
		},
		"206 longer than its range": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", "bytes 0-3/4")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte("abcdefgh"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			d := &Download{Client: http.DefaultClient, Size: 4, Dest: filepath.Join(t.TempDir(), "f")}
			f, _ := os.Create(d.Dest)
			defer f.Close()
			if _, err := d.fetchRange(context.Background(), f, srv.URL, 0, 4); err == nil {
				t.Fatal("accepted a body longer than asked for")
			}
		})
	}
}

// A download whose final rename fails can be retried: its part stays bdc's.
func TestDownloadRetriesFailedInstall(t *testing.T) {
	srv := serve(t, []byte("abc"))
	dest := filepath.Join(t.TempDir(), "f")
	os.Mkdir(dest, 0o755) // a directory in the way: the rename fails
	d := &Download{Client: http.DefaultClient, Size: 3, Dest: dest,
		URLs: func(context.Context) ([]string, error) { return []string{srv.URL}, nil }}
	if err := d.Run(context.Background()); err == nil {
		t.Fatal("installed over a directory")
	}
	os.Remove(dest)
	if err := d.Run(context.Background()); err != nil {
		t.Fatal("retry:", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "abc" {
		t.Fatalf("got %q", got)
	}
}
