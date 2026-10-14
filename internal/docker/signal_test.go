package docker

import (
	"bytes"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBorrowInterruptLeavesTheMenuAlive(t *testing.T) {
	parent, stop := notifyCommand(context.Background())
	defer stop()
	child, release := borrowInterrupt(parent)
	signalProcess(t, os.Interrupt)
	select {
	case <-child.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("interrupt did not stop the borrowed operation")
	}
	if parent.Err() != nil {
		t.Fatalf("interrupt canceled the menu: %v", parent.Err())
	}
	release()
	signalProcess(t, syscall.SIGTERM)
	select {
	case <-parent.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("terminate did not cancel the menu")
	}
}

func TestLiveLogsInterruptReturnsToServiceMenu(t *testing.T) {
	m, fake := deployed(t)
	fake.blockFollow = true
	fake.signalOnFollow = true
	var out, errOut bytes.Buffer
	m.out, m.errOut = &out, &errOut
	ctx, stop := notifyCommand(context.Background())
	defer stop()
	err := m.serviceMenu(ctx, newUI(strings.NewReader("6\n0\n"), &out))
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatalf("returning from logs canceled the menu: %v", ctx.Err())
	}
	if text := errOut.String(); strings.Contains(text, "操作失败") || strings.Contains(text, "context canceled") || strings.Contains(text, "signal:") {
		t.Fatalf("interrupt was reported as a failure:\n%s", text)
	}
	if !strings.Contains(out.String(), "实时日志") || !strings.Contains(out.String(), "0/q") {
		t.Fatalf("did not return to the service menu:\n%s", out.String())
	}
}

func signalProcess(t *testing.T, signal os.Signal) {
	t.Helper()
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = process.Signal(signal); err != nil {
		t.Fatal(err)
	}
}
