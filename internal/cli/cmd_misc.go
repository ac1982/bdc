package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/ac1982/bdc/internal/baidu"
	"github.com/ac1982/bdc/internal/update"
)

// recycle

type recycleCmd struct {
	List    recycleListCmd    `cmd:"" help:"列出回收站"`
	Restore recycleRestoreCmd `cmd:"" help:"还原文件"`
	Delete  recycleDeleteCmd  `cmd:"" help:"彻底删除文件 (百度可能要求在网页或手机上完成安全验证)"`
}

type recycleListCmd struct{}

type recycledResult struct {
	Files []baidu.Deleted `json:"files"`
}

func (r recycledResult) Human(w io.Writer) {
	rows := [][]string{{"fs_id", "大小", "删除时间", "剩余天数", "路径"}}
	for _, f := range r.Files {
		sz := size(f.Size)
		if f.IsDir {
			sz = "-"
		}
		rows = append(rows, []string{strconv.FormatInt(f.FsID, 10), sz, clock(f.Mtime), strconv.Itoa(f.DaysLeft), f.Path})
	}
	table(w, rows)
	fmt.Fprintf(w, "共 %d 项\n", len(r.Files))
}

func (c *recycleListCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	files, err := client.Recycled(app.ctx)
	return recycledResult{append([]baidu.Deleted{}, files...)}, err
}

type recycleRestoreCmd struct {
	IDs []int64 `arg:"" name:"fs_id" help:"要还原的文件的 fs_id (见 recycle list)"`
}

func (c *recycleRestoreCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	if err := client.Restore(app.ctx, c.IDs...); err != nil {
		return nil, err
	}
	return idsResult{c.IDs, "还原"}, nil
}

type recycleDeleteCmd struct {
	IDs []int64 `arg:"" name:"fs_id" help:"要彻底删除的文件的 fs_id"`
	Yes bool    `short:"y" help:"不再确认"`
}

func (c *recycleDeleteCmd) Run(app *App) (Result, error) {
	if err := app.confirm(c.Yes, fmt.Sprintf("彻底删除 %d 个文件, 无法恢复?", len(c.IDs))); err != nil {
		return nil, err
	}
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	if err := client.Purge(app.ctx, c.IDs...); err != nil {
		return nil, err
	}
	return idsResult{c.IDs, "彻底删除"}, nil
}

// offline

type offlineCmd struct {
	Add    offlineAddCmd    `cmd:"" help:"添加离线下载任务"`
	List   offlineListCmd   `cmd:"" help:"列出任务"`
	Cancel offlineCancelCmd `cmd:"" help:"取消任务"`
	Delete offlineDeleteCmd `cmd:"" help:"删除任务记录"`
}

type offlineAddCmd struct {
	URLs []string `arg:"" name:"url" help:"http(s), magnet 或 ed2k 链接"`
	To   string   `help:"保存到此网盘目录, 默认为工作目录"`
}

type offlineAddResult struct {
	IDs []int64 `json:"ids"`
}

func (r offlineAddResult) Human(w io.Writer) {
	for _, id := range r.IDs {
		fmt.Fprintln(w, "已添加任务", id)
	}
}

func (c *offlineAddCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	r := offlineAddResult{IDs: []int64{}}
	for _, u := range c.URLs {
		id, err := client.AddOfflineTask(app.ctx, u, app.abs(c.To))
		if err != nil {
			return r, err
		}
		r.IDs = append(r.IDs, id)
	}
	return r, nil
}

type offlineListCmd struct {
	IDs []int64 `arg:"" optional:"" name:"id" help:"只列出这些任务"`
}

type tasksResult struct {
	Tasks []baidu.OfflineTask `json:"tasks"`
}

func (r tasksResult) Human(w io.Writer) {
	if len(r.Tasks) == 0 {
		fmt.Fprintln(w, "没有离线下载任务")
		return
	}
	rows := [][]string{{"id", "状态", "进度", "名称", "保存到"}}
	for _, t := range r.Tasks {
		progress := size(t.Done) + "/" + size(t.Size)
		rows = append(rows, []string{strconv.FormatInt(t.ID, 10), baidu.OfflineStatus[t.Status], progress, t.Name, t.SavePath})
	}
	table(w, rows)
}

func (c *offlineListCmd) Run(app *App) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	var tasks []baidu.OfflineTask
	if len(c.IDs) > 0 {
		tasks, err = client.OfflineTasksByID(app.ctx, c.IDs...)
	} else {
		tasks, err = client.OfflineTasks(app.ctx)
	}
	return tasksResult{append([]baidu.OfflineTask{}, tasks...)}, err
}

type offlineCancelCmd struct {
	IDs []int64 `arg:"" name:"id" help:"任务 id"`
}

func (c *offlineCancelCmd) Run(app *App) (Result, error) {
	return eachID(app, c.IDs, "取消", (*baidu.Client).CancelOfflineTask)
}

type offlineDeleteCmd struct {
	IDs []int64 `arg:"" name:"id" help:"任务 id"`
}

func (c *offlineDeleteCmd) Run(app *App) (Result, error) {
	return eachID(app, c.IDs, "删除", (*baidu.Client).DeleteOfflineTask)
}

// eachID applies op to each id, stopping at the first error.
func eachID(app *App, ids []int64, verb string, op func(*baidu.Client, context.Context, int64) error) (Result, error) {
	client, err := app.baidu()
	if err != nil {
		return nil, err
	}
	r := idsResult{IDs: []int64{}, verb: verb}
	for _, id := range ids {
		if err := op(client, app.ctx, id); err != nil {
			return r, err
		}
		r.IDs = append(r.IDs, id)
	}
	return r, nil
}

// update

type updateCmd struct {
	Check bool `help:"只检查, 不安装"`
	Yes   bool `short:"y" help:"不再确认"`
}

type updateResult struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Installed bool   `json:"installed"`
	URL       string `json:"url"`
}

func (r updateResult) Human(w io.Writer) {
	switch {
	case r.Installed:
		fmt.Fprintf(w, "已更新到 %s\n", r.Latest)
	case r.Available:
		fmt.Fprintf(w, "有新版本 %s (当前 %s): %s\n", r.Latest, r.Current, r.URL)
	default:
		fmt.Fprintf(w, "已是最新版本 %s\n", r.Current)
	}
}

func (c *updateCmd) Run(app *App) (Result, error) {
	rel, err := update.Latest(app.ctx)
	if err != nil {
		return nil, err
	}
	r := updateResult{Current: Version, Latest: rel.Version, URL: rel.URL, Available: update.Newer(rel.Version, Version)}
	if c.Check || !r.Available {
		return r, nil
	}
	if err := app.confirm(c.Yes, fmt.Sprintf("安装 %s?", rel.Version)); err != nil {
		return r, err
	}
	if err := rel.Install(app.ctx); err != nil {
		return r, err
	}
	r.Installed = true
	return r, nil
}
