//go:build unix

package git

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcessGroup runs Git in its own process group so that cancelling the
// command can kill the whole tree. Git spawns helpers (git-remote-https,
// git-upload-pack, git index-pack) that inherit its pipes; without this they
// outlive the parent and hold us blocked after a timeout.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
}
