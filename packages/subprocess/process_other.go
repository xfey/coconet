//go:build !unix

package subprocess

import "os/exec"

func prepare(command *exec.Cmd)   {}
func stop(command *exec.Cmd)      { _ = command.Process.Kill() }
func terminate(command *exec.Cmd) { stop(command) }
