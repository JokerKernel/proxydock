package docker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
)

type commandSignalKey struct{}

// commandSignals is the single listener for interrupt and terminate.
// A nested follow can borrow SIGINT so it stops only that operation.
// In the interactive menu, SIGINT never cancels the session itself.
type commandSignals struct {
	mu           sync.Mutex
	ch           chan os.Signal
	cancel       context.CancelFunc
	nested       chan os.Signal
	done         chan struct{}
	once         sync.Once
	menu         bool
	latched      bool
	inputWaiting bool
	intrW        *os.File
}

// notifyCommand cancels ctx on SIGINT or SIGTERM until stop is called.
func notifyCommand(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	signals := &commandSignals{
		ch:     make(chan os.Signal, 1),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	signal.Notify(signals.ch, os.Interrupt, syscall.SIGTERM)
	go signals.loop()
	return context.WithValue(ctx, commandSignalKey{}, signals), func() {
		signals.once.Do(func() {
			signal.Stop(signals.ch)
			close(signals.done)
			cancel()
		})
	}
}

func (s *commandSignals) loop() {
	for {
		select {
		case <-s.done:
			return
		case sig := <-s.ch:
			if sig != os.Interrupt {
				s.cancel()
				continue
			}
			s.mu.Lock()
			nested := s.nested
			menu := s.menu
			waiting := s.inputWaiting
			writer := s.intrW
			s.mu.Unlock()
			if nested != nil {
				select {
				case nested <- sig:
				default:
				}
				continue
			}
			if menu {
				if waiting && writer != nil {
					_, _ = writer.Write([]byte{1})
					continue
				}
				s.mu.Lock()
				s.latched = true
				s.mu.Unlock()
				continue
			}
			s.cancel()
		}
	}
}

func (s *commandSignals) enableMenu(writer *os.File) {
	s.mu.Lock()
	s.menu = true
	s.intrW = writer
	s.mu.Unlock()
}

func (s *commandSignals) disableMenu() {
	s.mu.Lock()
	s.menu = false
	s.latched = false
	s.inputWaiting = false
	s.intrW = nil
	s.mu.Unlock()
}

func (s *commandSignals) setInputWaiting(waiting bool) {
	s.mu.Lock()
	s.inputWaiting = waiting
	s.mu.Unlock()
}

func (s *commandSignals) menuEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.menu
}

func (s *commandSignals) borrowed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nested != nil
}

func (s *commandSignals) takeLatch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	latched := s.latched
	s.latched = false
	return latched
}

func signalsFrom(ctx context.Context) *commandSignals {
	signals, _ := ctx.Value(commandSignalKey{}).(*commandSignals)
	return signals
}

// attachMenuSignals makes SIGINT leave the session alive while Menu runs.
func attachMenuSignals(ctx context.Context, u *ui) func() {
	signals := signalsFrom(ctx)
	if signals == nil || u == nil || !u.interactive {
		return func() {}
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return func() {}
	}
	u.signals = signals
	u.intrR = reader
	signals.enableMenu(writer)
	return func() {
		signals.disableMenu()
		u.signals = nil
		u.intrR = nil
		_ = reader.Close()
		_ = writer.Close()
	}
}

// callContext borrows SIGINT for one Docker call inside the menu.
// A call that is already running under borrowInterrupt keeps its context.
func (m *Manager) callContext(ctx context.Context) (context.Context, func()) {
	signals := signalsFrom(ctx)
	if signals == nil || !signals.menuEnabled() || signals.borrowed() {
		return ctx, func() {}
	}
	return borrowInterrupt(ctx)
}

func interruptedCall(parent, child context.Context, err error) error {
	if err == nil || child == parent || parent.Err() != nil {
		return err
	}
	if child.Err() != nil || errors.Is(err, context.Canceled) || exitedOnInterrupt(err) {
		return errInterrupted
	}
	return err
}

// borrowInterrupt returns a context canceled by SIGINT without canceling ctx.
// SIGTERM still cancels ctx, and therefore the borrowed context.
func borrowInterrupt(ctx context.Context) (context.Context, context.CancelFunc) {
	signals, ok := ctx.Value(commandSignalKey{}).(*commandSignals)
	if !ok || signals == nil {
		return signal.NotifyContext(ctx, os.Interrupt)
	}
	child, cancel := context.WithCancel(ctx)
	nested := make(chan os.Signal, 1)
	stopped := make(chan struct{})
	signals.mu.Lock()
	signals.nested = nested
	latched := signals.latched
	signals.latched = false
	signals.mu.Unlock()
	if latched {
		cancel()
	}
	go func() {
		select {
		case <-nested:
			cancel()
		case <-child.Done():
		case <-stopped:
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			signals.mu.Lock()
			if signals.nested == nested {
				signals.nested = nil
			}
			signals.mu.Unlock()
			close(stopped)
			select {
			case <-nested:
				cancel()
			default:
			}
		})
	}
	return child, stop
}

func exitedOnInterrupt(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGINT
}
