package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/mattn/go-isatty"
)

// The terminal's input is read into one buffer, and taken from it by one
// reader at a time: by a question (ask), or by the shell's line editor while
// it reads a command line. Input typed while a command
// runs stays in the buffer for whoever reads next. Answers are plain lines,
// edited by the terminal itself; Ctrl-C there is a signal, which cancels the
// command.

// keyboard is the buffered terminal input.
type keyboard struct {
	tty *os.File // the terminal, or nil when the input is not one

	mu      sync.Mutex
	changed chan struct{} // closed and replaced on every change below
	buf     []byte        // read, not yet taken
	ended   bool          // the input ended
}

func newKeyboard(in *os.File) *keyboard {
	k := &keyboard{changed: make(chan struct{})}
	if isatty.IsTerminal(in.Fd()) || isatty.IsCygwinTerminal(in.Fd()) {
		k.tty = in
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			k.mu.Lock()
			k.buf = append(k.buf, buf[:n]...)
			k.ended = err != nil
			k.notify()
			k.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return k
}

// notify wakes those waiting for a change; mu is held.
func (k *keyboard) notify() {
	close(k.changed)
	k.changed = make(chan struct{})
}

// wait waits, with mu held, until ready holds or ctx ends.
func (k *keyboard) wait(ctx context.Context, ready func() bool) error {
	for !ready() {
		changed := k.changed
		k.mu.Unlock()
		select {
		case <-changed:
			k.mu.Lock()
		case <-ctx.Done():
			k.mu.Lock()
			return ctx.Err()
		}
	}
	return nil
}

// discard drops the input not taken yet, including what the terminal holds
// of a line not ended.
func (k *keyboard) discard() {
	if k.tty != nil {
		flushInput(k.tty)
	}
	k.buf = nil
}

// editorInput is the input of the shell's line editor, which reads only
// while it reads a command line (as it asks for keys); what is typed
// meanwhile is left for the command's questions or the next line.
func (k *keyboard) editorInput() io.Reader { return editorInput{k} }

type editorInput struct{ k *keyboard }

// Read hands the editor at most a line: the editor buffers what it reads,
// and must not hold what follows.
func (e editorInput) Read(p []byte) (int, error) {
	k := e.k
	k.mu.Lock()
	defer k.mu.Unlock()
	k.wait(context.Background(), func() bool { return len(k.buf) > 0 || k.ended })
	if len(k.buf) == 0 {
		return 0, io.EOF
	}
	n := min(len(p), len(k.buf))
	if i := bytes.IndexAny(k.buf[:n], "\r\n"); i >= 0 {
		n = i + 1
	}
	n = copy(p, k.buf[:n])
	k.buf = k.buf[n:]
	return n, nil
}

func (a *App) keyboard() *keyboard {
	if a.keys == nil {
		a.keys = newKeyboard(a.stdin)
	}
	return a.keys
}

// ask puts a question to the person at the terminal and returns the answer:
// the next line. At a terminal only what is typed after the question counts.
// If ctx ends first (Ctrl-C), what was typed is dropped and ctx's error
// returned; at the end of input, io.EOF.
func (a *App) ask(ctx context.Context, question string) (string, error) {
	k := a.keyboard()
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.tty != nil {
		k.discard()
	}
	fmt.Fprint(a.stderr, question)
	if err := k.wait(ctx, func() bool { return bytes.IndexByte(k.buf, '\n') >= 0 || k.ended }); err != nil {
		k.discard()
		fmt.Fprintln(a.stderr)
		return "", err
	}
	line := k.buf
	if i := bytes.IndexByte(k.buf, '\n'); i >= 0 {
		line, k.buf = k.buf[:i], k.buf[i+1:]
	} else if k.buf = nil; len(line) == 0 {
		return "", io.EOF
	}
	return string(bytes.TrimSpace(line)), nil
}
