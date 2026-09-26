package cli

import (
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
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

// stopCassette saves the recording; Main calls it before exiting.
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
		panic(err) // test setup error
	}
	stopCassette = func() { rec.Stop() }
	return rec
}

// volatile query and form parameters change on every run.
var volatile = []string{"time", "rand", "sign", "timestamp", "t", "logid", "devuid", "cuid", "bdstoken", "uploadid", "_", "dp-logid"}

// matchRequest compares method, host, path and stable parameters.
func matchRequest(r *http.Request, i cassette.Request) bool {
	u, err := url.Parse(i.URL)
	if err != nil || r.Method != i.Method || r.URL.Path != u.Path || hostClass(r.URL.Host) != hostClass(u.Host) {
		return false
	}
	return stable(r.URL.Query()) == stable(u.Query()) && r.Header.Get("Range") == first(i.Headers["Range"])
}

// hostClass ignores which of Baidu's many data servers was picked.
func hostClass(h string) string {
	switch {
	case strings.HasSuffix(h, ".baidupcs.com"):
		return "baidupcs.com"
	case strings.HasSuffix(h, ".pcs.baidu.com") && h != "pcs.baidu.com":
		return "upload.pcs.baidu.com"
	}
	return h
}

func stable(q url.Values) string {
	for _, k := range volatile {
		q.Del(k)
	}
	return q.Encode()
}

var secretParam = regexp.MustCompile(`(BDUSS|STOKEN|bdstoken|BDCLND|bdusstoken)=[^&;"\s]+`)

// redact removes credentials before the recording is written.
func redact(i *cassette.Interaction) error {
	for _, h := range []http.Header{i.Request.Headers, i.Response.Headers} {
		for k := range h {
			if slices.Contains([]string{"Cookie", "Set-Cookie"}, k) {
				h[k] = []string{"REDACTED"}
			}
		}
	}
	i.Request.URL = secretParam.ReplaceAllString(i.Request.URL, "$1=REDACTED")
	i.Request.Body = secretParam.ReplaceAllString(i.Request.Body, "$1=REDACTED")
	i.Request.Form = nil
	i.Response.Body = secretParam.ReplaceAllString(i.Response.Body, "$1=REDACTED")
	if len(i.Request.Body) > 64<<10 {
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
