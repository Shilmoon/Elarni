package shadowquic

import (
	"os/exec"
	"syscall"
)

// The core has no console; the client program must not open one.
func hideWindow(command *exec.Cmd) {
	const createNoWindow = 0x08000000
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
