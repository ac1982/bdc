package baidu

import (
	"errors"
	"fmt"
)

// Classes of failure, for errors.Is. An *Error matches the class of its code.
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrAuth     = errors.New("not logged in or login expired")
	ErrInvalid  = errors.New("invalid input")
)

// Error is a failure reported by Baidu, or a failure to reach it.
type Error struct {
	Op      string      // what was being done, e.g. "列出目录 /a"
	Code    int         // Baidu's error code; 0 for network and decoding failures
	Message string      // Baidu's message, or our explanation of the code
	Err     error       // the underlying error, if any
	Items   []BatchItem // a failed batch call: Baidu's answer per item, when it gave one
}

// BatchItem is Baidu's answer for one item of a batch call; Errno 0 is done.
type BatchItem struct {
	Path  string  `json:"path"`
	Errno flexInt `json:"errno"`
}

func (e *Error) Error() string {
	switch {
	case e.Code != 0:
		return fmt.Sprintf("%s: %s (错误码 %d)", e.Op, e.Message, e.Code)
	case e.Message != "":
		return fmt.Sprintf("%s: %s", e.Op, e.Message)
	}
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) Is(target error) bool {
	return target != nil && codeClass[e.Code] == target
}

// Code returns Baidu's error code in err, or 0.
func Code(err error) int {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return 0
}

// errBatch is the errno of a batch call where some items failed.
const errBatch = 12

// codeClass maps the codes we understand to a class.
var codeClass = map[int]error{
	-6:    ErrAuth,     // 身份验证失败
	132:   ErrAuth,     // 帐号存在安全风险, 需要安全验证
	-9:    ErrNotFound, // 文件或目录不存在
	31066: ErrNotFound, // file does not exist
	-8:    ErrExists,   // 文件或目录已存在
	-30:   ErrExists,   // 目标已存在
	31061: ErrExists,   // file already exists
	-7:    ErrInvalid,  // 文件名非法
	-12:   ErrInvalid,  // 提取码错误
	31023: ErrInvalid,  // param error
}

// codeMessage explains codes whose server message is empty or unhelpful.
var codeMessage = map[int]string{
	-6:    "身份验证失败, 请重新登录",
	132:   "百度要求安全验证 (操作过于频繁或帐号有风险), 请在网页或手机上完成验证后重试",
	-7:    "文件名非法",
	-8:    "文件或目录已存在",
	-30:   "目标已存在",
	-9:    "文件或目录不存在",
	-12:   "提取码错误",
	31061: "文件或目录已存在",
	31066: "文件或目录不存在",
}
