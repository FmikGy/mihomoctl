package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type ApplyPaths struct {
	MihomoBinary string
	ConfigPath   string
	ConfigDir    string
	BackupDir    string
}

type ApplyResult struct {
	BackupPath string
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

	configDir := filepath.Dir(a.Paths.ConfigPath)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return ApplyResult{}, fmt.Errorf("create config directory: %w", err)
	}
	stage, err := writeTempFile(configDir, ".mihomoctl-staging-*", config)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("stage config: %w", err)
	}
	defer os.Remove(stage)

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
		return ApplyResult{}, &ApplyError{Stage: "backup", Cause: err}
	}
	if err := os.Rename(stage, a.Paths.ConfigPath); err != nil {
		return ApplyResult{BackupPath: backupPath}, &ApplyError{Stage: "replace", Cause: err}
	}
	if err := syncDirectory(configDir); err != nil {
		rollbackErr := a.rollback(ctx, backupPath, hadOriginal, metadata)
		return ApplyResult{BackupPath: backupPath}, &ApplyError{Stage: "replace", Cause: err, RollbackErr: rollbackErr}
	}
	secureMetadata := fileMetadata{mode: 0o600, uid: os.Geteuid(), gid: os.Getegid()}
	if hadOriginal {
		secureMetadata.uid = metadata.uid
		secureMetadata.gid = metadata.gid
		secureMetadata.mode |= metadata.mode.Perm() & 0o040
	}
	if err := applyFileMetadata(a.Paths.ConfigPath, secureMetadata); err != nil {
		rollbackErr := a.rollback(ctx, backupPath, hadOriginal, metadata)
		return ApplyResult{BackupPath: backupPath}, &ApplyError{Stage: "permissions", Cause: err, RollbackErr: rollbackErr}
	}

	if err := a.Activate(ctx); err != nil {
		rollbackErr := a.rollback(ctx, backupPath, hadOriginal, metadata)
		return ApplyResult{BackupPath: backupPath}, &ApplyError{Stage: "activation", Cause: err, RollbackErr: rollbackErr}
	}
	if err := a.HealthCheck(ctx); err != nil {
		rollbackErr := a.rollback(ctx, backupPath, hadOriginal, metadata)
		return ApplyResult{BackupPath: backupPath}, &ApplyError{Stage: "health check", Cause: err, RollbackErr: rollbackErr}
	}
	return ApplyResult{BackupPath: backupPath}, nil
}

func (a *ConfigApplier) validate() error {
	if a == nil || a.Runner == nil {
		return fmt.Errorf("config command runner is required")
	}
	if a.Paths.MihomoBinary == "" || !filepath.IsAbs(a.Paths.MihomoBinary) {
		return fmt.Errorf("mihomo binary path must be absolute")
	}
	if a.Paths.ConfigPath == "" || !filepath.IsAbs(a.Paths.ConfigPath) {
		return fmt.Errorf("config path must be absolute")
	}
	if a.Paths.ConfigDir != "" && !filepath.IsAbs(a.Paths.ConfigDir) {
		return fmt.Errorf("config directory must be absolute")
	}
	if a.Paths.BackupDir == "" || !filepath.IsAbs(a.Paths.BackupDir) {
		return fmt.Errorf("backup directory must be absolute")
	}
	if a.Activate == nil {
		return fmt.Errorf("config activation callback is required")
	}
	if a.HealthCheck == nil {
		return fmt.Errorf("config health check callback is required")
	}
	return nil
}

func (a *ConfigApplier) backupCurrent() (string, bool, fileMetadata, error) {
	info, err := os.Lstat(a.Paths.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, fileMetadata{}, nil
	}
	if err != nil {
		return "", false, fileMetadata{}, err
	}
	if !info.Mode().IsRegular() {
		return "", false, fileMetadata{}, fmt.Errorf("config path is not a regular file")
	}
	metadata, err := metadataFromInfo(info)
	if err != nil {
		return "", false, fileMetadata{}, err
	}
	if err := os.MkdirAll(a.Paths.BackupDir, 0o700); err != nil {
		return "", false, fileMetadata{}, err
	}
	if err := os.Chmod(a.Paths.BackupDir, 0o700); err != nil {
		return "", false, fileMetadata{}, err
	}

	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	baseName := fmt.Sprintf("%s.%s", filepath.Base(a.Paths.ConfigPath), now.UTC().Format("20060102T150405.000000000Z"))
	backupPath := filepath.Join(a.Paths.BackupDir, baseName+".bak")
	for sequence := 1; ; sequence++ {
		_, err := os.Lstat(backupPath)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", false, fileMetadata{}, err
		}
		backupPath = filepath.Join(a.Paths.BackupDir, fmt.Sprintf("%s.%d.bak", baseName, sequence))
	}
	input, err := os.Open(a.Paths.ConfigPath)
	if err != nil {
		return "", false, fileMetadata{}, err
	}
	defer input.Close()
	if err := atomicCopy(backupPath, input); err != nil {
		return "", false, fileMetadata{}, err
	}
	return backupPath, true, metadata, nil
}

func (a *ConfigApplier) rollback(ctx context.Context, backupPath string, hadOriginal bool, metadata fileMetadata) error {
	rollbackTimeout := a.RollbackTimeout
	if rollbackTimeout <= 0 {
		rollbackTimeout = 30 * time.Second
	}
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()

	var restoreErr error
	if hadOriginal {
		backup, err := os.Open(backupPath)
		if err != nil {
			restoreErr = err
		} else {
			restoreErr = atomicCopy(a.Paths.ConfigPath, backup)
			restoreErr = errors.Join(restoreErr, backup.Close())
			if restoreErr == nil {
				restoreErr = applyFileMetadata(a.Paths.ConfigPath, metadata)
			}
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

func applyFileMetadata(path string, metadata fileMetadata) error {
	if err := os.Chown(path, metadata.uid, metadata.gid); err != nil {
		return err
	}
	return os.Chmod(path, metadata.mode.Perm())
}

func writeTempFile(dir, pattern string, data []byte) (string, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	keep := false
	defer func() {
		file.Close()
		if !keep {
			os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	keep = true
	return path, nil
}

func atomicCopy(destination string, source io.Reader) error {
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
