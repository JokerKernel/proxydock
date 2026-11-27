//go:build linux

package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestCtrlCInSubmenuExitsTheMenu(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	master, slave := openMenuPTY(t)
	m.out = slave
	m.errOut = slave
	ctx, stop := notifyCommand(context.Background())
	defer stop()
	screen := &ptyScreen{file: master}
	done := make(chan error, 1)
	go func() {
		done <- m.Menu(ctx, slave)
	}()
	screen.waitFor(t, "请选择")
	if _, err := master.Write([]byte("2\n")); err != nil {
		t.Fatal(err)
	}
	screen.waitFor(t, "生成/更新配置")
	signalProcess(t, os.Interrupt)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("menu exit = %v\n%s", err, screen.buf.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Ctrl+C did not leave the menu:\n%s", screen.buf.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("quitting canceled the session: %v", ctx.Err())
	}
	text := screen.buf.String()
	if strings.Contains(text, "操作失败") || strings.Contains(text, "context canceled") || strings.Contains(text, "已取消") {
		t.Fatalf("interrupt was reported as a failure:\n%s", text)
	}
}

func openMenuPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	var number uint32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number)))
	if errno != 0 {
		t.Fatal(errno)
	}
	unlock := 0
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock)))
	if errno != 0 {
		t.Fatal(errno)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

type ptyScreen struct {
	file *os.File
	buf  bytes.Buffer
}

func (p *ptyScreen) waitFor(t *testing.T, needle string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	tmp := make([]byte, 1024)
	for !strings.Contains(p.buf.String(), needle) {
		if time.Now().After(deadline) {
			t.Fatalf("did not see %q:\n%s", needle, p.buf.String())
		}
		_ = p.file.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := p.file.Read(tmp)
		if n > 0 {
			p.buf.Write(tmp[:n])
		}
		if err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatal(err)
		}
	}
}
