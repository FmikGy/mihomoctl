package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type cancelAfterChecksContext struct {
	context.Context
	checks atomic.Int32
	allow  int32
}

func (c *cancelAfterChecksContext) Err() error {
	if c.checks.Add(1) > c.allow {
		return context.Canceled
	}
	return nil
}

func TestOperationLockWaitCanBeCanceled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.lock")
	first, err := acquireOperationLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = acquireOperationLock(ctx, path)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait error = %v", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatalf("canceled lock wait took too long: %v", time.Since(started))
	}
}

func TestOperationLockRejectsAlreadyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "operation.lock")
	lock, err := acquireOperationLock(ctx, path)
	if lock != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("acquireOperationLock() = %#v, %v", lock, err)
	}
}

func TestOperationLockRejectsSpecialFileWithoutChangingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.fifo")
	if err := syscall.Mkfifo(path, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := acquireOperationLock(context.Background(), path)
	if lock != nil || err == nil {
		t.Fatalf("special-file lock = %#v, %v", lock, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("special-file mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
	}
}

func TestOperationLockRejectsHardLinkWithoutChangingTarget(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	path := filepath.Join(directory, "operation.lock")
	if err := os.WriteFile(target, []byte("keep"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, path); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireOperationLock(context.Background(), path)
	if lock != nil || err == nil {
		t.Fatalf("hard-link lock = %#v, %v", lock, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("hard-link target mode changed to %o", info.Mode().Perm())
	}
}

func TestOperationLockAllowsNextWaiterAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.lock")
	first, err := acquireOperationLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = first.release()
		close(released)
	}()
	second, err := acquireOperationLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.release()
	<-released
}

func TestWithMutationRechecksCancellationAfterReload(t *testing.T) {
	paths := testPaths(t.TempDir())
	application, err := New(WithPaths(paths), WithRunner(&fakeRunner{}), WithEUID(func() int { return 1000 }))
	if err != nil {
		t.Fatal(err)
	}
	ctx := &cancelAfterChecksContext{Context: context.Background(), allow: 3}
	called := false
	err = application.withMutation(ctx, func() error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("mutation after cancellation: error=%v called=%v checks=%d", err, called, ctx.checks.Load())
	}
}
