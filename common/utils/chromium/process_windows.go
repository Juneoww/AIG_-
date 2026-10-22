package chromium

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func configureBrowserProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
func killBrowserProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Windows 使用系统进程树终止命令，避免遗留 renderer 子进程。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kill := exec.CommandContext(ctx, "taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := kill.Run(); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
