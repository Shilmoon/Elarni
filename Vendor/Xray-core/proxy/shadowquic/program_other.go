//go:build !windows

package shadowquic

import "os/exec"

func hideWindow(*exec.Cmd) {}
