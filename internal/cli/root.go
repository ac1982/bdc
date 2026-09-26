package cli

import (
	"errors"
	"fmt"
	"io"
)

// root is the command grammar.
type root struct {
	JSON    bool        `name:"json" help:"stdout 只输出一份 JSON 文档, 日志和进度在 stderr"`
	Help    helpFlag    `short:"h" help:"显示帮助"`
	Version versionFlag `short:"v" help:"显示版本"`

	Login  loginCmd  `cmd:"" group:"帐号" help:"登录百度帐号"`
	Logout logoutCmd `cmd:"" group:"帐号" help:"退出当前帐号"`
	Who    whoCmd    `cmd:"" group:"帐号" help:"显示当前帐号"`
	Users  usersCmd  `cmd:"" group:"帐号" help:"列出已登录的帐号"`
	Su     suCmd     `cmd:"" group:"帐号" help:"切换帐号"`
	Quota  quotaCmd  `cmd:"" group:"帐号" help:"显示网盘容量"`

	Ls     lsCmd     `cmd:"" group:"浏览" aliases:"ll" help:"列出目录"`
	Tree   treeCmd   `cmd:"" group:"浏览" help:"以树形列出目录"`
	Meta   metaCmd   `cmd:"" group:"浏览" help:"显示文件或目录的详细信息"`
	Search searchCmd `cmd:"" group:"浏览" help:"按文件名搜索"`
	Cd     cdCmd     `cmd:"" group:"浏览" help:"切换工作目录"`
	Pwd    pwdCmd    `cmd:"" group:"浏览" help:"显示工作目录"`

	Mkdir mkdirCmd `cmd:"" group:"管理" help:"创建目录"`
	Rm    rmCmd    `cmd:"" group:"管理" help:"删除文件或目录 (进入回收站)"`
	Cp    cpCmd    `cmd:"" group:"管理" help:"复制文件或目录"`
	Mv    mvCmd    `cmd:"" group:"管理" help:"移动或重命名文件或目录"`

	Download downloadCmd `cmd:"" group:"传输" help:"下载文件或目录"`
	Upload   uploadCmd   `cmd:"" group:"传输" help:"上传文件或目录"`

	Share   shareCmd   `cmd:"" group:"分享" help:"分享链接: 创建, 列出, 取消, 转存"`
	Recycle recycleCmd `cmd:"" group:"管理" help:"回收站"`
	Offline offlineCmd `cmd:"" group:"传输" help:"离线下载: 由百度的服务器下载链接到网盘"`

	Config configCmd `cmd:"" group:"其他" help:"显示和修改设置"`
	Update updateCmd `cmd:"" group:"其他" help:"检查并安装新版本"`
}

var (
	errShowHelp    = errors.New("help")
	errShowVersion = errors.New("version")
)

// helpFlag and versionFlag stop parsing before validation, so --help works
// even when required arguments are missing; exec then prints the answer.
type helpFlag bool

func (helpFlag) IgnoreDefault()     {}
func (helpFlag) BeforeReset() error { return errShowHelp }

type versionFlag bool

func (versionFlag) BeforeReset() error { return errShowVersion }

type helpResult struct {
	Help string `json:"help"`
}

func (h helpResult) Human(w io.Writer) { io.WriteString(w, h.Help) }

type versionResult struct {
	Version string `json:"version"`
}

func (v versionResult) Human(w io.Writer) { fmt.Fprintln(w, "bnd", v.Version) }
