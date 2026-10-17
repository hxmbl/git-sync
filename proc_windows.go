//go:build !unix

package main

import "os/exec"

// setProcessGroup is a no-op on platforms without process groups, where
// cancelling the command kills the process directly and WaitDelay bounds any
// helper still holding the output pipes.
func setProcessGroup(cmd *exec.Cmd) {}
