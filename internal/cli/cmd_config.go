package cli

import (
	"fmt"
	"io"

	"github.com/ac1982/baidunetdisk-cli/internal/config"
)

type configCmd struct {
	Show  configShowCmd  `cmd:"" default:"1" help:"显示设置"`
	Set   configSetCmd   `cmd:"" help:"修改设置"`
	Reset configResetCmd `cmd:"" help:"恢复默认设置"`
}

// configResult is the settings; accounts and cookies are never shown.
type configResult struct {
	Path     string          `json:"path"`
	Settings config.Settings `json:"settings"`
}

func (r configResult) Human(w io.Writer) {
	s := r.Settings
	limit := func(n int64) string {
		if n == 0 {
			return "不限"
		}
		return size(n) + "/s"
	}
	table(w, [][]string{
		{"save-dir", s.SaveDir, "下载保存目录"},
		{"connections", fmt.Sprint(s.Connections), "每个下载的连接数"},
		{"parallel", fmt.Sprint(s.Parallel), "同时传输的文件数"},
		{"download-limit", limit(s.DownloadLimit), "下载限速"},
		{"upload-limit", limit(s.UploadLimit), "上传限速"},
		{"proxy", s.Proxy, "代理, 如 http://127.0.0.1:7890"},
	})
	fmt.Fprintf(w, "\n配置文件: %s\n", r.Path)
}

type configShowCmd struct{}

func (c *configShowCmd) Run(app *App) (Result, error) {
	return configResult{app.cfg.Path(), app.cfg.Settings}, nil
}

type configSetCmd struct {
	SaveDir       *string   `help:"下载保存目录" type:"path"`
	Connections   *int      `help:"每个下载的连接数"`
	Parallel      *int      `help:"同时传输的文件数"`
	DownloadLimit *byteSize `help:"下载限速, 如 2MB, 0 为不限"`
	UploadLimit   *byteSize `help:"上传限速, 如 512K, 0 为不限"`
	Proxy         *string   `help:"代理地址, 空字符串为不用代理"`
}

func (c *configSetCmd) Run(app *App) (Result, error) {
	s := &app.cfg.Settings
	if c.SaveDir != nil {
		s.SaveDir = *c.SaveDir
	}
	if c.Connections != nil {
		if *c.Connections < 1 || *c.Connections > 64 {
			return nil, inputf("connections 应在 1 到 64 之间")
		}
		s.Connections = *c.Connections
	}
	if c.Parallel != nil {
		if *c.Parallel < 1 || *c.Parallel > 16 {
			return nil, inputf("parallel 应在 1 到 16 之间")
		}
		s.Parallel = *c.Parallel
	}
	if c.DownloadLimit != nil {
		s.DownloadLimit = int64(*c.DownloadLimit)
	}
	if c.UploadLimit != nil {
		s.UploadLimit = int64(*c.UploadLimit)
	}
	if c.Proxy != nil {
		s.Proxy = *c.Proxy
	}
	if err := app.cfg.Save(); err != nil {
		return nil, err
	}
	return configResult{app.cfg.Path(), *s}, nil
}

type configResetCmd struct{}

func (c *configResetCmd) Run(app *App) (Result, error) {
	app.cfg.Settings = config.Defaults()
	if err := app.cfg.Save(); err != nil {
		return nil, err
	}
	return configResult{app.cfg.Path(), app.cfg.Settings}, nil
}
