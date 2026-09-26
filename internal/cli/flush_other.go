//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package cli

import "os"

// flushInput would drop the terminal's input not read yet; here the
// terminal keeps it.
func flushInput(*os.File) {}
