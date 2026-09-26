// Package e2e runs the bnd binary through scenarios and compares its stdout,
// stderr and exit code with a recorded run.
//
//	go test ./e2e                  replay offline (the default)
//	go test ./e2e -record          record with a real account, only under /bnd-test
//	go test ./e2e -update          replay, and accept the new output as correct
//
// Recording reads the login from BND_TEST_COOKIES, or else from the current
// account of your own bnd config. Recordings contain account details, so
// e2e/testdata is not committed; without it the scenarios are skipped.
package e2e

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ac1982/baidunetdisk-cli/internal/config"
)

var (
	record = flag.Bool("record", false, "record the scenarios with a real account")
	update = flag.Bool("update", false, "replay and overwrite the recorded output")
)

var binary string

func TestMain(m *testing.M) {
	flag.Parse()
	dir, err := os.MkdirTemp("", "bnd-e2e-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "bnd")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "..")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// step is one command of a scenario. In args, $WORK is the scenario's local
// working directory, $COOKIES the login, and $NAME a value captured earlier.
type step struct {
	name    string
	args    []string
	capture map[string]string // variable → regexp with one group, matched on stdout
	sorted  bool              // lines come out in parallel order: compare sorted
	check   func(work string) error
}

type scenario struct {
	name  string
	files map[string][]byte // created under $WORK before the first step
	steps []step
}

func TestScenarios(t *testing.T) {
	cookies := "BDUSS=REDACTED; STOKEN=REDACTED"
	if *record {
		cookies = recordCookies(t)
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			dir := filepath.Join("testdata", sc.name)
			if !*record {
				if _, err := os.Stat(dir); err != nil {
					t.Skip("no recording; run go test ./e2e -record")
				}
			} else {
				os.RemoveAll(dir)
				os.MkdirAll(dir, 0o755)
			}
			run(t, sc, dir, cookies)
			if *record {
				checkNoSecrets(t, dir, cookies)
			}
		})
	}
}

func run(t *testing.T, sc scenario, dir, cookies string) {
	tmp := t.TempDir()
	work, cfg := filepath.Join(tmp, "work"), filepath.Join(tmp, "config")
	for name, content := range sc.files {
		p := filepath.Join(work, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(work, 0o755)
	vars := map[string]string{"WORK": work, "COOKIES": cookies}
	for i, st := range sc.steps {
		base := filepath.Join(dir, fmt.Sprintf("%02d-%s", i+1, st.name))
		args := make([]string, len(st.args))
		for j, a := range st.args {
			args[j] = os.Expand(a, func(k string) string { return vars[k] })
		}
		cassette, _ := filepath.Abs(base + ".yaml")
		if !*record {
			// replay consumes a copy so the recording stays intact
			data, _ := os.ReadFile(cassette)
			cassette = filepath.Join(tmp, filepath.Base(cassette))
			os.WriteFile(cassette, data, 0o644)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), config.EnvDir+"="+cfg, "BND_CASSETTE="+cassette)
		if *record {
			cmd.Env = append(cmd.Env, "BND_RECORD=1")
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		cancel()
		code := 0
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		for k, re := range st.capture {
			m := regexp.MustCompile(re).FindStringSubmatch(stdout.String())
			if m == nil {
				t.Fatalf("%s: %s not found in output:\n%s", base, re, stdout.String())
			}
			vars[k] = m[1]
		}
		if st.check != nil {
			if err := st.check(work); err != nil {
				t.Errorf("%s: %v", base, err)
			}
		}
		got := fmt.Sprintf("$ bnd %s\n[exit %d]\n--- stdout\n%s--- stderr\n%s",
			strings.Join(st.args, " "), code, normalize(stdout.String(), tmp, st.sorted), normalize(stderr.String(), tmp, st.sorted))
		golden := base + ".golden"
		if *record || *update {
			os.WriteFile(golden, []byte(got), 0o644)
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if got != string(want) {
			t.Errorf("%s: output differs from the recording\n--- recorded\n%s\n--- now\n%s", base, want, got)
		}
	}
}

var (
	rfc3339  = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d[+-]\d\d:\d\d`)
	padding  = regexp.MustCompile(`(\S)  +`) // table columns widen with the temporary path
	datetime = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d:\d\d`)
)

// normalize removes what changes from run to run: temporary paths, times
// and the column widths that depend on them.
func normalize(s, tmp string, sorted bool) string {
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		s = strings.ReplaceAll(s, real, "$TMP")
	}
	s = strings.ReplaceAll(s, tmp, "$TMP")
	s = rfc3339.ReplaceAllString(s, "<time>")
	s = datetime.ReplaceAllString(s, "<time>")
	s = padding.ReplaceAllString(s, "$1  ")
	if sorted {
		lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		slices.Sort(lines)
		s = strings.Join(lines, "\n") + "\n"
	}
	if s == "\n" {
		return ""
	}
	return s
}

func recordCookies(t *testing.T) string {
	if c := os.Getenv("BND_TEST_COOKIES"); c != "" {
		return c
	}
	cfg, err := config.Load()
	if err != nil || cfg.Current() == nil {
		t.Fatal("recording needs a login: set BND_TEST_COOKIES or log in with bnd login")
	}
	return cfg.Current().Cookies
}

// checkNoSecrets fails, and deletes the recording, if a cookie value leaked into it.
func checkNoSecrets(t *testing.T, dir, cookies string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		for _, part := range strings.Split(cookies, ";") {
			if _, v, _ := strings.Cut(strings.TrimSpace(part), "="); len(v) >= 16 && bytes.Contains(data, []byte(v)) {
				os.RemoveAll(dir)
				t.Fatalf("%s contains a cookie value (%.4s…); recording deleted", e.Name(), v)
			}
		}
	}
}

// random returns reproducible bytes: the same content on every recording.
func random(n int, seed uint64) []byte {
	b := make([]byte, n)
	r := rand.NewChaCha8([32]byte{byte(seed), byte(seed >> 8)})
	r.Read(b)
	return b
}

// same checks that each pair of files under work has equal content.
func same(pairs ...string) func(string) error {
	return func(work string) error {
		for i := 0; i < len(pairs); i += 2 {
			a, err := os.ReadFile(filepath.Join(work, pairs[i]))
			if err != nil {
				return err
			}
			b, err := os.ReadFile(filepath.Join(work, pairs[i+1]))
			if err != nil {
				return err
			}
			if !bytes.Equal(a, b) {
				return fmt.Errorf("%s and %s differ", pairs[i], pairs[i+1])
			}
		}
		return nil
	}
}
