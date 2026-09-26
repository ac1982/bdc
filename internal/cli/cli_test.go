package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ac1982/baidunetdisk-cli/internal/baidutest"
	"github.com/ac1982/baidunetdisk-cli/internal/config"
)

// testApp is an App logged in to a fake Baidu, with its own config dir.
type testApp struct {
	*App
	fake           *baidutest.Fake
	stdout, stderr bytes.Buffer
}

func newTestApp(t *testing.T) *testApp {
	t.Setenv(config.EnvDir, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings.SaveDir = t.TempDir()
	cfg.Put(config.Account{UID: baidutest.UID, Name: baidutest.Name, Cookies: baidutest.Cookies})
	null, _ := os.Open(os.DevNull)
	t.Cleanup(func() { null.Close() })
	ta := &testApp{fake: baidutest.New()}
	ta.App = &App{ctx: context.Background(), cfg: cfg, stdout: &ta.stdout, stderr: &ta.stderr, stdin: null,
		transport: ta.fake.Client().Transport}
	return ta
}

// run executes a command line and returns the exit code; output accumulates.
func (ta *testApp) run(args ...string) int {
	ta.stdout.Reset()
	ta.stderr.Reset()
	return ta.exec(args)
}

// json runs a command with --json and decodes its document.
func (ta *testApp) json(t *testing.T, args ...string) (int, map[string]any) {
	t.Helper()
	code := ta.run(append(args, "--json")...)
	var doc map[string]any
	if err := json.Unmarshal(ta.stdout.Bytes(), &doc); err != nil {
		t.Fatalf("bnd %s: stdout is not one JSON document: %v\n%s", strings.Join(args, " "), err, ta.stdout.String())
	}
	return code, doc
}

func paths(doc map[string]any, key string) []string {
	var out []string
	for _, f := range doc[key].([]any) {
		out = append(out, f.(map[string]any)["path"].(string))
	}
	return out
}

func TestGlobs(t *testing.T) {
	ta := newTestApp(t)
	for _, p := range []string{"/d/[1].txt", "/d/1.txt", "/d/a/x.txt", "/d/b/x.txt", "/d/c/y.txt"} {
		ta.fake.Put(p, []byte(p), 1)
	}
	for _, c := range []struct {
		arg  string
		want []string
	}{
		{"/d/[1].txt", []string{"/d/[1].txt"}},               // an exact name means itself
		{`/d/\[1\].txt`, []string{"/d/[1].txt"}},             // escaped
		{"/d/[0-9].txt", []string{"/d/1.txt"}},               // a pattern otherwise
		{"/d/*/x.txt", []string{"/d/a/x.txt", "/d/b/x.txt"}}, // only paths that exist
	} {
		code, doc := ta.json(t, "ls", c.arg)
		if got := paths(doc, "files"); code != 0 || strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("ls %s = %d %v, want %v", c.arg, code, got, c.want)
		}
	}
	if code, doc := ta.json(t, "ls", "/d/*.zip"); code != 2 || doc["error"].(map[string]any)["kind"] != "input" {
		t.Errorf("no match: %d %v", code, doc)
	}
	// rm of a bracketed name removes that file only
	if code := ta.run("rm", "/d/[1].txt"); code != 0 || ta.fake.Exists("/d/[1].txt") || !ta.fake.Exists("/d/1.txt") {
		t.Errorf("rm [1].txt: %d, [1].txt exists %v, 1.txt exists %v", code, ta.fake.Exists("/d/[1].txt"), ta.fake.Exists("/d/1.txt"))
	}
}

func TestLsKeepsPartialResults(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/d/a.txt", []byte("a"), 1)
	code, doc := ta.json(t, "ls", "/d", "/nope")
	if code != 2 || strings.Join(paths(doc, "files"), ",") != "/d/a.txt" {
		t.Errorf("%d %v", code, doc)
	}
}

func TestMD5OnlyWhenReal(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/one.bin", []byte("x"), 1)
	ta.fake.Put("/multi.bin", bytes.Repeat([]byte("y"), 9<<20), 3)
	_, doc := ta.json(t, "meta", "/one.bin", "/multi.bin")
	files := doc["files"].([]any)
	if _, ok := files[0].(map[string]any)["md5"]; !ok {
		t.Error("single-block file lost its md5")
	}
	if md5, ok := files[1].(map[string]any)["md5"]; ok {
		t.Errorf("multi-block file reports md5 %v", md5)
	}
}

func TestMkdirIsRepeatable(t *testing.T) {
	ta := newTestApp(t)
	if code := ta.run("mkdir", "/a/b"); code != 0 {
		t.Fatal(ta.stderr.String())
	}
	code, doc := ta.json(t, "mkdir", "/a/b")
	if code != 0 || len(doc["existing"].([]any)) != 1 {
		t.Errorf("mkdir again: %d %v", code, doc)
	}
	ta.fake.Put("/f", []byte("x"), 1)
	if code := ta.run("mkdir", "/f"); code != 2 {
		t.Errorf("mkdir over a file: %d", code)
	}
}

