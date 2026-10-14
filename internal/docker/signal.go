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
type commandSignals struct {
	mu     sync.Mutex
	ch     chan os.Signal
	cancel context.CancelFunc
	nested chan os.Signal
	done   chan struct{}
	once   sync.Once
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
			s.mu.Lock()
			nested := s.nested
			s.mu.Unlock()
			if sig == os.Interrupt && nested != nil {
				select {
				case nested <- sig:
				default:
				}
				continue
			}
			s.cancel()
		}
	}
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
	signals.mu.Unlock()
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
