package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/schollz/progressbar/v3"
)

// meter shows the combined progress of a transfer on stderr: a bar when
// stderr is a terminal, and a line for each finished file either way.
type meter struct {
	mu  sync.Mutex
	w   io.Writer
	bar *progressbar.ProgressBar // nil without a terminal
}

func (a *App) newMeter(total int64, verb string) *meter {
	m := &meter{w: a.stderr}
	if f, ok := a.stderr.(*os.File); ok && isatty.IsTerminal(f.Fd()) {
		m.bar = progressbar.NewOptions64(total,
			progressbar.OptionSetWriter(f),
			progressbar.OptionSetDescription(verb),
			progressbar.OptionShowBytes(true),
			progressbar.OptionShowTotalBytes(true),
			progressbar.OptionSetWidth(24),
			progressbar.OptionThrottle(200*time.Millisecond),
			progressbar.OptionClearOnFinish(),
			progressbar.OptionUseIECUnits(true),
		)
	}
	return m
}

// add counts n more bytes transferred.
func (m *meter) add(n int64) {
	if m.bar != nil {
		m.bar.Add64(n)
	}
}

// logf prints a line above the bar.
func (m *meter) logf(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bar != nil {
		m.bar.Clear()
	}
	fmt.Fprintf(m.w, format+"\n", args...)
}

func (m *meter) done() {
	if m.bar != nil {
		m.bar.Finish()
	}
}
