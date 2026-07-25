//go:build unix

package codex

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func prepareCmdForKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func forceKillCmd(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// Darwin may return EPERM (instead of ESRCH) when the process group is
	// already gone; treat both as success for idempotent kill.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil &&
		!errors.Is(err, os.ErrProcessDone) &&
		!errors.Is(err, syscall.ESRCH) &&
		!errors.Is(err, syscall.EPERM) {
		return err
	}
	return nil
}
