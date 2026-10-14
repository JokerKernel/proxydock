package docker

import (
	"os"
	"syscall"
	"time"
)

func (u *ui) readLineInterruptible() (string, error) {
	var pending []byte
	for {
		if u.signals != nil && u.signals.takeLatch() {
			u.discardPendingInput()
			return "", errInterrupted
		}
		if u.reader.Buffered() == 0 {
			if err := u.waitStdinOrInterrupt(); err != nil {
				u.discardPendingInput()
				return "", err
			}
		}
		one, err := u.reader.ReadByte()
		if err != nil {
			if len(pending) == 0 {
				return "", err
			}
			return string(pending), err
		}
		if one == '\n' {
			return string(pending), nil
		}
		pending = append(pending, one)
	}
}

func (u *ui) waitStdinOrInterrupt() error {
	stdin, stdinOK := fileFD(u.raw)
	intr, intrOK := fileFD(u.intrR)
	if !stdinOK || !intrOK || u.signals == nil {
		return nil
	}
	u.signals.setInputWaiting(true)
	defer u.signals.setInputWaiting(false)
	if u.signals.takeLatch() {
		return errInterrupted
	}
	for {
		var set syscall.FdSet
		max := stdin
		if intr > max {
			max = intr
		}
		setFd(&set, stdin)
		setFd(&set, intr)
		_, err := syscall.Select(max+1, &set, nil, nil, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if fdReady(&set, intr) {
			u.drainInterrupt()
			return errInterrupted
		}
		if fdReady(&set, stdin) {
			return nil
		}
	}
}

func fileFD(value any) (int, bool) {
	file, ok := value.(*os.File)
	if !ok || file == nil {
		return 0, false
	}
	return int(file.Fd()), true
}

func (u *ui) drainInterrupt() {
	if u.intrR == nil {
		return
	}
	_ = u.intrR.SetReadDeadline(time.Now())
	buf := make([]byte, 64)
	for {
		_, err := u.intrR.Read(buf)
		if err != nil {
			break
		}
	}
	_ = u.intrR.SetReadDeadline(time.Time{})
}

func setFd(set *syscall.FdSet, fd int) {
	if fd < 0 || fd >= len(set.Bits)*64 {
		return
	}
	set.Bits[fd/64] |= 1 << (uint(fd) % 64)
}

func fdReady(set *syscall.FdSet, fd int) bool {
	if fd < 0 || fd >= len(set.Bits)*64 {
		return false
	}
	return set.Bits[fd/64]&(1<<(uint(fd)%64)) != 0
}
