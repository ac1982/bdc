package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"gopkg.in/dnaeon/go-vcr.v4/pkg/cassette"
	"gopkg.in/dnaeon/go-vcr.v4/pkg/recorder"
)

// Test hooks: BND_CASSETTE=<file> replays HTTP from the file instead of the
// network; with BND_RECORD=1 it records real traffic into it. Used by e2e.
const (
	envCassette = "BND_CASSETTE"
	envRecord   = "BND_RECORD"
)

// stopCassette saves the recording; Main calls it before returning.
var stopCassette = func() {}

func cassetteTransport(real http.RoundTripper) http.RoundTripper {
	name := os.Getenv(envCassette)
	if name == "" {
		return real
	}
	mode := recorder.ModeReplayOnly
	if os.Getenv(envRecord) == "1" {
		mode = recorder.ModeRecordOnly
	}
	rec, err := recorder.New(strings.TrimSuffix(name, ".yaml"),
		recorder.WithMode(mode),
		recorder.WithRealTransport(real),
		recorder.WithSkipRequestLatency(true),
		recorder.WithMatcher(matchRequest),
		recorder.WithHook(redact, recorder.BeforeSaveHook),
	)
	if err != nil {
		panic(err) // a broken test setup
	}
	stopCassette = func() { rec.Stop() }
	return rec
}

// volatile parameters differ on every run: times, signatures, session ids
// and the samples precreate derives from the time.
var volatile = map[string]bool{
	"time": true, "rand": true, "sign": true, "timestamp": true, "t": true, "devuid": true, "cuid": true,
	"bdstoken": true, "bdusstoken": true, "uploadid": true, "local_mtime": true, "local_ctime": true, "data_time": true,
	"data_offset": true, "data_content": true, "data_length": true, "Client_logid": true,
}

// matchRequest compares method, host, path, stable parameters and, for small
// bodies, their stable content. File servers are matched by path and range
// only, since Baidu hands out a different server and signature every time.
func matchRequest(r *http.Request, i cassette.Request) bool {
	u, err := url.Parse(i.URL)
	if err != nil || r.Method != i.Method || r.URL.Path != u.Path {
		return false
	}
	if server(r.URL.Host) != server(u.Host) || r.Header.Get("Range") != first(i.Headers["Range"]) {
		return false
	}
	if server(u.Host) == "file server" {
		return true
	}
	return stable(r.URL.Query()) == stable(u.Query()) && bodyKey(r) == bodyKey(recorded(i))
}

// server names the host, grouping Baidu's interchangeable servers.
func server(host string) string {
	switch {
	case strings.HasSuffix(host, ".baidupcs.com"):
		return "file server"
	case strings.HasSuffix(host, ".pcs.baidu.com"):
		return "upload server"
	}
	return host
}

func stable(q url.Values) string {
	for k := range q {
		if volatile[k] {
			q.Del(k)
		}
	}
	return q.Encode()
}

// bodyKey is the stable content of a form or PCS "param" body; other bodies
// (uploaded data) are not compared.
func bodyKey(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	data, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(data))
	ctype, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch ctype {
	case "application/x-www-form-urlencoded":
		q, _ := url.ParseQuery(string(data))
		return stable(q)
	case "multipart/form-data":
		form, err := multipart.NewReader(bytes.NewReader(data), params["boundary"]).ReadForm(1 << 20)
		if err == nil && len(form.Value["param"]) == 1 {
			var v any
			json.Unmarshal([]byte(form.Value["param"][0]), &v)
			key, _ := json.Marshal(v)
			return string(key)
		}
	}
	return ""
}

// recorded rebuilds enough of a recorded request to compute its bodyKey.
func recorded(i cassette.Request) *http.Request {
	r, _ := http.NewRequest(i.Method, i.URL, strings.NewReader(i.Body))
	r.Header = i.Headers.Clone()
	return r
}

var secret = regexp.MustCompile(`(BDUSS|STOKEN|bdstoken|BDCLND|bdusstoken|randsk)("?\s*[=:]\s*"?)[^&;",\s]+`)

// redact removes credentials before the recording is written.
func redact(i *cassette.Interaction) error {
	for _, h := range []http.Header{i.Request.Headers, i.Response.Headers} {
		for _, k := range []string{"Cookie", "Set-Cookie"} {
			if h.Get(k) != "" {
				h[k] = []string{"REDACTED"}
			}
		}
	}
	scrub := func(s string) string { return secret.ReplaceAllString(s, "${1}${2}REDACTED") }
	i.Request.URL = scrub(i.Request.URL)
	i.Request.Body = scrub(i.Request.Body)
	i.Request.Form = nil
	i.Response.Body = scrub(i.Response.Body)
	if !strings.HasPrefix(i.Request.Headers.Get("Content-Type"), "application/x-www-form-urlencoded") &&
		len(i.Request.Body) > 64<<10 {
		i.Request.Body = "" // uploaded file data; not needed to replay
	}
	return nil
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
