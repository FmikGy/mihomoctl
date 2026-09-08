package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// MaxManagedConfigBytes is the application-level read limit for an existing
// Mihomo configuration.
const MaxManagedConfigBytes int64 = 10 << 20

// EnsureRootManagedDirectory safely creates path, if necessary, and verifies
// that every non-sticky component is root-owned and not writable by group or
// others. The walk never follows symbolic links.
func EnsureRootManagedDirectory(path string) error {
	if err := validateCanonicalPath("root-managed directory", path); err != nil {
		return err
	}
	if path == string(filepath.Separator) {
		return errors.New("root-managed directory must not be the filesystem root")
	}
	return ensureRootManagedDirectory(path)
}

// ValidateRootManagedDirectory verifies an existing root-managed directory
// using the same ownership, permissions, and no-symlink policy as creation.
func ValidateRootManagedDirectory(path string) error {
	if err := validateCanonicalPath("root-managed directory", path); err != nil {
		return err
	}
	if path == string(filepath.Separator) {
		return errors.New("root-managed directory must not be the filesystem root")
	}
	return validateRootManagedDirectory(path)
}

// ReadManagedConfig reads an existing Mihomo configuration without following
// its final path component. When running as root, it also resolves the parent
// directory through a root-owned, non-writable directory chain and requires
// the configuration itself to be owned by root and not writable by group or
// others.
func ReadManagedConfig(path string, maxBytes int64) ([]byte, error) {
	if err := validateCanonicalPath("managed config", path); err != nil {
		return nil, err
	}
	if maxBytes <= 0 || maxBytes == int64(^uint64(0)>>1) {
		return nil, errors.New("managed config size limit must be positive and bounded")
	}

	requireRootOwner := os.Geteuid() == 0
	fd, err := openManagedConfig(path, requireRootOwner)
	if err != nil {
		return nil, err
	}
	return readManagedConfigFD(fd, path, maxBytes, requireRootOwner)
}

func openManagedConfig(path string, requireRootOwner bool) (int, error) {
	flags := unix.O_RDONLY | unix.O_NONBLOCK | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if !requireRootOwner {
		fd, err := unix.Open(path, flags, 0)
		if errors.Is(err, unix.ELOOP) {
			return -1, fmt.Errorf("managed config must not be a symbolic link: %w", err)
		}
		if err != nil {
			return -1, fmt.Errorf("open managed config: %w", err)
		}
		return fd, nil
	}

	parentFD, err := openRootManagedDirectory(filepath.Dir(path), false)
	if err != nil {
		return -1, fmt.Errorf("open trusted managed config directory: %w", err)
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, filepath.Base(path), flags, 0)
	if errors.Is(err, unix.ELOOP) {
		return -1, fmt.Errorf("managed config must not be a symbolic link: %w", err)
	}
	if err != nil {
		return -1, fmt.Errorf("open managed config: %w", err)
	}
	return fd, nil
}

func readManagedConfigFD(fd int, path string, maxBytes int64, requireRootOwner bool) ([]byte, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("inspect managed config: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, errors.New("managed config must be a regular file")
	}
	if requireRootOwner {
		if err := validateRootManagedFile(stat.Uid, stat.Mode); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
	}
	if stat.Size > maxBytes {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("managed config exceeds the %d-byte size limit", maxBytes)
	}

	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open managed config file descriptor")
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read managed config: %w", err)
	}
	if int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("managed config exceeds the %d-byte size limit", maxBytes)
	}
	return content, nil
}

func validateRootManagedFile(uid uint32, mode uint32) error {
	if uid != 0 {
		return errors.New("managed config must be owned by root")
	}
	if mode&0o022 != 0 {
		return errors.New("managed config must not be writable by group or others")
	}
	return nil
}
