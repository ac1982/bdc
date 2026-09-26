package cli

import (
	"context"
	"io"
	"strings"

	"github.com/chzyer/readline"
)

// newLine returns a line editor on the app's terminal: the shell's, or the
// one questions are asked with outside it.
func (a *App) newLine(cfg readline.Config) (*readline.Instance, error) {
	a.keys = newKeyboard(a.stdin)
	cfg.Stdin, cfg.Stdout, cfg.Stderr = io.NopCloser(a.keys), a.stderr, a.stderr
	rl, err := readline.NewEx(&cfg)
	if err != nil {
		return nil, err
	}
	a.line = rl
	return rl, nil
}

// ask puts a question to the person at the terminal and returns the answer.
func (a *App) ask(ctx context.Context, question string) (string, error) {
	if a.line == nil {
		if _, err := a.newLine(readline.Config{}); err != nil {
			return "", err
		}
	}
	answer, err := a.readLine(ctx, question)
	return strings.TrimSpace(answer), err
}

// readLine reads a line from the terminal after prompt. Ctrl-C is
// readline.ErrInterrupt, end of input io.EOF. If ctx ends first, the line is
// ended as if Ctrl-C was typed, dropping what was typed so far, and ctx's
// error is returned.
func (a *App) readLine(ctx context.Context, prompt string) (string, error) {
	type lineRead struct {
		line string
		err  error
	}
	a.line.SetPrompt(prompt)
	read := make(chan lineRead, 1)
	go func() {
		line, err := a.line.Readline()
		read <- lineRead{line, err}
	}()
	select {
	case r := <-read:
		return r.line, r.err
	case <-ctx.Done():
		a.keys.ctrlC()
		<-read
		return "", ctx.Err()
	}
}

// keyboard is the terminal's input as the line editor reads it, into which
// a Ctrl-C can be typed: that is how a read is ended from outside.
type keyboard struct {
	keys  chan []byte   // read from the terminal; closed at its end
	typed chan struct{} // a Ctrl-C typed by ctrlC
	rest  []byte        // of the last keys, not yet read
}

func newKeyboard(r io.Reader) *keyboard {
	k := &keyboard{keys: make(chan []byte), typed: make(chan struct{}, 1)}
	go func() {
		defer close(k.keys)
		for {
			buf := make([]byte, 256)
			n, err := r.Read(buf)
			if n > 0 {
				k.keys <- buf[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	return k
}

// ctrlC types a Ctrl-C.
func (k *keyboard) ctrlC() {
	select {
	case k.typed <- struct{}{}:
	default: // one is waiting already
	}
}

func (k *keyboard) Read(p []byte) (int, error) {
	if len(k.rest) == 0 {
		select {
		case b, ok := <-k.keys:
			if !ok {
				return 0, io.EOF
			}
			k.rest = b
		case <-k.typed:
			return copy(p, []byte{readline.CharInterrupt}), nil
		}
	}
	n := copy(p, k.rest)
	k.rest = k.rest[n:]
	return n, nil
}
