package cli

import (
	"net/http"
	"net/url"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
	"github.com/ac1982/baidunetdisk-cli/internal/config"
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
		u, err := url.Parse(s.Proxy)
		if err != nil {
			return nil, inputf("代理地址无效: %s", s.Proxy)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return baidu.New(&http.Client{Transport: cassetteTransport(tr)}, cookies)
}
