package platform

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const DefaultBackupRetention = 20

type ApplyPaths struct {
	MihomoBinary string
	ConfigPath   string
	ConfigDir    string
	BackupDir    string
}

type ApplyResult struct {
	BackupPath string

	configPath    string
	hadOriginal   bool
	metadata      fileMetadata
	installedHash [sha256.Size]byte
	installedSize int64
	recoverable   bool
}

type fileMetadata struct {
	mode os.FileMode
	uid  int
	gid  int
}

// ConfigApplier validates a staged config, atomically installs it and restores
// the prior config if activation or the post-activation health check fails.
// Every filesystem path and external action is supplied by the caller.
type ConfigApplier struct {
	Runner          CommandRunner
	Paths           ApplyPaths
	Activate        func(context.Context) error
	HealthCheck     func(context.Context) error
	Now             func() time.Time
	RollbackTimeout time.Duration
	// BackupRetention limits backups created for the active config. Zero uses
	// DefaultBackupRetention; a negative value disables pruning.
	BackupRetention int
}

type ApplyError struct {
	Stage       string
	Cause       error
	RollbackErr error
}

func (e *ApplyError) Error() string {
	if e.RollbackErr != nil {
		return fmt.Sprintf("config apply failed during %s; rollback also failed", e.Stage)
	}
	return fmt.Sprintf("config apply failed during %s", e.Stage)
}

func (e *ApplyError) Unwrap() error { return e.Cause }

func (a *ConfigApplier) Apply(ctx context.Context, config []byte) (ApplyResult, error) {
	if err := a.validate(); err != nil {
		return ApplyResult{}, err
	}
	if len(config) == 0 {
		return ApplyResult{}, fmt.Errorf("config must not be empty")
	}
	if int64(len(config)) > MaxManagedConfigBytes {
		return ApplyResult{}, fmt.Errorf("config exceeds the %d-byte size limit", MaxManagedConfigBytes)
	}

	configDir := filepath.Dir(a.Paths.ConfigPath)
	if os.Geteuid() == 0 {
		if err := validateRootManagedExecutable(a.Paths.MihomoBinary); err != nil {
			return ApplyResult{}, fmt.Errorf("unsafe mihomo binary: %w", err)
		}
		if err := ensureRootManagedDirectory(configDir); err != nil {
			return ApplyResult{}, fmt.Errorf("unsafe config directory: %w", err)
		}
		if a.Paths.ConfigDir != "" && filepath.Clean(a.Paths.ConfigDir) != filepath.Clean(configDir) {
			if err := validateRootManagedDirectory(a.Paths.ConfigDir); err != nil {
				return ApplyResult{}, fmt.Errorf("unsafe mihomo data directory: %w", err)
			}
		}
	} else if err := os.MkdirAll(configDir, 0o700); err != nil {
		return ApplyResult{}, fmt.Errorf("create config directory: %w", err)
	}
	stageFile, err := writeTempFile(configDir, ".mihomoctl-staging-*", config)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("stage config: %w", err)
	}
	stage := stageFile.Name()
	defer func() {
		if stageFile != nil {
			_ = stageFile.Close()
		}
		_ = os.Remove(stage)
	}()

	validationArgs := []string{"-t"}
	if a.Paths.ConfigDir != "" {
		validationArgs = append(validationArgs, "-d", a.Paths.ConfigDir)
	}
	validationArgs = append(validationArgs, "-f", stage)
	if _, err := a.Runner.Run(ctx, a.Paths.MihomoBinary, validationArgs...); err != nil {
		return ApplyResult{}, &ApplyError{Stage: "validation", Cause: err}
	}

	backupPath, hadOriginal, metadata, err := a.backupCurrent()
	if err != nil {
		stage := "backup"
		var retentionErr *backupRetentionError
		if errors.As(err, &retentionErr) {
			stage = "backup retention"
		}
		return ApplyResult{}, &ApplyError{Stage: stage, Cause: err}
	}
	result := ApplyResult{
		BackupPath: backupPath, configPath: a.Paths.ConfigPath,
		hadOriginal: hadOriginal, metadata: metadata, installedHash: sha256.Sum256(config),
		installedSize: int64(len(config)),
	}
	secureMetadata := fileMetadata{mode: 0o600, uid: os.Geteuid(), gid: os.Getegid()}
	if hadOriginal {
		secureMetadata = managedConfigMetadata(metadata)
	}
	// Commit ownership and permissions while the old config is still in place.
	// A crash after rename can then never strand a root-only staging file where
	// a less-privileged Mihomo service expects its readable config.
	if err := applyFileMetadata(stageFile, secureMetadata); err != nil {
		return result, &ApplyError{Stage: "permissions", Cause: err}
	}
	if err := stageFile.Sync(); err != nil {
		return result, &ApplyError{Stage: "permissions", Cause: err}
	}
	openedInfo, err := stageFile.Stat()
	if err != nil {
		return result, &ApplyError{Stage: "permissions", Cause: err}
	}
	pathInfo, err := os.Lstat(stage)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) {
		if err == nil {
			err = errors.New("staging path changed before replace")
		}
		return result, &ApplyError{Stage: "permissions", Cause: err}
	}
	if err := stageFile.Close(); err != nil {
		return result, &ApplyError{Stage: "permissions", Cause: err}
	}
	stageFile = nil
	if err := os.Rename(stage, a.Paths.ConfigPath); err != nil {
		return result, &ApplyError{Stage: "replace", Cause: err}
	}
	result.recoverable = true
	if err := syncDirectory(configDir); err != nil {
		rollbackErr := a.emergencyRollback(ctx, backupPath, hadOriginal, metadata)
		return result, &ApplyError{Stage: "replace", Cause: err, RollbackErr: rollbackErr}
	}

	if err := a.Activate(ctx); err != nil {
		rollbackErr := a.emergencyRollback(ctx, backupPath, hadOriginal, metadata)
		return result, &ApplyError{Stage: "activation", Cause: err, RollbackErr: rollbackErr}
	}
	if err := a.HealthCheck(ctx); err != nil {
		rollbackErr := a.emergencyRollback(ctx, backupPath, hadOriginal, metadata)
		return result, &ApplyError{Stage: "health check", Cause: err, RollbackErr: rollbackErr}
	}
	return result, nil
}

