package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
)

// Kind is the class of a failure; it decides the exit code.
type Kind string

const (
	Failed     Kind = "failed"
	Input      Kind = "input"
	Dependency Kind = "dependency"
	Auth       Kind = "auth"
	Usage      Kind = "usage"
	Cancelled  Kind = "cancelled"
)

// ExitCode of a kind; 0 for success.
func (k Kind) ExitCode() int {
	switch k {
	case Input:
		return 2
	case Dependency:
		return 3
	case Auth:
		return 4
	case Usage:
		return 64
	case Cancelled:
		return 130
	}
	return 1
}

// kindError marks an error with an explicit kind.
type kindError struct {
	kind Kind
	err  error
}

func (e *kindError) Error() string { return e.err.Error() }
func (e *kindError) Unwrap() error { return e.err }

func withKind(k Kind, err error) error { return &kindError{k, err} }

func inputf(format string, a ...any) error { return withKind(Input, fmt.Errorf(format, a...)) }
func usagef(format string, a ...any) error { return withKind(Usage, fmt.Errorf(format, a...)) }

// errNotLoggedIn is returned by commands that need an account when there is none.
var errNotLoggedIn = withKind(Auth, errors.New("尚未登录, 请先运行 bnd login"))

// classify finds the kind of err: an explicit mark wins, then cancellation,
// then what Baidu said, then failed.
func classify(err error) Kind {
	if k, ok := errors.AsType[*kindError](err); ok {
		return k.kind
	}
	switch {
	case errors.Is(err, context.Canceled):
		return Cancelled
	case errors.Is(err, baidu.ErrAuth):
		return Auth
	case errors.Is(err, baidu.ErrNotFound), errors.Is(err, baidu.ErrExists), errors.Is(err, baidu.ErrInvalid):
		return Input
	}
	return Failed
}
