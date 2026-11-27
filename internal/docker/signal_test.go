package docker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
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

func TestMenuInterruptDoesNotCancelTheSession(t *testing.T) {
	ctx, stop := notifyCommand(context.Background())
	defer stop()
	signals := signalsFrom(ctx)
	signals.enableMenu(nil)
	defer signals.disableMenu()
	signalProcess(t, os.Interrupt)
	deadline := time.Now().Add(2 * time.Second)
	for !signals.takeLatch() {
		if time.Now().After(deadline) {
			t.Fatal("interrupt did not stay inside the menu")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatalf("menu interrupt canceled the session: %v", ctx.Err())
	}
}

func TestCtrlCAtMenuPromptQuits(t *testing.T) {
	m, _ := testManager(t)
	var out lockedBuffer
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = output.Close()
	})
	ui := newUI(input, &out)
	ui.interactive = true
	ctx, stop := notifyCommand(context.Background())
	defer stop()
	detach := attachMenuSignals(ctx, ui)
	defer detach()
	done := make(chan error, 1)
	go func() {
		done <- m.configMenu(ctx, ui)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), "请选择") {
		if time.Now().After(deadline) {
			t.Fatalf("prompt did not appear:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	signalProcess(t, os.Interrupt)
	select {
	case err := <-done:
		if !errors.Is(err, errQuit) {
			t.Fatalf("config menu interrupt = %v, want quit", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl+C did not leave the config menu")
	}
	if ctx.Err() != nil {
		t.Fatalf("quitting canceled the session: %v", ctx.Err())
	}
	text := out.String()
	if strings.Contains(text, "操作失败") || strings.Contains(text, "context canceled") || strings.Contains(text, "已取消") {
		t.Fatalf("interrupt was reported as a failure:\n%s", text)
	}
}

func TestCtrlCAtCancelablePromptQuits(t *testing.T) {
	var out lockedBuffer
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = output.Close()
	})
	ui := newUI(input, &out)
	ui.interactive = true
	ctx, stop := notifyCommand(context.Background())
	defer stop()
	detach := attachMenuSignals(ctx, ui)
	defer detach()
	done := make(chan error, 1)
	go func() {
		_, err := ui.chooseNumberCancelable("请选择", 1, 2, 1)
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), "请选择") {
		if time.Now().After(deadline) {
			t.Fatalf("prompt did not appear:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	signalProcess(t, os.Interrupt)
	select {
	case err := <-done:
		if !errors.Is(err, errQuit) {
			t.Fatalf("prompt interrupt = %v, want quit", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl+C did not leave the prompt")
	}
	if ctx.Err() != nil {
		t.Fatalf("quitting canceled the session: %v", ctx.Err())
	}
}

func TestDockerCallInterruptStaysInTheMenu(t *testing.T) {
	m, fake := testManager(t)
	fake.blockFollow = true
	fake.signalOnFollow = true
	ctx, stop := notifyCommand(context.Background())
	defer stop()
	signals := signalsFrom(ctx)
	signals.enableMenu(nil)
	defer signals.disableMenu()
	_, err := m.dockerOp(ctx, opLabel{}, "logs", "--follow", "abc")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("interrupt error = %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("docker interrupt canceled the session: %v", ctx.Err())
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
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
