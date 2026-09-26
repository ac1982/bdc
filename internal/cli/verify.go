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
func (a *App) verify(ctx context.Context, k *baidu.Check) error {
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
		answer, err := a.ask(fmt.Sprintf("选择 1-%d (回车选 1, q 放弃): ", len(methods)))
		if err != nil || answer == "q" {
			return errCancelled
		}
		if n, err := strconv.Atoi(answer); answer == "" || err == nil && n >= 1 && n <= len(methods) {
			m = methods[max(n, 1)-1]
		}
	}
	for send := true; ; {
		if send {
			if err := k.Send(ctx, m.Type); err != nil {
				return err
			}
			fmt.Fprintf(a.stderr, "验证码已发送到 %s.\n", m.To)
		}
		code, err := a.ask("输入验证码 (r 重新发送, q 放弃): ")
		switch {
		case err != nil || code == "q":
			return errCancelled
		case code == "" || code == "r":
			send = code == "r"
			continue
		}
		send = false
		err = k.Submit(ctx, code)
		if e, ok := errors.AsType[*baidu.Error](err); ok && e.Code != 0 { // a wrong code: try again
			fmt.Fprintln(a.stderr, "验证失败:", e.Message)
			continue
		}
		if err == nil {
			fmt.Fprintln(a.stderr, "验证通过, 继续.")
		}
		return err
	}
}

var methodNames = map[string]string{"sms": "短信", "email": "邮箱"}
