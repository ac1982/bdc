package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

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
	cfg.Put(config.Account{UK: baidutest.UK, Name: baidutest.Name, Cookies: baidutest.Cookies})
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
		t.Fatalf("bdc %s: stdout is not one JSON document: %v\n%s", strings.Join(args, " "), err, ta.stdout.String())
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
	os.MkdirAll(filepath.Join(src, "up", "nothing", "here"), 0o755) // empty directories go up too
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
	if !ta.fake.Exists("/r/up/nothing/here") {
		t.Error("empty directory not created")
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

func TestCaseInsensitiveTargets(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/a/Readme.txt", []byte("a"), 1)
	ta.fake.Put("/b/README.txt", []byte("b"), 1)
	// Baidu ignores case: these two would land on one name.
	if code := ta.run("cp", "/a/Readme.txt", "/b/README.txt", "/new"); code != 2 || ta.fake.Exists("/new") {
		t.Errorf("cp of case variants: %d", code)
	}
	// An upload onto a case variant is the same file: skipped by default.
	src := filepath.Join(t.TempDir(), "readme.TXT")
	os.WriteFile(src, []byte("local"), 0o644)
	code, doc := ta.json(t, "upload", src, "/a")
	if item := doc["files"].([]any)[0].(map[string]any); code != 0 || item["status"] != "skipped" {
		t.Errorf("upload onto a case variant: %d %v", code, item)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" { // case-insensitive local file systems
		code, doc := ta.json(t, "download", "-o", t.TempDir(), "/a/Readme.txt", "/b/README.txt")
		if code != 2 || doc["summary"].(map[string]any)["failed"] != 1.0 {
			t.Errorf("download of case variants: %d %v", code, doc)
		}
	}
}

func TestDownloadNeverTouchesOthersFiles(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/d/x.bdc-part", []byte("a real file"), 1)
	ta.fake.Put("/d/x", []byte("x"), 1)
	dl := t.TempDir()
	code, doc := ta.json(t, "download", "-o", dl, "/d/x.bdc-part", "/d/x")
	if code != 2 || doc["summary"].(map[string]any)["downloaded"] != 1.0 {
		t.Errorf("%d %v", code, doc)
	}
	if got, _ := os.ReadFile(filepath.Join(dl, "x.bdc-part")); string(got) != "a real file" {
		t.Errorf("the downloaded x.bdc-part was clobbered: %q", got)
	}
	// A leftover file bdc did not create is not taken for a part file.
	dl2 := t.TempDir()
	os.WriteFile(filepath.Join(dl2, "x.bdc-part"), []byte("mine"), 0o644)
	if code, _ := ta.json(t, "download", "-o", dl2, "/d/x"); code != 2 {
		t.Errorf("download next to a foreign part file: %d", code)
	}
	if got, _ := os.ReadFile(filepath.Join(dl2, "x.bdc-part")); string(got) != "mine" {
		t.Errorf("foreign part file changed: %q", got)
	}
}

func TestDownloadFailuresStayLocal(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/dir/f", []byte("f"), 1)
	ta.fake.Put("/ok", []byte("ok"), 1)
	dl := t.TempDir()
	os.WriteFile(filepath.Join(dl, "dir"), []byte("a file where a directory must go"), 0o644)
	code, doc := ta.json(t, "download", "-o", dl, "/dir", "/ok", "/missing*")
	sum := doc["summary"].(map[string]any)
	if code != 2 || sum["downloaded"] != 1.0 || sum["failed"] != 2.0 {
		t.Errorf("%d %v", code, doc)
	}
	if got, _ := os.ReadFile(filepath.Join(dl, "ok")); string(got) != "ok" {
		t.Error("the independent file was not downloaded")
	}
}

func TestManyPaths(t *testing.T) {
	ta := newTestApp(t)
	for i := range 300 {
		ta.fake.Put(fmt.Sprintf("/many/很长的中文文件名-%03d.txt", i), []byte("x"), 1)
	}
	for i := range 1001 {
		ta.fake.Put(fmt.Sprintf("/big/%04d", i), nil, 1)
	}
	if _, doc := ta.json(t, "ls", "/big"); len(doc["files"].([]any)) != 1001 {
		t.Errorf("ls of 1001 entries: %d", len(doc["files"].([]any)))
	}
	if code := ta.run("rm", "/many/*"); code != 0 || ta.fake.Exists("/many/很长的中文文件名-299.txt") {
		t.Errorf("rm of 300 matches: %d %s", code, ta.stderr.String())
	}
}

func TestPartialMove(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/src/a", []byte("a"), 1)
	ta.fake.Put("/src/b", []byte("b"), 1)
	ta.fake.Put("/src/c", []byte("c"), 1)
	ta.fake.Put("/dst/b", []byte("old"), 1)
	ta.fake.Put("/dst/c", []byte("old"), 1) // existed before: not moved, though present
	code, doc := ta.json(t, "mv", "/src/a", "/src/b", "/src/c", "/dst")
	items, _ := doc["items"].([]any)
	if code != 2 || len(items) != 1 || items[0].(map[string]any)["from"] != "/src/a" {
		t.Errorf("partial move: %d %v", code, doc)
	}
}

func TestCaseOnlyRename(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/a.txt", []byte("a"), 1)
	if code := ta.run("mv", "/a.txt", "/A.txt"); code != 0 {
		t.Errorf("case-only rename: %d %s", code, ta.stderr.String())
	}
}

func TestCaseOnlyRenameIsInPlace(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/a.txt", []byte("a"), 1)
	if code := ta.run("mv", "/a.txt", "/A.txt"); code != 0 || ta.fake.Name("/a.txt") != "A.txt" {
		t.Errorf("%d %s name=%q", code, ta.stderr.String(), ta.fake.Name("/a.txt"))
	}
	if ta.fake.Requests["pan.baidu.com/api/filemanager"] != 1 {
		t.Errorf("%d file-manager calls, want one rename", ta.fake.Requests["pan.baidu.com/api/filemanager"])
	}
}

func TestDeepest(t *testing.T) {
	got := deepest([]string{"/a", "/a/b", "/a/b c", "/a/b/x", "/d", "/a/b/x"}, path.Dir)
	if want := []string{"/a/b c", "/a/b/x", "/d"}; !slices.Equal(got, want) {
		t.Errorf("deepest = %v, want %v", got, want)
	}
}

func TestLocalKey(t *testing.T) {
	composed, decomposed := "/x/caf\u00e9.txt", "/x/cafe\u0301.txt"
	same := localKey(composed) == localKey(decomposed) && localKey("/X/A") == localKey("/x/a")
	if want := runtime.GOOS == "darwin"; runtime.GOOS != "windows" && same != want {
		t.Errorf("darwin folds case and normalization, others neither: got same=%v", same)
	}
}

func TestBracketTargets(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/src", []byte("s"), 1)
	ta.fake.Mkdir("/[dest]")
	if code := ta.run("cp", "/src", "/[dest]"); code != 0 || !ta.fake.Exists("/[dest]/src") {
		t.Errorf("cp into [dest]: %d %s", code, ta.stderr.String())
	}
	if code := ta.run("mv", "/src", "/[1].txt"); code != 0 || !ta.fake.Exists("/[1].txt") {
		t.Errorf("mv to [1].txt: %d %s", code, ta.stderr.String())
	}
}

func TestShareNeedsLogin(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/f", []byte("f"), 1)
	ta.cfg.Current().Cookies = "BDUSS=expired"
	if code, _ := ta.json(t, "share", "create", "/f"); code != 4 {
		t.Errorf("share create with an expired login: %d", code)
	}
}

func TestUploadEmptyDirOntoFile(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/r/empty", []byte("a file"), 1)
	src := filepath.Join(t.TempDir(), "empty")
	os.Mkdir(src, 0o755)
	if code, doc := ta.json(t, "upload", src, "/r"); code != 2 {
		t.Errorf("empty dir onto a file: %d %v", code, doc)
	}
}

func TestRemoveReportsPartialBatches(t *testing.T) {
	ta := newTestApp(t)
	for i := range 501 {
		ta.fake.Put(fmt.Sprintf("/d/%03d", i), nil, 1)
	}
	ta.fake.Fail = func(endpoint string, n int) any {
		if endpoint == "pan.baidu.com/api/filemanager" && n == 2 {
			return map[string]any{"errno": 132}
		}
		return nil
	}
	code, doc := ta.json(t, "rm", "/d/*")
	if removed, _ := doc["removed"].([]any); code == 0 || len(removed) != 500 {
		t.Errorf("%d, removed %d", code, len(removed))
	}
}

func TestRecycle(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/d/f.txt", []byte("f"), 1)
	ta.run("rm", "/d")
	_, doc := ta.json(t, "recycle", "list")
	files := doc["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("recycle bin: %v", doc)
	}
	id := fmt.Sprint(int64(files[0].(map[string]any)["fsId"].(float64)))
	if code := ta.run("recycle", "restore", id); code != 0 || !ta.fake.Exists("/d/f.txt") {
		t.Errorf("restore: %d %s", code, ta.stderr.String())
	}
	ta.run("rm", "/d")
	if code := ta.run("recycle", "delete", "-y", id); code != 0 {
		t.Errorf("purge: %d %s", code, ta.stderr.String())
	}
	if _, doc := ta.json(t, "recycle", "list"); len(doc["files"].([]any)) != 0 {
		t.Errorf("still in the bin: %v", doc)
	}
}

// Baidu's security check: without a terminal the change fails (auth); at
// one, the person picks where the code goes and types it, and the change
// goes on.
func TestSecurityCheck(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/d/a", nil, 1)
	ta.fake.Guarded = true
	code, doc := ta.json(t, "rm", "/d/a")
	if e, _ := doc["error"].(map[string]any); code != 4 || e["code"] != 132.0 || !strings.Contains(e["message"].(string), "终端") {
		t.Fatalf("without a terminal: %d %v", code, doc)
	}

	in := filepath.Join(t.TempDir(), "in")
	os.WriteFile(in, []byte("3\n\n111111\n"+baidutest.VerifyCode+"\n"), 0o600) // a bad choice, the default, a wrong code, the code
	ta.stdin, _ = os.Open(in)
	defer ta.stdin.Close()
	client, _ := ta.baidu()
	client.SetVerifier(ta.verify)
	if code := ta.run("rm", "/d/a"); code != 0 || ta.fake.Exists("/d/a") {
		t.Fatalf("with the code: %d %s", code, ta.stderr.String())
	}
	for _, want := range []string{"1) 短信 138*****000", "验证码已发送到 138*****000", "验证码错误", "验证通过"} {
		if !strings.Contains(ta.stderr.String(), want) {
			t.Errorf("dialog lacks %q:\n%s", want, ta.stderr.String())
		}
	}
	if strings.Contains(ta.stderr.String(), "邮箱") { // not bound: not offered
		t.Errorf("offers an unbound email:\n%s", ta.stderr.String())
	}
	if n := ta.fake.Requests["pan.baidu.com/api/authwidget?send"]; n != 1 {
		t.Errorf("sent %d codes", n)
	}
}

func TestOffline(t *testing.T) {
	ta := newTestApp(t)
	_, doc := ta.json(t, "offline", "add", "--to", "/dl", "https://example.com/a.iso")
	ids := doc["ids"].([]any)
	if len(ids) != 1 {
		t.Fatalf("add: %v", doc)
	}
	if _, doc := ta.json(t, "offline", "list"); len(doc["tasks"].([]any)) != 1 {
		t.Errorf("list: %v", doc)
	}
	if code := ta.run("offline", "delete", fmt.Sprint(int64(ids[0].(float64)))); code != 0 {
		t.Errorf("delete: %s", ta.stderr.String())
	}
}

func TestShareCreate(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/f.txt", []byte("f"), 1)
	code, doc := ta.json(t, "share", "create", "-p", "abcd", "/f.txt")
	if code != 0 || doc["pwd"] != "abcd" || !strings.HasSuffix(doc["url"].(string), "?pwd=abcd") {
		t.Errorf("%d %v", code, doc)
	}
	if code, _ := ta.json(t, "share", "create", "/nope"); code != 2 {
		t.Errorf("share of a missing path: %d", code)
	}
}

// A copy onto an existing file fails (the synchronous form of the API
// reported success for it without copying).
func TestCopyOntoExistingFails(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/x/a.txt", []byte("old"), 1)
	ta.fake.Put("/y/a.txt", []byte("new"), 1)
	code, doc := ta.json(t, "cp", "/y/a.txt", "/x")
	if items, _ := doc["items"].([]any); code != 2 || len(items) != 0 || string(ta.fake.Data("/x/a.txt")) != "old" {
		t.Errorf("%d %v", code, doc)
	}
}

func TestSearchDepth(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/d/mid.bin", nil, 1)
	ta.fake.Put("/d/sub/mid.bin", nil, 1)
	_, doc := ta.json(t, "search", "--path", "/d", "mid")
	if got := paths(doc, "files"); strings.Join(got, ",") != "/d/mid.bin" {
		t.Errorf("search in /d: %v", got)
	}
	_, doc = ta.json(t, "search", "-r", "--path", "/d", "mid")
	if got := paths(doc, "files"); len(got) != 2 {
		t.Errorf("search -r: %v", got)
	}
}

// The shell's history never keeps a login line: it holds the cookies.
func TestShellHistoryOmitsLogin(t *testing.T) {
	ta := newTestApp(t)
	in := filepath.Join(t.TempDir(), "in")
	lines := []string{
		`login --cookies "BDUSS=SECRET1" --bogus`,
		`--json login --cookies "BDUSS=SECRET2"`,
		`--json=true login --cookies "BDUSS=SECRET3" --bogus`,
		`logn --cookies "BDUSS=SECRET4"`, // does not parse: not kept either
		`login --cookies "BDUSS=SECRET5" --help`,
		`-v login --cookies "BDUSS=SECRET6"`,
		`--help login --cookies "BDUSS=SECRET8"`,
		`login --cookies "BDUSS=SECRET9" --version`,
		`help login --cookies "BDUSS=SECRET7"`,
		"ls /", "exit",
	}
	os.WriteFile(in, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	var err error
	if ta.stdin, err = os.Open(in); err != nil {
		t.Fatal(err)
	}
	defer ta.stdin.Close()
	if code := ta.shell(); code != 0 {
		t.Fatalf("shell: %d %s", code, ta.stderr.String())
	}
	dir, _ := config.Dir()
	h, _ := os.ReadFile(filepath.Join(dir, "history"))
	if strings.Contains(string(h), "SECRET") || strings.TrimSpace(string(h)) != "ls /" {
		t.Errorf("history: %q", h)
	}
}

// A failed delete task names the failed items; the others were removed.
func TestRemoveReportsPartialTask(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/d/a", nil, 1)
	ta.fake.Put("/d/b", nil, 1)
	ta.fake.Fail = func(endpoint string, n int) any {
		if endpoint == "pan.baidu.com/share/taskquery" {
			return map[string]any{"errno": 0, "status": "failed", "task_errno": 132, "list": []any{map[string]any{"from": "/d/b", "error_code": 132}}}
		}
		return nil
	}
	code, doc := ta.json(t, "rm", "/d/a", "/d/b")
	if removed, _ := doc["removed"].([]any); code != 4 || len(removed) != 1 || removed[0] != "/d/a" {
		t.Errorf("%d %v", code, doc)
	}
}

func TestRestoreWaitsForTask(t *testing.T) {
	ta := newTestApp(t)
	ta.fake.Put("/d/f", nil, 1)
	ta.run("rm", "/d/f")
	_, doc := ta.json(t, "recycle", "list")
	id := fmt.Sprint(int64(doc["files"].([]any)[0].(map[string]any)["fsId"].(float64)))
	ta.fake.Fail = func(endpoint string, n int) any {
		if endpoint == "pan.baidu.com/share/taskquery" {
			return map[string]any{"errno": 0, "status": "failed", "task_errno": -9, "list": []any{}}
		}
		return nil
	}
	if code := ta.run("recycle", "restore", id); code != 2 {
		t.Errorf("restore of a failed task: %d %s", code, ta.stderr.String())
	}
}

// guardedApp is a test app whose fake demands a security check, answered
// at a "terminal" with the given lines.
func guardedApp(t *testing.T, answers string) *testApp {
	ta := newTestApp(t)
	ta.fake.Guarded = true
	in := filepath.Join(t.TempDir(), "in")
	os.WriteFile(in, []byte(answers), 0o600)
	var err error
	if ta.stdin, err = os.Open(in); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ta.stdin.Close() })
	client, _ := ta.baidu()
	client.SetVerifier(ta.verify)
	return ta
}

