// Command bdc is a command-line client for Baidu Netdisk.
package main

import (
	"os"

	"github.com/ac1982/bdc/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