// Rollback restores the config represented by a successful Apply result and
// reactivates it. Rollback uses a fresh bounded context even when ctx has
// already been canceled.
func (a *ConfigApplier) Rollback(ctx context.Context, result ApplyResult) error {
	if err := a.validate(); err != nil {
		return err
	}
	if !result.recoverable || result.configPath != a.Paths.ConfigPath {
		return errors.New("apply result cannot be rolled back by this applier")
	}
	content, err := ReadManagedConfig(result.configPath, result.installedSize)
	if err != nil {
		return fmt.Errorf("refusing to roll back a config that changed after apply: %w", err)
	}
	if sha256.Sum256(content) != result.installedHash {
		return errors.New("refusing to roll back a config that changed after apply")
	}
	return a.rollback(ctx, result.BackupPath, result.hadOriginal, result.metadata)
}

func (a *ConfigApplier) validate() error {
	if a == nil || a.Runner == nil {
		return fmt.Errorf("config command runner is required")
	}
	if err := validateCanonicalPath("mihomo binary", a.Paths.MihomoBinary); err != nil {
		return err
	}
	if err := validateCanonicalPath("config", a.Paths.ConfigPath); err != nil {
		return err
	}
	if a.Paths.ConfigDir != "" {
		if err := validateCanonicalPath("config directory", a.Paths.ConfigDir); err != nil {
			return err
		}
	}
	if err := validateCanonicalPath("backup directory", a.Paths.BackupDir); err != nil {
		return err
	}
	if a.Activate == nil {
		return fmt.Errorf("config activation callback is required")
	}
	if a.HealthCheck == nil {
		return fmt.Errorf("config health check callback is required")
	}
	return nil
}