// Giving up ends the command: its other requests do not ask again.
func TestSecurityCheckGiveUp(t *testing.T) {
	ta := guardedApp(t, "q\n")
	src := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d"} {
		os.WriteFile(filepath.Join(src, n), []byte(n), 0o600)
	}
	if code := ta.run("upload", src, "/up"); code != 130 {
		t.Errorf("exit %d: %s", code, ta.stderr.String())
	}
	if n := ta.fake.Requests["pan.baidu.com/api/authwidget?get"]; n != 1 {
		t.Errorf("asked %d times", n)
	}
}

// A lost login while checking ends the check with it, not with "cancelled".
func TestSecurityCheckLoginLost(t *testing.T) {
	ta := guardedApp(t, "\n111111\n222222\nq\n")
	ta.fake.Put("/d/a", nil, 1)
	ta.fake.Fail = func(endpoint string, n int) any {
		if endpoint == "pan.baidu.com/api/authwidget?check" {
			return map[string]any{"errno": -6}
		}
		return nil
	}
	if code := ta.run("rm", "/d/a"); code != 4 {
		t.Errorf("exit %d: %s", code, ta.stderr.String())
	}
	if n := ta.fake.Requests["pan.baidu.com/api/authwidget?check"]; n != 1 {
		t.Errorf("checked %d times", n)
	}
}

