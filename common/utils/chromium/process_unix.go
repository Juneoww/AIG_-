//go:build !windows

package chromium

import (
	"os/exec"
	"syscall"
)

// Linux 进程组保证取消时同时回收 Chromium 的 renderer 子进程。
func configureBrowserProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func killBrowserProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