func validateCanonicalPath(name, path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%s path must be absolute", name)
	}
	if strings.ContainsRune(path, '\x00') || filepath.Clean(path) != path {
		return fmt.Errorf("%s path must be canonical", name)
	}
	return nil
}

func (a *ConfigApplier) backupCurrent() (string, bool, fileMetadata, error) {
	input, info, err := openRegularFileNoFollow(a.Paths.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, fileMetadata{}, nil
	}
	if err != nil {
		return "", false, fileMetadata{}, err
	}
	defer input.Close()
	metadata, err := metadataFromInfo(info)
	if err != nil {
		return "", false, fileMetadata{}, err
	}
	if os.Geteuid() == 0 {
		if err := validateRootManagedFile(uint32(metadata.uid), uint32(metadata.mode.Perm())); err != nil {
			return "", false, fileMetadata{}, fmt.Errorf("unsafe active config: %w", err)
		}
	}
	if err := secureBackupDirectory(a.Paths.BackupDir); err != nil {
		return "", false, fileMetadata{}, err
	}

	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	baseName := fmt.Sprintf("%s.%s", filepath.Base(a.Paths.ConfigPath), now.UTC().Format("20060102T150405.000000000Z"))
	backupPath, err := createBackupFromReaderExclusive(input, a.Paths.BackupDir, baseName)
	if err != nil {
		return "", false, fileMetadata{}, err
	}
	retention := a.BackupRetention
	if retention == 0 {
		retention = DefaultBackupRetention
	}
	if retention > 0 {
		if err := a.pruneBackups(retention, backupPath); err != nil {
			return "", false, fileMetadata{}, &backupRetentionError{err: err}
		}
	}
	return backupPath, true, metadata, nil
}

type backupRetentionError struct{ err error }

func (e *backupRetentionError) Error() string { return e.err.Error() }
func (e *backupRetentionError) Unwrap() error { return e.err }

