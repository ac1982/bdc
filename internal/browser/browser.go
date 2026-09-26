// Package browser reads the Baidu login cookies of a local Chrome or Edge.
package browser

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/browserutils/kooky"
	_ "github.com/browserutils/kooky/browser/chrome"
	_ "github.com/browserutils/kooky/browser/edge"
)

// Browser names as kooky reports them.
const (
	Chrome = "chrome"
	Edge   = "edge"
)

// Cookies returns "name=value; …" for the unexpired Baidu cookies of the
// browser profile ("Default", "Profile 1", …).
func Cookies(browser, profile string) (string, error) {
	ctx := context.Background()
	var all kooky.Cookies
	found := false
	for store := range kooky.TraverseCookieStores(ctx).OnlyCookieStores() {
		if store.Browser() != browser || !matchProfile(store, profile) {
			continue
		}
		found = true
		cookies, err := store.TraverseCookies(kooky.Valid, kooky.DomainHasSuffix("baidu.com")).ReadAllCookies(ctx)
		store.Close()
		if err != nil {
			return "", fmt.Errorf("无法读取 %s 的 Cookie (%w). macOS 上请在 系统设置 → 隐私与安全性 → 完全磁盘访问权限 中允许终端程序", browser, err)
		}
		all = append(all, cookies...)
	}
	if !found {
		return "", fmt.Errorf("找不到 %s 的用户配置 %q", browser, profile)
	}
	// pan.baidu.com's STOKEN differs from the passport one on .baidu.com; the
	// most specific domain wins.
	slices.SortStableFunc(all, func(a, b *kooky.Cookie) int {
		return domainRank(a.Domain) - domainRank(b.Domain)
	})
	var parts []string
	seen := map[string]bool{}
	for _, c := range all {
		if domainRank(c.Domain) < len(domains) && !seen[c.Name] {
			seen[c.Name] = true
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	if !seen["BDUSS"] {
		return "", ErrNoLogin
	}
	return strings.Join(parts, "; "), nil
}

// ErrNoLogin means the browser is not logged in to Baidu.
var ErrNoLogin = errors.New("浏览器中没有百度的登录, 请先在浏览器中登录 pan.baidu.com")

// domains whose cookies apply to the netdisk, most specific first.
var domains = []string{"pan.baidu.com", ".pan.baidu.com", ".baidu.com", "baidu.com"}

func domainRank(d string) int {
	if i := slices.Index(domains, d); i >= 0 {
		return i
	}
	return len(domains)
}

func matchProfile(s kooky.CookieStore, profile string) bool {
	return s.Profile() == profile || (profile == "Default" && s.IsDefaultProfile())
}
