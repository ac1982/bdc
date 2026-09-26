package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// flushInput drops the terminal's input not read yet.
func flushInput(tty *os.File) { unix.IoctlSetInt(int(tty.Fd()), unix.TCFLSH, unix.TCIFLUSH) }
