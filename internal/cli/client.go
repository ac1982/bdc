package cli

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/ac1982/bdc/internal/baidu"
	"github.com/ac1982/bdc/internal/config"
)

// newClient builds the Baidu client for an account, honouring the proxy
// setting and, in tests, the recording transport. A non-nil rt replaces the
// network (unit tests use a fake Baidu).
func newClient(s config.Settings, cookies string, rt http.RoundTripper) (*baidu.Client, error) {
	if rt != nil {
		return baidu.New(&http.Client{Transport: rt}, cookies)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 16
	if s.Proxy != "" {
		u, err := parseProxy(s.Proxy)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return baidu.New(&http.Client{Transport: cassetteTransport(tr)}, cookies)
}

// parseProxy checks a proxy address: http, https or socks5, with a host.
func parseProxy(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || !slices.Contains([]string{"http", "https", "socks5", "socks5h"}, u.Scheme) {
		return nil, inputf("代理地址无效: %s (应如 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080)", s)
	}
	return u, nil
}
