package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"mihomoctl/internal/profile"
)

const operationLockPollInterval = 100 * time.Millisecond

type operationLock struct {
	file *os.File
}

func acquireOperationLock(ctx context.Context, path string) (*operationLock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("操作锁路径必须是绝对路径")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建操作锁目录失败: %w", err)
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开操作锁失败: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("打开操作锁失败")
	}
	closeWithError := func(cause error) (*operationLock, error) {
		return nil, errors.Join(cause, file.Close())
	}
	info, err := file.Stat()
	if err != nil {
		return closeWithError(fmt.Errorf("检查操作锁失败: %w", err))
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return closeWithError(errors.New("操作锁必须是当前用户所有的普通文件"))
	}
	if err := file.Chmod(0o600); err != nil {
		return closeWithError(fmt.Errorf("设置操作锁权限失败: %w", err))
	}

	ticker := time.NewTicker(operationLockPollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return closeWithError(err)
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if contextErr := ctx.Err(); contextErr != nil {
				unlockErr := unix.Flock(fd, unix.LOCK_UN)
				return closeWithError(errors.Join(contextErr, unlockErr))
			}
			return &operationLock{file: file}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return closeWithError(fmt.Errorf("获取操作锁失败: %w", err))
		}
		select {
		case <-ctx.Done():
			return closeWithError(ctx.Err())
		case <-ticker.C:
		}
	}
}

func (lock *operationLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	return errors.Join(err, lock.file.Close())
}

// withMutation serializes every privileged state-changing operation across
// CLI, TUI and systemd timer processes, then refreshes disk-backed state so a
// waiting process cannot overwrite a newer operation with stale memory.
func (a *App) withMutation(ctx context.Context, operation func() error) (resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if os.Geteuid() == 0 {
		if err := ensureRootApplicationDirectories(a.paths); err != nil {
			return &InvalidInputError{Cause: fmt.Errorf("验证 root 受管目录失败: %w", err)}
		}
	}
	lock, err := acquireOperationLock(ctx, a.paths.OperationLock)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, lock.release())
	}()

	a.mu.Lock()
	a.reloadDiskStateLocked()
	a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}

func (a *App) reloadDiskStateLocked() {
	a.state = nil
	a.store = nil
	a.stateLoadErr = nil
	if err := a.loadState(); err == nil {
		store, storeErr := profile.NewStore(a.paths.ProfileRoot)
		if storeErr != nil {
			a.stateLoadErr = classifyProfileStoreError(storeErr)
		} else {
			a.store = store
		}
	} else if !isMissing(err) {
		a.stateLoadErr = err
	}

	a.client = nil
	a.clientLoadErr = nil
	if err := a.loadClient(); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.clientLoadErr = err
	}
	a.rebuildAPI()
}
