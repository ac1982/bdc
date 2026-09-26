package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
)

// verify passes Baidu's security check with the person at the terminal: they
// pick where the code is sent, from what the account has, and type it in.
// Giving up cancels the command: its other requests would only be stopped
// again.
func (a *App) verify(ctx context.Context, k *baidu.Check) error {
	if a.meter != nil {
		defer a.meter.hold()()
	}
	err := a.checkDialog(ctx, k)
	if errors.Is(err, errCancelled) {
		a.cancel()
	}
	return err
}

func (a *App) checkDialog(ctx context.Context, k *baidu.Check) error {
	methods, err := k.Methods(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stderr, "百度要求安全验证 (操作过于频繁或帐号有风险). 验证码可以发到:")
	for i, m := range methods {
		fmt.Fprintf(a.stderr, "  %d) %s %s\n", i+1, methodNames[m.Type], m.To)
	}
	var m baidu.Method
	for m.Type == "" {
		answer, err := a.ask(ctx, fmt.Sprintf("选择 1-%d (回车选 1, q 放弃): ", len(methods)))
		if err != nil || answer == "q" {
			return errCancelled
		}
		if n, err := strconv.Atoi(answer); answer == "" || err == nil && n >= 1 && n <= len(methods) {
			m = methods[max(n, 1)-1]
		}
	}
	for send := true; ; {
		if send {
			send = false
			switch err := k.Send(ctx, m.Type); {
			case err == nil:
				fmt.Fprintf(a.stderr, "验证码已发送到 %s.\n", m.To)
			case baidu.Rejected(err): // e.g. sent too often: the person decides
				fmt.Fprintln(a.stderr, "发送失败:", err)
			default:
				return err
			}
		}
		code, err := a.ask(ctx, "输入验证码 (r 重新发送, q 放弃): ")
		switch {
		case err != nil || code == "q":
			return errCancelled
		case code == "r":
			send = true
			continue
		case code == "":
			continue
		}
		switch err := k.Submit(ctx, code); {
		case err == nil:
			fmt.Fprintln(a.stderr, "验证通过, 继续.")
			return nil
		case baidu.Rejected(err): // a wrong or expired code: the person may try again
			fmt.Fprintln(a.stderr, "验证失败:", err)
		default:
			return err
		}
	}
}

var methodNames = map[string]string{"sms": "短信", "email": "邮箱"}
