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
	app *App
	w   io.Writer
	bar *progressbar.ProgressBar // nil without a terminal

	held   bool     // a dialog has the terminal (see hold): nothing is drawn
	unseen int64    // bytes counted while held
	lines  []string // lines logged while held
}

// newMeter starts the meter of the app's transfer, until done.
func (a *App) newMeter(total int64, verb string) *meter {
	m := &meter{app: a, w: a.stderr}
	a.meter = m
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
	if m.bar == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held {
		m.unseen += n
		return
	}
	m.bar.Add64(n)
}

// hold clears the bar and draws nothing until release is called, for a
// dialog on the terminal; transfers go on, and what they report is shown
// afterwards.
func (m *meter) hold() (release func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held = true
	if m.bar != nil {
		m.bar.Clear()
	}
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.held = false
		for _, l := range m.lines {
			fmt.Fprintln(m.w, l)
		}
		m.lines = nil
		if m.bar != nil {
			m.bar.Add64(m.unseen)
		}
		m.unseen = 0
	}
}

// logf prints a line above the bar.
func (m *meter) logf(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held {
		m.lines = append(m.lines, fmt.Sprintf(format, args...))
		return
	}
	if m.bar != nil {
		m.bar.Clear()
	}
	fmt.Fprintf(m.w, format+"\n", args...)
}

func (m *meter) done() {
	m.app.meter = nil
	if m.bar != nil {
		m.bar.Finish()
	}
}
