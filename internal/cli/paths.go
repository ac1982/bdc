package cli

import (
	"errors"
	"path"
	"runtime"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
)

func hasGlob(p string) bool { return strings.ContainsAny(p, `*?[\`) }

// remoteKey and localKey fold paths the way their store compares them:
// Baidu ignores case; the usual file systems of macOS ignore case and Unicode
// normalization (é composed or not), those of Windows ignore case. Two paths
// with the same key are the same file.
func remoteKey(p string) string { return strings.ToLower(p) }

func localKey(p string) string {
	switch runtime.GOOS {
	case "darwin":
		return strings.ToLower(norm.NFC.String(p))
	case "windows":
		return strings.ToLower(p)
	}
	return p
}

// expand resolves each argument against the working directory and expands
// wildcards against the netdisk. A path without wildcards is passed through
// for the command to check. Otherwise every component from the first
// wildcard on is matched against real directory entries, so only existing
// paths come out; an entry whose name equals the component exactly wins over
// the pattern, so names like "[1].txt" mean themselves (\ escapes, too). A
// pattern that matches nothing is an input error.
func (a *App) expand(c *baidu.Client, args ...string) ([]string, error) {
	var out []string
	for _, arg := range args {
		p := a.abs(arg)
		if !hasGlob(p) {
			out = append(out, p)
			continue
		}
		matches := []string{"/"}
		listing := false // from the first wildcard on, components are matched against listings
		for _, part := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
			listing = listing || hasGlob(part)
			var next []string
			for _, dir := range matches {
				if !listing {
					next = append(next, path.Join(dir, part))
					continue
				}
				found, err := a.match(c, dir, part)
				if err != nil {
					return nil, err
				}
				next = append(next, found...)
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

// match returns the entries of dir named part: the entry with exactly that
// name if there is one, else those matching part as a pattern.
func (a *App) match(c *baidu.Client, dir, part string) ([]string, error) {
	entries, err := c.List(a.ctx, dir)
	if errors.Is(err, baidu.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		if e.Name == part {
			return []string{e.Path}, nil
		}
		if ok, _ := path.Match(part, e.Name); ok {
			found = append(found, e.Path)
		}
	}
	return found, nil
}
