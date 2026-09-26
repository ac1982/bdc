package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// size formats bytes the way people read them: 0B, 16B, 3.00MB.
func size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	v, i := float64(n)/unit, 0
	for v >= unit && i < 4 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.2f%cB", v, "KMGTP"[i])
}

// byteSize is a flag value such as 512K, 2MB or 1.5G (powers of 1024).
type byteSize int64

func (b *byteSize) UnmarshalText(text []byte) error {
	s := strings.ToUpper(strings.TrimSpace(string(text)))
	s = strings.TrimSuffix(strings.TrimSuffix(s, "B"), "I")
	mult := int64(1)
	if s != "" {
		if i := strings.IndexRune("KMGT", rune(s[len(s)-1])); i >= 0 {
			mult = 1 << (10 * (i + 1))
			s = s[:len(s)-1]
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return fmt.Errorf("无效的大小 %q, 例如 512K, 2MB, 1G", text)
	}
	*b = byteSize(v * float64(mult))
	return nil
}

func clock(t time.Time) string { return t.Format("2006-01-02 15:04:05") }

// table writes rows as left-aligned columns, measuring display width so
// Chinese names line up.
func table(w io.Writer, rows [][]string) {
	var widths []int
	for _, r := range rows {
		for i, c := range r {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], runewidth.StringWidth(c))
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			b.WriteString(c)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-runewidth.StringWidth(c)+2))
			}
		}
		fmt.Fprintln(w, b.String())
	}
}
