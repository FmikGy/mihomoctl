package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const clientTempAttempts = 32

const maxClientStateBytes = 1 << 20

// openOwnedClientDirectory resolves every user-controlled component relative
// to an already-open directory. No symlink is followed, and callers retain a
// descriptor for the exact directory they validated.
func openOwnedClientDirectory(plan *clientOwnerPlan, create, prepare bool) (int, error) {
	if plan == nil || plan.home == "" || plan.filename == "" {
		return -1, errors.New("客户端状态路径计划无效")
	}
	current, err := unix.Open(plan.home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("打开 sudo 用户主目录失败: %w", err)
	}
	closeCurrent := func(cause error) (int, error) {
		return -1, errors.Join(cause, unix.Close(current))
	}
	var homeStat unix.Stat_t
	if err := unix.Fstat(current, &homeStat); err != nil {
		return closeCurrent(fmt.Errorf("检查 sudo 用户主目录失败: %w", err))
	}
	if homeStat.Mode&unix.S_IFMT != unix.S_IFDIR || homeStat.Uid != uint32(plan.uid) {
		return closeCurrent(errors.New("sudo 用户主目录所有权无效"))
	}

	for index, component := range plan.directoryComponents {
		created := false
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			mkdirErr := unix.Mkdirat(current, component, 0o700)
			if mkdirErr == nil {
				created = true
				if err := unix.Fsync(current); err != nil {
					return closeCurrent(fmt.Errorf("同步客户端状态父目录失败: %w", err))
				}
			} else if !errors.Is(mkdirErr, unix.EEXIST) {
				return closeCurrent(fmt.Errorf("创建客户端状态目录失败: %w", mkdirErr))
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			return closeCurrent(fmt.Errorf("打开客户端状态目录失败: %w", openErr))
		}
		var stat unix.Stat_t
		if err := unix.Fstat(next, &stat); err != nil {
			_ = unix.Close(next)
			return closeCurrent(fmt.Errorf("检查客户端状态目录失败: %w", err))
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || !validClientDirectoryOwner(stat.Uid, plan.uid, os.Geteuid(), created) {
			_ = unix.Close(next)
			return closeCurrent(errors.New("客户端状态目录所有权无效"))
		}
		finalDirectory := index == len(plan.directoryComponents)-1
		if prepare && created {
			if err := unix.Fchown(next, plan.uid, plan.gid); err != nil {
				_ = unix.Close(next)
				return closeCurrent(fmt.Errorf("设置客户端状态目录所有者失败: %w", err))
			}
		}
		if prepare && (created || finalDirectory) {
			if err := unix.Fchmod(next, 0o700); err != nil {
				_ = unix.Close(next)
				return closeCurrent(fmt.Errorf("设置客户端状态目录权限失败: %w", err))
			}
		}
		if prepare && (created || finalDirectory) {
			if err := unix.Fsync(next); err != nil {
				_ = unix.Close(next)
				return closeCurrent(fmt.Errorf("同步客户端状态目录失败: %w", err))
			}
		}
		if err := unix.Close(current); err != nil {
			_ = unix.Close(next)
			return -1, err
		}
		current = next
	}
	return current, nil
}

func validClientDirectoryOwner(owner uint32, callerUID, creatorUID int, created bool) bool {
	if created {
		return owner == uint32(creatorUID)
	}
	return owner == uint32(callerUID)
}

func writeOwnedClientFile(plan *clientOwnerPlan, content []byte, mode os.FileMode, uid, gid int) error {
	if len(content) > maxClientStateBytes {
		return errors.New("客户端状态文件过大")
	}
	directoryFD, err := openOwnedClientDirectory(plan, true, true)
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)

	temporaryName, fileFD, err := createOwnedClientTemp(directoryFD)
	if err != nil {
		return err
	}
	renamed := false
	var file *os.File
	defer func() {
		if file != nil {
			_ = file.Close()
		} else if fileFD >= 0 {
			_ = unix.Close(fileFD)
		}
		if !renamed {
			_ = unix.Unlinkat(directoryFD, temporaryName, 0)
		}
	}()
	if err := unix.Fchown(fileFD, uid, gid); err != nil {
		return fmt.Errorf("设置客户端状态所有者失败: %w", err)
	}
	if err := unix.Fchmod(fileFD, uint32(mode.Perm())); err != nil {
		return fmt.Errorf("设置客户端状态权限失败: %w", err)
	}
	file = os.NewFile(uintptr(fileFD), temporaryName)
	if file == nil {
		return errors.New("创建客户端状态临时文件失败")
	}
	fileFD = -1
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("写入客户端状态失败: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("同步客户端状态失败: %w", err)
	}
	closeErr := file.Close()
	file = nil
	if closeErr != nil {
		return fmt.Errorf("关闭客户端状态失败: %w", closeErr)
	}
	if err := unix.Renameat(directoryFD, temporaryName, directoryFD, plan.filename); err != nil {
		return fmt.Errorf("发布客户端状态失败: %w", err)
	}
	renamed = true
	if err := unix.Fsync(directoryFD); err != nil {
		return fmt.Errorf("同步客户端状态父目录失败: %w", err)
	}
	return nil
}

func readLocalClientFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("打开客户端状态失败")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("检查客户端状态失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("客户端状态必须是普通文件")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		_ = file.Close()
		return nil, errors.New("客户端状态必须归当前用户所有")
	}
	if info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, errors.New("客户端状态权限过宽，必须禁止组和其他用户访问")
	}
	if info.Size() > maxClientStateBytes {
		_ = file.Close()
		return nil, errors.New("客户端状态文件过大")
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxClientStateBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("读取客户端状态失败: %w", err)
	}
	if len(content) > maxClientStateBytes {
		return nil, errors.New("客户端状态文件过大")
	}
	return content, nil
}

func readOwnedClientFile(plan *clientOwnerPlan) ([]byte, error) {
	directoryFD, err := openOwnedClientDirectory(plan, false, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(directoryFD)
	fileFD, err := unix.Openat(directoryFD, plan.filename, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("打开客户端状态失败: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fileFD, &stat); err != nil {
		_ = unix.Close(fileFD)
		return nil, fmt.Errorf("检查客户端状态失败: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(plan.uid) || stat.Gid != uint32(plan.gid) {
		_ = unix.Close(fileFD)
		return nil, errors.New("客户端状态必须是 sudo 用户及其主组所有的普通文件")
	}
	if stat.Mode&0o077 != 0 {
		_ = unix.Close(fileFD)
		return nil, errors.New("客户端状态权限过宽，必须禁止组和其他用户访问")
	}
	file := os.NewFile(uintptr(fileFD), plan.filename)
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, errors.New("打开客户端状态失败")
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxClientStateBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("读取客户端状态失败: %w", err)
	}
	if len(content) > maxClientStateBytes {
		return nil, errors.New("客户端状态文件过大")
	}
	return content, nil
}

func createOwnedClientTemp(directoryFD int) (string, int, error) {
	for range clientTempAttempts {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", -1, fmt.Errorf("生成客户端状态临时文件名失败: %w", err)
		}
		name := ".mihomoctl-" + hex.EncodeToString(nonce[:])
		fd, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, fmt.Errorf("创建客户端状态临时文件失败: %w", err)
		}
		return name, fd, nil
	}
	return "", -1, errors.New("无法创建唯一的客户端状态临时文件")
}

func captureOwnedClientFile(path string, plan *clientOwnerPlan) (fileCheckpoint, error) {
	checkpoint := fileCheckpoint{path: path, clientPlan: plan}
	directoryFD, err := openOwnedClientDirectory(plan, false, false)
	if errors.Is(err, unix.ENOENT) {
		return checkpoint, nil
	}
	if err != nil {
		return fileCheckpoint{}, err
	}
	defer unix.Close(directoryFD)
	fileFD, err := unix.Openat(directoryFD, plan.filename, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return checkpoint, nil
	}
	if err != nil {
		return fileCheckpoint{}, fmt.Errorf("打开客户端状态回滚点失败: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fileFD, &stat); err != nil {
		_ = unix.Close(fileFD)
		return fileCheckpoint{}, fmt.Errorf("检查客户端状态回滚点失败: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(plan.uid) || stat.Gid != uint32(plan.gid) {
		_ = unix.Close(fileFD)
		return fileCheckpoint{}, errors.New("客户端状态回滚点必须是 sudo 用户及其主组所有的普通文件")
	}
	if stat.Mode&0o077 != 0 {
		_ = unix.Close(fileFD)
		return fileCheckpoint{}, errors.New("客户端状态回滚点权限过宽")
	}
	if stat.Size > maxClientStateBytes {
		_ = unix.Close(fileFD)
		return fileCheckpoint{}, errors.New("客户端状态回滚点文件过大")
	}
	file := os.NewFile(uintptr(fileFD), plan.filename)
	if file == nil {
		_ = unix.Close(fileFD)
		return fileCheckpoint{}, errors.New("打开客户端状态回滚点失败")
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxClientStateBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return fileCheckpoint{}, err
	}
	if len(content) > maxClientStateBytes {
		return fileCheckpoint{}, errors.New("客户端状态回滚点文件过大")
	}
	checkpoint.exists = true
	checkpoint.data = content
	checkpoint.mode = os.FileMode(stat.Mode & 0o777)
	checkpoint.uid = int(stat.Uid)
	checkpoint.gid = int(stat.Gid)
	return checkpoint, nil
}

func restoreOwnedClientFile(checkpoint fileCheckpoint) error {
	if checkpoint.clientPlan == nil {
		return errors.New("客户端状态回滚点无效")
	}
	if checkpoint.exists {
		return writeOwnedClientFile(checkpoint.clientPlan, checkpoint.data, checkpoint.mode, checkpoint.uid, checkpoint.gid)
	}
	directoryFD, err := openOwnedClientDirectory(checkpoint.clientPlan, false, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)
	if err := unix.Unlinkat(directoryFD, checkpoint.clientPlan.filename, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("移除客户端状态失败: %w", err)
	}
	return unix.Fsync(directoryFD)
}