func ensureRootManagedDirectory(path string) error {
	fd, err := openRootManagedDirectory(path, true)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

func validateRootManagedDirectory(path string) error {
	fd, err := openRootManagedDirectory(path, false)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// openRootManagedDirectory walks from a trusted root using directory FDs. It
// rejects symlinks and writable path components; a sticky root-owned directory
// such as /tmp is accepted only as an ancestor, never as the destination.
func openRootManagedDirectory(path string, create bool) (int, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return -1, errors.New("path must be absolute")
	}
	current, err := unix.Open(string(os.PathSeparator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	components := strings.Split(strings.TrimPrefix(clean, string(os.PathSeparator)), string(os.PathSeparator))
	if clean == string(os.PathSeparator) {
		components = nil
	}
	for index, component := range components {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			mkdirErr := unix.Mkdirat(current, component, 0o700)
			if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(current)
				return -1, fmt.Errorf("create %s: %w", filepath.Join(string(os.PathSeparator), filepath.Join(components[:index+1]...)), mkdirErr)
			}
			if mkdirErr == nil {
				if syncErr := unix.Fsync(current); syncErr != nil {
					_ = unix.Close(current)
					return -1, fmt.Errorf("sync parent of path component %q: %w", component, syncErr)
				}
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			_ = unix.Close(current)
			return -1, fmt.Errorf("open trusted path component %q: %w", component, openErr)
		}
		allowSticky := index < len(components)-1
		if err := validateRootDirectoryFD(next, component, allowSticky); err != nil {
			_ = unix.Close(next)
			_ = unix.Close(current)
			return -1, err
		}
		_ = unix.Close(current)
		current = next
	}
	return current, nil
}

func validateRootDirectoryFD(fd int, name string, allowSticky bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 {
		return fmt.Errorf("path component %q must be a root-owned directory", name)
	}
	if stat.Mode&0o022 != 0 && !(allowSticky && stat.Mode&unix.S_ISVTX != 0) {
		return fmt.Errorf("path component %q must not be writable by group or others", name)
	}
	return nil
}

func validateRootManagedExecutable(path string) error {
	parentFD, err := openRootManagedDirectory(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, filepath.Base(path), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != 0 {
		return errors.New("executable must be a root-owned regular file")
	}
	if stat.Mode&0o022 != 0 || stat.Mode&0o111 == 0 {
		return errors.New("executable must not be writable by group or others and must be executable")
	}
	return nil
}

func openRegularFileNoFollow(path string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, errors.New("open file descriptor")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, errors.New("path is not a regular file")
	}
	return file, info, nil
}

func secureBackupDirectory(path string) error {
	if filepath.Clean(path) == string(os.PathSeparator) {
		return errors.New("backup path must not be the filesystem root")
	}
	if os.Geteuid() == 0 {
		fd, err := openRootManagedDirectory(path, true)
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		return unix.Fchmod(fd, 0o700)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("backup path must be a directory owned by the current user")
	}
	return os.Chmod(path, 0o700)
}

func createBackupExclusive(sourcePath, backupDir, baseName string) (string, error) {
	input, _, err := openRegularFileNoFollow(sourcePath)
	if err != nil {
		return "", err
	}
	defer input.Close()
	return createBackupFromReaderExclusive(input, backupDir, baseName)
}

func createBackupFromReaderExclusive(input io.Reader, backupDir, baseName string) (string, error) {
	for sequence := 0; ; sequence++ {
		name := baseName + ".bak"
		if sequence > 0 {
			name = fmt.Sprintf("%s.%d.bak", baseName, sequence)
		}
		path := filepath.Join(backupDir, name)
		output, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(openErr, os.ErrExist) {
			continue
		}
		if openErr != nil {
			return "", openErr
		}
		copyErr := func() error {
			if err := output.Chmod(0o600); err != nil {
				return err
			}
			if _, err := io.Copy(output, input); err != nil {
				return err
			}
			return output.Sync()
		}()
		closeErr := output.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			_ = os.Remove(path)
			return "", err
		}
		if err := syncDirectory(backupDir); err != nil {
			return "", err
		}
		return path, nil
	}
}

type backupFile struct {
	path      string
	name      string
	timestamp time.Time
	sequence  int
}

func (a *ConfigApplier) pruneBackups(keep int, protectedPath string) error {
	entries, err := os.ReadDir(a.Paths.BackupDir)
	if err != nil {
		return err
	}
	base := filepath.Base(a.Paths.ConfigPath)
	backups := make([]backupFile, 0, len(entries))
	for _, entry := range entries {
		timestamp, sequence, managed := parseManagedBackupName(base, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 || !managed {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		backups = append(backups, backupFile{
			path: filepath.Join(a.Paths.BackupDir, entry.Name()), name: entry.Name(),
			timestamp: timestamp, sequence: sequence,
		})
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].timestamp.Equal(backups[j].timestamp) {
			if backups[i].sequence == backups[j].sequence {
				return backups[i].name < backups[j].name
			}
			return backups[i].sequence < backups[j].sequence
		}
		return backups[i].timestamp.Before(backups[j].timestamp)
	})
	removeCount := max(0, len(backups)-keep)
	removed := 0
	for _, backup := range backups {
		if removed >= removeCount {
			break
		}
		if backup.path == protectedPath {
			continue
		}
		if err := os.Remove(backup.path); err != nil {
			return err
		}
		removed++
	}
	if removed > 0 {
		return syncDirectory(a.Paths.BackupDir)
	}
	return nil
}

func managedBackupName(base, name string) bool {
	_, _, ok := parseManagedBackupName(base, name)
	return ok
}

func parseManagedBackupName(base, name string) (time.Time, int, bool) {
	prefix := base + "."
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".bak") {
		return time.Time{}, 0, false
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".bak")
	parts := strings.Split(stem, ".")
	if len(parts) != 2 && len(parts) != 3 {
		return time.Time{}, 0, false
	}
	timestampText := parts[0] + "." + parts[1]
	timestamp, err := time.Parse("20060102T150405.000000000Z", timestampText)
	if err != nil || timestamp.UTC().Format("20060102T150405.000000000Z") != timestampText {
		return time.Time{}, 0, false
	}
	if len(parts) == 3 {
		sequence, err := strconv.Atoi(parts[2])
		if err != nil || sequence <= 0 || strconv.Itoa(sequence) != parts[2] {
			return time.Time{}, 0, false
		}
		return timestamp, sequence, true
	}
	return timestamp, 0, true
}

