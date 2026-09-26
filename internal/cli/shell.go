package cli

// shell runs the interactive shell.
func (a *App) shell() int {
	return a.report("", nil, usagef("交互模式尚未实现"))
}
