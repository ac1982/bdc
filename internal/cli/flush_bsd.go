//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// flushInput drops the terminal's input not read yet.
func flushInput(tty *os.File) {
	const fread = 1 // FREAD of <sys/fcntl.h>: the input queue
	unix.IoctlSetPointerInt(int(tty.Fd()), unix.TIOCFLUSH, fread)
}