func (a *ConfigApplier) rollback(ctx context.Context, backupPath string, hadOriginal bool, metadata fileMetadata) error {
	return a.rollbackWithBudget(ctx, backupPath, hadOriginal, metadata, true)
}

func (a *ConfigApplier) emergencyRollback(ctx context.Context, backupPath string, hadOriginal bool, metadata fileMetadata) error {
	return a.rollbackWithBudget(ctx, backupPath, hadOriginal, metadata, false)
}

func (a *ConfigApplier) rollbackWithBudget(ctx context.Context, backupPath string, hadOriginal bool, metadata fileMetadata, preserveDeadline bool) error {
	rollbackTimeout := a.RollbackTimeout
	if rollbackTimeout <= 0 {
		rollbackTimeout = 30 * time.Second
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Automatic recovery from a failed Apply needs a fresh window even when the
	// operation exhausted its deadline. Explicit Rollback calls can be nested
	// in a larger transaction and therefore retain that transaction's deadline.
	rollbackBase := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); preserveDeadline && ok {
		var deadlineCancel context.CancelFunc
		rollbackBase, deadlineCancel = context.WithDeadline(rollbackBase, deadline)
		defer deadlineCancel()
	}
	rollbackCtx, cancel := context.WithTimeout(rollbackBase, rollbackTimeout)
	defer cancel()

	var restoreErr error
	if hadOriginal {
		backup, _, err := openRegularFileNoFollow(backupPath)
		if err != nil {
			restoreErr = err
		} else {
			restoreErr = atomicCopy(a.Paths.ConfigPath, backup, &metadata)
			restoreErr = errors.Join(restoreErr, backup.Close())
		}
	} else {
		if err := os.Remove(a.Paths.ConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			restoreErr = err
		}
		restoreErr = errors.Join(restoreErr, syncDirectory(filepath.Dir(a.Paths.ConfigPath)))
	}
	if restoreErr != nil {
		return restoreErr
	}
	if err := a.Activate(rollbackCtx); err != nil {
		return fmt.Errorf("activate restored config: %w", err)
	}
	if err := a.HealthCheck(rollbackCtx); err != nil {
		return fmt.Errorf("restored config health check: %w", err)
	}
	return nil
}

func metadataFromInfo(info os.FileInfo) (fileMetadata, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileMetadata{}, errors.New("config file ownership is unavailable")
	}
	return fileMetadata{mode: info.Mode().Perm(), uid: int(stat.Uid), gid: int(stat.Gid)}, nil
}

func applyFileMetadata(file *os.File, metadata fileMetadata) error {
	if err := file.Chown(metadata.uid, metadata.gid); err != nil {
		return err
	}
	return file.Chmod(metadata.mode.Perm())
}

func managedConfigMetadata(metadata fileMetadata) fileMetadata {
	metadata.mode = 0o400 | metadata.mode.Perm()&0o044
	if metadata.uid == os.Geteuid() {
		metadata.mode |= 0o200
	}
	return metadata
}

func writeTempFile(dir, pattern string, data []byte) (*os.File, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	path := file.Name()
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return nil, err
	}
	if _, err := file.Write(data); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	keep = true
	return file, nil
}

func atomicCopy(destination string, source io.Reader, metadata *fileMetadata) error {
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".mihomoctl-copy-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := io.Copy(temp, source); err != nil {
		temp.Close()
		return err
	}
	if metadata != nil {
		if err := applyFileMetadata(temp, managedConfigMetadata(*metadata)); err != nil {
			temp.Close()
			return err
		}
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
