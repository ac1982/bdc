package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAccounts(t *testing.T) {
	t.Setenv(EnvDir, t.TempDir())
	c, err := Load()
	if err != nil || c.Current() != nil {
		t.Fatal(c, err)
	}
	c.Put(Account{UID: 1, Name: "a", Cookies: "BDUSS=1"})
	c.Current().Workdir = "/x"
	c.Put(Account{UID: 2, Name: "b"})
	c.Put(Account{UID: 1, Name: "a2", Cookies: "BDUSS=new"}) // re-login keeps the workdir
	if cur := c.Current(); cur.Name != "a2" || cur.Workdir != "/x" || len(c.Accounts) != 2 {
		t.Fatalf("%+v", c.Accounts)
	}
	c.Remove(1)
	if cur := c.Current(); cur == nil || cur.UID != 2 {
		t.Fatalf("after remove: %+v", cur)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := Load()
	if err != nil || again.Current().UID != 2 {
		t.Fatal(again, err)
	}
	if fi, _ := os.Stat(c.Path()); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v: it holds login cookies", fi.Mode().Perm())
	}
}

func TestCorruptConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDir, dir)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"), 0o600)
	if _, err := Load(); err == nil {
		t.Fatal("loaded a corrupt config")
	}
}
