package cli

import (
	"errors"
	"path"
	"strings"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
)

func hasGlob(p string) bool { return strings.ContainsAny(p, "*?[") }

// expand resolves each argument against the working directory and expands
// wildcards (in any path component) against the netdisk. A pattern that
// matches nothing is an input error.
func (a *App) expand(c *baidu.Client, args ...string) ([]string, error) {
	var out []string
	for _, arg := range args {
		p := a.abs(arg)
		if !hasGlob(p) {
			out = append(out, p)
			continue
		}
		matches := []string{"/"}
		for _, part := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
			var next []string
			for _, dir := range matches {
				if !hasGlob(part) {
					next = append(next, path.Join(dir, part))
					continue
				}
				entries, err := c.List(a.ctx, dir)
				if errors.Is(err, baidu.ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, err
				}
				for _, e := range entries {
					if ok, _ := path.Match(part, e.Name); ok {
						next = append(next, e.Path)
					}
				}
			}
			matches = next
		}
		if len(matches) == 0 {
			return nil, inputf("没有匹配 %s 的文件", p)
		}
		out = append(out, matches...)
	}
	return out, nil
}
