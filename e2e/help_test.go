package e2e

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// commands lists every command whose help is locked in golden/help.txt.
var commands = []string{
	"", "login", "logout", "who", "users", "su", "quota",
	"ls", "tree", "meta", "search", "cd", "pwd",
	"mkdir", "rm", "cp", "mv", "recycle list", "recycle restore", "recycle delete",
	"download", "upload", "offline add", "offline list", "offline cancel", "offline delete",
	"share create", "share list", "share cancel", "share save",
	"config show", "config set", "config reset", "update",
}

// TestHelp locks the help of every command: the command line is the interface.
func TestHelp(t *testing.T) {
	var b strings.Builder
	for _, c := range commands {
		args := append(strings.Fields(c), "--help")
		out, err := exec.Command(binary, args...).Output()
		if err != nil {
			t.Fatalf("bdc %s: %v", strings.Join(args, " "), err)
		}
		b.WriteString("=== bdc " + strings.Join(args, " ") + "\n" + string(out) + "\n")
	}
	got := b.String()
	if *update {
		os.WriteFile("golden/help.txt", []byte(got), 0o644)
		return
	}
	want, _ := os.ReadFile("golden/help.txt")
	if got != string(want) {
		g, w := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := range min(len(g), len(w)) {
			if g[i] != w[i] {
				t.Fatalf("help differs from golden/help.txt at line %d:\n got: %q\nwant: %q\nrun go test ./e2e -update -run TestHelp to accept", i+1, g[i], w[i])
			}
		}
		t.Fatalf("help differs from golden/help.txt in length (%d lines, want %d); run go test ./e2e -update -run TestHelp to accept", len(g), len(w))
	}
}
