package baidu

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// task is a background job (file operations, big share transfers) as
// share/taskquery reports it. A failed task lists only the items that
// failed; the others were done.
type task struct {
	Status    string `json:"status"` // pending, running, success or failed
	TaskErrno int    `json:"task_errno"`
	List      []struct {
		ErrorCode int    `json:"error_code"`
		From      string `json:"from"`
		To        string `json:"to"`
		Path      string `json:"path"`
	} `json:"list"`
}

// waitTask polls a background task until it ends. A failure is an *Error
// whose Items are the failed items.
func (c *Client) waitTask(ctx context.Context, op string, id int64) (task, error) {
	delay := 500 * time.Millisecond
	for {
		var t task
		if err := c.do(ctx, &request{op: op, path: "share/taskquery", query: url.Values{"taskid": {strconv.FormatInt(id, 10)}}}, &t); err != nil {
			return t, err
		}
		switch t.Status {
		case "success":
			return t, nil
		case "failed":
			e := &Error{Op: op, Code: t.TaskErrno}
			for _, it := range t.List {
				if it.ErrorCode == 0 {
					continue
				}
				if len(e.Items) == 0 {
					e.Code = it.ErrorCode // the first failure is more telling than the task's code
				}
				e.Items = append(e.Items, BatchItem{Path: firstNonEmpty(it.From, it.Path), Errno: flexInt(it.ErrorCode)})
			}
			e.Message = firstNonEmpty(codeMessage[e.Code], "操作失败")
			return t, e
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-time.After(delay):
		}
		delay = min(2*delay, 2*time.Second)
	}
}

// runTask sends a request that may start a background task (answering its
// taskid) and waits for the task to end.
func (c *Client) runTask(ctx context.Context, req *request) error {
	var resp struct {
		TaskID int64 `json:"taskid"`
	}
	if err := c.do(ctx, req, &resp); err != nil {
		return err
	}
	if resp.TaskID == 0 {
		return nil
	}
	_, err := c.waitTask(ctx, req.op, resp.TaskID)
	return err
}
