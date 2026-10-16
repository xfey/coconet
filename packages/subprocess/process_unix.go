//go:build unix

package subprocess

import (
	"os/exec"
	"syscall"
)

func prepare(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.Setpgid = true
	command.SysProcAttr.Pgid = 0
}

func stop(command *exec.Cmd) {
	// Only the process group created for this invocation is addressed.
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}

func terminate(command *exec.Cmd) {
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
}