// A question is dropped when the command is cancelled (Ctrl-C, SIGTERM),
// with what was typed so far: the next read starts afresh.
func TestAskCancelled(t *testing.T) {
	ta := newTestApp(t)
	r, w, _ := os.Pipe()
	defer w.Close()
	ta.stdin = r
	w.WriteString("123") // half an answer
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	done := make(chan error)
	go func() { _, err := ta.ask(ctx, "? "); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting for an answer")
	}
	w.WriteString("pwd\n")
	if line, err := ta.ask(context.Background(), "> "); line != "pwd" || err != nil {
		t.Errorf("next line %q, %v", line, err)
	}
}

// A line that ends just as the command is cancelled leaves no Ctrl-C behind
// to interrupt the next question.
func TestAskCancelledAfterLine(t *testing.T) {
	for range 50 {
		ta := newTestApp(t)
		r, w, _ := os.Pipe()
		ta.stdin = r
		w.WriteString("abc\n")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ta.ask(ctx, "? ")
		w.WriteString("next\n")
		line, err := ta.ask(context.Background(), "> ")
		w.Close()
		if err == nil && line == "abc" { // the first question returned before reading its line
			line, err = ta.ask(context.Background(), "> ")
		}
		if line != "next" || err != nil {
			t.Fatalf("next line %q, %v", line, err)
		}
	}
}

