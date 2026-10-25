//go:build linux || darwin

package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockPoll is how often a waiting process retries the lock. flock has no timeout of its own, so
// the wait is a non-blocking attempt in a loop.
const lockPoll = 25 * time.Millisecond

// lockExclusive takes an exclusive advisory lock on path (created 0600 if absent), waiting at most
// timeout. flock locks belong to the open file description, so two goroutines of one process
// exclude each other exactly as two processes do.
func lockExclusive(ctx context.Context, path string, timeout time.Duration) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("another kubectl-topology process holds %s; gave up after %s", path, timeout)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
