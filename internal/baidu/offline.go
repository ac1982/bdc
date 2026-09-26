package baidu

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OfflineTask is a download Baidu runs on its own servers.
type OfflineTask struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	Source   string    `json:"source"`
	SavePath string    `json:"savePath"`
	Status   int       `json:"status"` // see OfflineStatus
	Size     int64     `json:"size"`
	Done     int64     `json:"done"`
	Created  time.Time `json:"created"`
}

// OfflineStatus describes a task status code.
var OfflineStatus = map[int]string{
	0: "下载成功", 1: "下载中", 2: "系统错误", 3: "资源不存在", 4: "下载超时",
	5: "下载失败", 6: "空间不足", 7: "已取消",
}

type rawTask struct {
	ID         flexInt `json:"task_id"`
	Name       string  `json:"task_name"`
	Source     string  `json:"source_url"`
	SavePath   string  `json:"save_path"`
	Status     flexInt `json:"status"`
	FileSize   flexInt `json:"file_size"`
	Finished   flexInt `json:"finished_size"`
	CreateTime flexInt `json:"create_time"`
}

func (r rawTask) task() OfflineTask {
	return OfflineTask{ID: int64(r.ID), Name: r.Name, Source: r.Source, SavePath: r.SavePath, Status: int(r.Status),
		Size: int64(r.FileSize), Done: int64(r.Finished), Created: time.Unix(int64(r.CreateTime), 0)}
}

func cloudDL(method string, q url.Values) string {
	q.Set("method", method)
	q.Set("app_id", "250528")
	return panBase + "rest/2.0/services/cloud_dl?" + q.Encode()
}

// AddOfflineTask asks Baidu to download source (http, https, magnet or ed2k) into dir.
func (c *Client) AddOfflineTask(ctx context.Context, source, dir string) (int64, error) {
	var resp struct {
		TaskID flexInt `json:"task_id"`
	}
	q := url.Values{"source_url": {source}, "save_path": {dir}, "task_from": {"0"}, "selected_idx": {"1"}}
	err := c.do(ctx, &request{op: "添加离线下载 " + source, method: "POST", url: cloudDL("add_task", q)}, &resp)
	return int64(resp.TaskID), err
}

// OfflineTasks lists all offline tasks.
func (c *Client) OfflineTasks(ctx context.Context) ([]OfflineTask, error) {
	var resp struct {
		Tasks []rawTask `json:"task_info"`
	}
	q := url.Values{"need_task_info": {"1"}, "status": {"255"}, "start": {"0"}, "limit": {"1000"}}
	if err := c.do(ctx, &request{op: "列出离线下载", method: "POST", url: cloudDL("list_task", q)}, &resp); err != nil {
		return nil, err
	}
	tasks := make([]OfflineTask, len(resp.Tasks))
	for i, r := range resp.Tasks {
		tasks[i] = r.task()
	}
	return tasks, nil
}

// OfflineTasksByID returns the given tasks.
func (c *Client) OfflineTasksByID(ctx context.Context, ids ...int64) ([]OfflineTask, error) {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	var resp struct {
		Tasks map[string]rawTask `json:"task_info"`
	}
	q := url.Values{"op_type": {"1"}, "task_ids": {strings.Join(s, ",")}}
	if err := c.do(ctx, &request{op: "查询离线下载", url: cloudDL("query_task", q)}, &resp); err != nil {
		return nil, err
	}
	var tasks []OfflineTask
	for _, id := range s {
		if r, ok := resp.Tasks[id]; ok {
			r.ID = flexInt(mustAtoi(id))
			tasks = append(tasks, r.task())
		}
	}
	return tasks, nil
}

// CancelOfflineTask stops a running task.
func (c *Client) CancelOfflineTask(ctx context.Context, id int64) error {
	q := url.Values{"task_id": {strconv.FormatInt(id, 10)}}
	return c.do(ctx, &request{op: "取消离线下载", method: "POST", url: cloudDL("cancel_task", q)}, nil)
}

// DeleteOfflineTask removes a task from the list.
func (c *Client) DeleteOfflineTask(ctx context.Context, id int64) error {
	q := url.Values{"task_id": {strconv.FormatInt(id, 10)}}
	return c.do(ctx, &request{op: "删除离线下载", method: "POST", url: cloudDL("delete_task", q)}, nil)
}

// ClearOfflineTasks removes all finished and failed tasks from the list.
func (c *Client) ClearOfflineTasks(ctx context.Context) error {
	return c.do(ctx, &request{op: "清空离线下载", method: "POST", url: cloudDL("clear_task", url.Values{})}, nil)
}

func mustAtoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