// While a dialog holds the meter, transfers go on; their lines wait.
func TestMeterHold(t *testing.T) {
	ta := newTestApp(t)
	m := ta.newMeter(10, "上传")
	release := m.hold()
	done := make(chan struct{})
	go func() { m.add(5); m.logf("已上传 %s", "a"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a transfer waits for the dialog")
	}
	if strings.Contains(ta.stderr.String(), "已上传") {
		t.Error("printed during the dialog")
	}
	release()
	m.done()
	if !strings.Contains(ta.stderr.String(), "已上传 a") {
		t.Errorf("line lost: %q", ta.stderr.String())
	}
}

// Input typed while a command runs does not block the questions it asks.
func TestKeyboardTypeAhead(t *testing.T) {
	r, w, _ := os.Pipe()
	defer w.Close()
	ta := newTestApp(t)
	ta.stdin = r
	ta.keyboard().editorInput() // a shell's editor, not reading: a command runs
	w.WriteString("\n")         // a stray Enter
	time.AfterFunc(100*time.Millisecond, func() { w.WriteString("y\n") })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, want := range []string{"", "y"} { // not a terminal: what was typed ahead counts
		if got, err := ta.ask(ctx, "? "); got != want || err != nil {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
}

// The line editor takes a line at a time: a pasted command and the answer
// to its question are not both swallowed by the editor.
func TestKeyboardEditorTakesALine(t *testing.T) {
	r, w, _ := os.Pipe()
	defer w.Close()
	ta := newTestApp(t)
	ta.stdin = r
	k := ta.keyboard()
	in := k.editorInput()
	w.WriteString("login\ninvalid\n")
	buf := make([]byte, 64)
	if n, _ := in.Read(buf); string(buf[:n]) != "login\n" {
		t.Errorf("editor took %q", buf[:n])
	}
	if got, _ := ta.ask(context.Background(), "? "); got != "invalid" {
		t.Errorf("answer %q", got)
	}
}

// A line longer than the editor's reads reaches it whole.
func TestKeyboardShortReads(t *testing.T) {
	r, w, _ := os.Pipe()
	defer w.Close()
	ta := newTestApp(t)
	ta.stdin = r
	in := ta.keyboard().editorInput()
	w.WriteString("pwd\nls\n")
	var got []byte
	buf := make([]byte, 1)
	for !bytes.HasSuffix(got, []byte("\n")) {
		n, err := in.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, buf[:n]...)
	}
	if string(got) != "pwd\n" {
		t.Errorf("got %q", got)
	}
}

// The shell's editor is one for the whole session: reading many lines leaves
// no goroutines behind.
func TestShellEditorLifetime(t *testing.T) {
	ta := newTestApp(t)
	in := filepath.Join(t.TempDir(), "in")
	os.WriteFile(in, []byte(strings.Repeat("ls /\n", 100)+"exit\n"), 0o600)
	ta.stdin, _ = os.Open(in)
	defer ta.stdin.Close()
	before := runtime.NumGoroutine()
	if code := ta.shell(); code != 0 {
		t.Fatalf("shell: %d", code)
	}
	time.Sleep(100 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+5 {
		t.Errorf("goroutines %d → %d", before, after)
	}
}

// A terminal that does not answer the editor's cursor query gets an answer
// made up; one that answers does not.
func TestCursorQuery(t *testing.T) {
	for _, answers := range []bool{false, true} {
		r, w, _ := os.Pipe()
		ta := newTestApp(t)
		ta.stdin = r
		k := ta.keyboard()
		k.editorOutput(io.Discard).Write([]byte(" \b\x1b[6n"))
		if answers {
			w.WriteString("\x1b[12;3R")
		}
		time.Sleep(2 * cursorWait)
		buf := make([]byte, 64)
		n, _ := k.editorInput().Read(buf)
		if want := map[bool]string{false: "\x1b[1;1R", true: "\x1b[12;3R"}[answers]; string(buf[:n]) != want {
			t.Errorf("answers %v: editor read %q", answers, buf[:n])
		}
		w.Close()
	}
}

// While the terminal is asked where the cursor is, the editor gets only the
// answer, not the lines pasted meanwhile (it would keep them).
func TestCursorQueryWithPaste(t *testing.T) {
	r, w, _ := os.Pipe()
	defer w.Close()
	ta := newTestApp(t)
	ta.stdin = r
	k := ta.keyboard()
	k.editorOutput(io.Discard).Write([]byte("\x1b[6n"))
	w.WriteString("login\ninvalid\n\x1b[5;1R")
	buf := make([]byte, 4096)
	if n, _ := k.editorInput().Read(buf); string(buf[:n]) != "\x1b[5;1R" {
		t.Fatalf("editor read %q", buf[:n])
	}
	if n, _ := k.editorInput().Read(buf); string(buf[:n]) != "login\n" {
		t.Errorf("then %q", buf[:n])
	}
}