func TestCopyMove(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/a/x.txt", []byte("a"), 1)
	ta.fake.Put("/b/x.txt", []byte("b"), 1)
	ta.fake.Put("/c.txt", []byte("c"), 1)
	if code := ta.run("cp", "/a/x.txt", "/c.txt", "/new"); code != 0 || !ta.fake.Exists("/new/x.txt") || !ta.fake.Exists("/new/c.txt") {
		t.Errorf("cp into a new dir: %d %s", code, ta.stderr.String())
	}
	if code := ta.run("cp", "/a/x.txt", "/b/x.txt", "/other"); code != 2 || ta.fake.Exists("/other") {
		t.Errorf("cp of two same-named files: %d", code)
	}
	if code := ta.run("mv", "/c.txt", "/a/x.txt"); code != 2 {
		t.Errorf("mv onto an existing file: %d", code)
	}
	if code := ta.run("mv", "/c.txt", "/renamed.txt"); code != 0 || !ta.fake.Exists("/renamed.txt") || ta.fake.Exists("/c.txt") {
		t.Errorf("rename: %d", code)
	}
	if code := ta.run("cp", "/nope", "/x"); code != 2 {
		t.Errorf("cp missing: %d", code)
	}
}

func TestUploadDownloadRoundTrip(t *testing.T) {
	ta := newTestApp(t)
	src := t.TempDir()
	big := bytes.Repeat([]byte("0123456789"), 900_000) // three upload blocks
	write := func(name string, data []byte) {
		p := filepath.Join(src, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, data, 0o644)
	}
	write("up/big.bin", big)
	write("up/sub/small.txt", []byte("small"))
	write("up/empty.txt", nil)
	if runtime.GOOS != "windows" {
		os.Symlink(filepath.Join(src, "up", "sub"), filepath.Join(src, "up", "link")) // a symlinked dir: skipped
	}

	code, doc := ta.json(t, "upload", filepath.Join(src, "up"), "/r")
	if code != 0 {
		t.Fatalf("upload: %d %s", code, ta.stdout.String())
	}
	sum := doc["summary"].(map[string]any)
	if sum["uploaded"] != 3.0 {
		t.Errorf("summary %v", sum)
	}
	if !bytes.Equal(ta.fake.Data("/r/up/big.bin"), big) {
		t.Error("uploaded content differs")
	}
	if code, doc := ta.json(t, "upload", filepath.Join(src, "up"), "/r"); code != 0 || doc["summary"].(map[string]any)["skipped"] == nil {
		t.Errorf("upload again: %d %v", code, doc)
	}

	dl := t.TempDir()
	if code := ta.run("download", "-o", dl, "/r/up"); code != 0 {
		t.Fatalf("download: %s", ta.stderr.String())
	}
	if got, _ := os.ReadFile(filepath.Join(dl, "up", "big.bin")); !bytes.Equal(got, big) {
		t.Error("downloaded content differs")
	}
	code, doc = ta.json(t, "download", "-o", dl, "/r/up")
	if code != 0 || doc["summary"].(map[string]any)["skipped"] != 3.0 {
		t.Errorf("download again: %d %v", code, doc["summary"])
	}
}

func TestDownloadPlanning(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/a/same.txt", []byte("a"), 1)
	ta.fake.Put("/b/same.txt", []byte("b"), 1)
	dl := t.TempDir()

	// Two files for one local path: the second fails, the first is intact.
	code, doc := ta.json(t, "download", "-o", dl, "/a/same.txt", "/b/same.txt")
	if code != 2 || doc["summary"].(map[string]any)["failed"] != 1.0 {
		t.Errorf("collision: %d %v", code, doc)
	}
	if got, _ := os.ReadFile(filepath.Join(dl, "same.txt")); string(got) != "a" {
		t.Errorf("content %q", got)
	}

	// A path that cannot be planned fails alone; the rest still downloads.
	code, doc = ta.json(t, "download", "-o", t.TempDir(), "/a/same.txt", "/nope")
	sum := doc["summary"].(map[string]any)
	if code != 2 || sum["downloaded"] != 1.0 || sum["failed"] != 1.0 {
		t.Errorf("partial: %d %v", code, doc)
	}
}

func TestExpiredLogin(t *testing.T) {
	ta := newTestApp(t)
	ta.cfg.Current().Cookies = "BDUSS=expired"
	for _, args := range [][]string{{"quota"}, {"meta", "/"}, {"ls", "/"}, {"rm", "/x"}, {"download", "/x"}} {
		code, doc := ta.json(t, args...)
		if code != 4 || doc["error"].(map[string]any)["kind"] != "auth" {
			t.Errorf("%v: %d %v", args, code, doc)
		}
	}
	if code, _ := ta.json(t, "login", "--cookies", "BDUSS=bad"); code != 4 {
		t.Errorf("login with bad cookies: %d", code)
	}
}

func TestJSONContract(t *testing.T) {
	ta := newTestApp(t)
	if code := ta.run("who", "--json=true"); code != 0 || !json.Valid(ta.stdout.Bytes()) {
		t.Errorf("--json=true: %s", ta.stdout.String())
	}
	if code, doc := ta.json(t, "ls", "--nope"); code != 64 || doc["error"].(map[string]any)["kind"] != "usage" {
		t.Errorf("unknown flag: %d %v", code, doc)
	}
	if code, doc := ta.json(t, "logout"); code != 64 || doc["ok"] != false {
		t.Errorf("logout without a terminal: %d %v", code, doc)
	}
	if code, doc := ta.json(t, "config"); code != 0 || strings.Contains(ta.stdout.String(), "fake-bduss") || doc["settings"] == nil {
		t.Errorf("config: %d %s", code, ta.stdout.String())
	}
}
