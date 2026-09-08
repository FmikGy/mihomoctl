package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const operationRollbackTimeout = 30 * time.Second
const maxApplicationStateBytes = int64(16 << 20)

type fileCheckpoint struct {
	path       string
	exists     bool
	data       []byte
	mode       os.FileMode
	uid        int
	gid        int
	clientPlan *clientOwnerPlan
}

func (a *App) captureFiles(paths ...string) ([]fileCheckpoint, error) {
	seen := make(map[string]struct{}, len(paths))
	checkpoints := make([]fileCheckpoint, 0, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		if path == filepath.Clean(a.paths.ClientFile) {
			plan, err := planClientOwnership(path)
			if err != nil {
				return nil, err
			}
			if plan != nil {
				checkpoint, err := captureOwnedClientFile(path, plan)
				if err != nil {
					return nil, fmt.Errorf("保存 %s 的回滚点失败: %w", path, err)
				}
				checkpoints = append(checkpoints, checkpoint)
				continue
			}
		}
		data, info, err := readRegularFileNoFollow(path, maxApplicationStateBytes)
		if errors.Is(err, os.ErrNotExist) {
			checkpoints = append(checkpoints, fileCheckpoint{path: path})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("保存 %s 的回滚点失败: %w", path, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("无法读取 %s 的所有权", path)
		}
		checkpoints = append(checkpoints, fileCheckpoint{
			path: path, exists: true, data: data, mode: info.Mode().Perm(),
			uid: int(stat.Uid), gid: int(stat.Gid),
		})
	}
	return checkpoints, nil
}

func readRegularFileNoFollow(path string, limit int64) ([]byte, os.FileInfo, error) {
	if limit < 0 {
		return nil, nil, errors.New("文件大小上限无效")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, errors.New("打开文件描述符失败")
	}
	info, err := file.Stat()
	if err != nil {
		return nil, nil, errors.Join(err, file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.Join(errors.New("文件必须是普通文件"), file.Close())
	}
	if info.Size() > limit {
		return nil, nil, errors.Join(errors.New("文件超过大小上限"), file.Close())
	}
	content, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, nil, err
	}
	if int64(len(content)) > limit {
		return nil, nil, errors.New("文件超过大小上限")
	}
	return content, info, nil
}

func restoreFiles(checkpoints []fileCheckpoint) error {
	var restoreErrors []error
	for _, checkpoint := range checkpoints {
		if checkpoint.clientPlan != nil {
			if err := restoreOwnedClientFile(checkpoint); err != nil {
				restoreErrors = append(restoreErrors, fmt.Errorf("恢复 %s 失败: %w", checkpoint.path, err))
			}
			continue
		}
		if checkpoint.exists {
			if err := atomicWriteOwned(checkpoint.path, checkpoint.data, checkpoint.mode, checkpoint.uid, checkpoint.gid); err != nil {
				restoreErrors = append(restoreErrors, fmt.Errorf("恢复 %s 失败: %w", checkpoint.path, err))
			}
			continue
		}
		if err := os.Remove(checkpoint.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			restoreErrors = append(restoreErrors, fmt.Errorf("移除 %s 失败: %w", checkpoint.path, err))
			continue
		}
		if err := syncParentDirectory(checkpoint.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			restoreErrors = append(restoreErrors, fmt.Errorf("同步 %s 失败: %w", filepath.Dir(checkpoint.path), err))
		}
	}
	return errors.Join(restoreErrors...)
}

func syncParentDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

type rollbackStep struct {
	name string
	run  func(context.Context) error
}

type rollbackStack struct {
	steps []rollbackStep
}

func (a *App) fileRollback(paths ...string) (*rollbackStack, error) {
	files, err := a.captureFiles(paths...)
	if err != nil {
		return nil, err
	}
	stack := &rollbackStack{}
	stack.add("状态文件", func(context.Context) error { return restoreFiles(files) })
	return stack, nil
}

// failAndReload rolls files and runtime back, then makes the in-memory view
// match disk again.
func (a *App) failAndReload(ctx context.Context, stack *rollbackStack, cause error) error {
	err := stack.fail(ctx, cause)
	a.mu.Lock()
	a.reloadDiskStateLocked()
	a.mu.Unlock()
	return err
}

func (stack *rollbackStack) add(name string, run func(context.Context) error) {
	stack.steps = append(stack.steps, rollbackStep{name: name, run: run})
}

func (stack *rollbackStack) fail(ctx context.Context, cause error) error {
	if cause == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), operationRollbackTimeout)
	defer cancel()
	errs := []error{cause}
	for index := len(stack.steps) - 1; index >= 0; index-- {
		step := stack.steps[index]
		if err := step.run(rollbackCtx); err != nil {
			errs = append(errs, fmt.Errorf("%s回滚失败: %w", step.name, err))
		}
	}
	return errors.Join(errs...)
}
