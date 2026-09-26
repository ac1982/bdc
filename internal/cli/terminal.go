package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
)

// The terminal's input goes to one reader at a time: to a question (ask)
// while one is asked, else to the shell's line editor, if there is one.
// Answers are plain lines, edited by the terminal itself: Ctrl-C there is a
// signal, which cancels the command, and the terminal drops the line typed.

// keyboard reads the terminal and hands its input on.
type keyboard struct {
	mu      sync.Mutex
	asking  bool          // guarded by mu
	editor  chan []byte   // the shell's line editor reads here; nil outside the shell
	answers chan []byte   // questions read here
	ended   chan struct{} // closed when the input ends
	partial []byte        // of the answers: read, not yet a whole line
}

// newKeyboard reads r; withEditor, what is not an answer goes to a line
// editor (see editorInput), else all is answers.
func newKeyboard(r io.Reader, withEditor bool) *keyboard {
	k := &keyboard{answers: make(chan []byte, 64), ended: make(chan struct{})}
	if withEditor {
		k.editor = make(chan []byte)
	}
	go func() {
		defer close(k.ended)
		for {
			buf := make([]byte, 256)
			n, err := r.Read(buf)
			if n > 0 {
				k.reader() <- buf[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	return k
}

// reader is where input goes now.
func (k *keyboard) reader() chan []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.editor != nil && !k.asking {
		return k.editor
	}
	return k.answers
}

func (k *keyboard) setAsking(on bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.asking = on
}

// editorInput is the input of the line editor: what is not an answer.
func (k *keyboard) editorInput() io.ReadCloser { return &editorInput{k: k} }

type editorInput struct {
	k    *keyboard
	rest []byte
}

func (e *editorInput) Read(p []byte) (int, error) {
	if len(e.rest) == 0 {
		select {
		case e.rest = <-e.k.editor:
		case <-e.k.ended:
			return 0, io.EOF
		}
	}
	n := copy(p, e.rest)
	e.rest = e.rest[n:]
	return n, nil
}

func (e *editorInput) Close() error { return nil }

func (a *App) keyboard() *keyboard {
	if a.keys == nil {
		a.keys = newKeyboard(a.stdin, false)
	}
	return a.keys
}

// ask puts a question to the person at the terminal and returns the answer:
// the next line typed. If ctx ends first (Ctrl-C), what was typed for it is
// dropped and ctx's error returned; at the end of input, io.EOF.
func (a *App) ask(ctx context.Context, question string) (string, error) {
	k := a.keyboard()
	fmt.Fprint(a.stderr, question)
	k.setAsking(true)
	defer k.setAsking(false)
	for {
		if i := bytes.IndexByte(k.partial, '\n'); i >= 0 {
			line := string(k.partial[:i])
			k.partial = k.partial[i+1:]
			return strings.TrimSpace(line), nil
		}
		select {
		case b := <-k.answers:
			k.partial = append(k.partial, b...)
		case <-ctx.Done():
			k.partial = nil
			for len(k.answers) > 0 {
				<-k.answers
			}
			fmt.Fprintln(a.stderr)
			return "", ctx.Err()
		case <-k.ended:
			if len(k.answers) > 0 {
				continue
			}
			if len(k.partial) == 0 {
				return "", io.EOF
			}
			line := string(k.partial)
			k.partial = nil
			return strings.TrimSpace(line), nil
		}
	}
}
