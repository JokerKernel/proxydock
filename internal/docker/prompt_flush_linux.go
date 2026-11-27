package docker

import (
	"os"
	"syscall"
)

// Linux TCFLSH / TCIFLUSH. amd64 and arm64 share this request number.
const (
	ioctlTCFLSH   uintptr = 0x540B
	ioctlTCIFLUSH uintptr = 0
)

func (u *ui) discardPendingInput() {
	if u == nil {
		return
	}
	u.discardBufferedInput()
	file, ok := u.raw.(*os.File)
	if !ok {
		return
	}
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), ioctlTCFLSH, ioctlTCIFLUSH)
}
