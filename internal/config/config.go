// Package config reads and writes bnd's settings and accounts.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// EnvDir overrides the directory holding config.json.
const EnvDir = "BND_CONFIG_DIR"

// Config is the whole file. Accounts hold login cookies; Settings never do,
// so Settings alone is safe to print.
type Config struct {
	Active   uint64    `json:"active,omitempty"`
	Accounts []Account `json:"accounts"`
	Settings Settings  `json:"settings"`

	path string
}

// Account is one logged-in Baidu user.
type Account struct {
	UID     uint64 `json:"uid"`
	Name    string `json:"name"`
	Cookies string `json:"cookies"`
	Workdir string `json:"workdir"`
}

// Settings are the user-tunable options.
type Settings struct {
	SaveDir       string `json:"saveDir"`
	Connections   int    `json:"connections"`   // connections per download
	Parallel      int    `json:"parallel"`      // files transferred at once
	DownloadLimit int64  `json:"downloadLimit"` // bytes per second, 0 = unlimited
	UploadLimit   int64  `json:"uploadLimit"`   // bytes per second, 0 = unlimited
	Proxy         string `json:"proxy"`
}

// Defaults are the settings of a new config.
func Defaults() Settings {
	home, _ := os.UserHomeDir()
	return Settings{
		SaveDir:     filepath.Join(home, "Downloads"),
		Connections: 8,
		Parallel:    2,
	}
}

// Dir is where config.json and resume records live.
func Dir() (string, error) {
	if d := os.Getenv(EnvDir); d != "" {
		return d, nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "bnd"), nil
}

// Load reads the config, or returns defaults if there is none yet.
func Load() (*Config, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	c := &Config{Settings: Defaults(), path: filepath.Join(dir, "config.json")}
	data, err := os.ReadFile(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("配置文件 %s 已损坏: %w", c.path, err)
	}
	return c, nil
}

// Save writes the config atomically, readable only by the user.
func (c *Config) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Path is the file the config is saved to.
func (c *Config) Path() string { return c.path }

// Current returns the active account, or nil when nobody is logged in.
func (c *Config) Current() *Account {
	for i := range c.Accounts {
		if c.Accounts[i].UID == c.Active {
			return &c.Accounts[i]
		}
	}
	return nil
}

// Put adds or replaces an account and makes it active.
func (c *Config) Put(a Account) {
	c.Active = a.UID
	for i := range c.Accounts {
		if c.Accounts[i].UID == a.UID {
			if a.Workdir == "" {
				a.Workdir = c.Accounts[i].Workdir
			}
			c.Accounts[i] = a
			return
		}
	}
	if a.Workdir == "" {
		a.Workdir = "/"
	}
	c.Accounts = append(c.Accounts, a)
}

// Remove deletes the account; if it was active, another one (if any) takes over.
func (c *Config) Remove(uid uint64) {
	for i := range c.Accounts {
		if c.Accounts[i].UID == uid {
			c.Accounts = append(c.Accounts[:i], c.Accounts[i+1:]...)
			break
		}
	}
	if c.Active == uid {
		c.Active = 0
		if len(c.Accounts) > 0 {
			c.Active = c.Accounts[0].UID
		}
	}
}
