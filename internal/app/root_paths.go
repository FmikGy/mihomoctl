package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"mihomoctl/internal/i18n"
	"mihomoctl/internal/platform"
)

type rootApplicationDirectory struct {
	name string
	path string
}

func rootApplicationDirectories(paths Paths) ([]rootApplicationDirectory, error) {
	candidates := []struct {
		name       string
		path       string
		isFilePath bool
	}{
		{name: "配置状态目录", path: paths.ConfigFile, isFilePath: true},
		{name: "数据目录", path: paths.DataDir},
		{name: "配置档案目录", path: paths.ProfileRoot},
		{name: "备份目录", path: paths.BackupDir},
		{name: "操作锁目录", path: paths.OperationLock, isFilePath: true},
		{name: "公开状态目录", path: paths.PublicFile, isFilePath: true},
	}

	directories := make([]rootApplicationDirectory, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.path == "" || !filepath.IsAbs(candidate.path) {
			return nil, i18n.Errorf("%s路径必须是绝对路径", i18n.M(candidate.name))
		}
		if strings.ContainsRune(candidate.path, '\x00') || filepath.Clean(candidate.path) != candidate.path {
			return nil, i18n.Errorf("%s路径必须是规范路径", i18n.M(candidate.name))
		}
		directory := candidate.path
		if candidate.isFilePath {
			directory = filepath.Dir(candidate.path)
		}
		if _, exists := seen[directory]; exists {
			continue
		}
		seen[directory] = struct{}{}
		directories = append(directories, rootApplicationDirectory{name: candidate.name, path: directory})
	}
	return directories, nil
}

func ensureRootApplicationDirectories(paths Paths) error {
	directories, err := rootApplicationDirectories(paths)
	if err != nil {
		return err
	}
	for _, directory := range directories {
		if err := platform.EnsureRootManagedDirectory(directory.path); err != nil {
			return i18n.Errorf("%s %s 不安全: %w", i18n.M(directory.name), directory.path, err)
		}
	}
	return nil
}

func validateExistingRootApplicationDirectories(paths Paths) error {
	directories, err := rootApplicationDirectories(paths)
	if err != nil {
		return err
	}
	for _, directory := range directories {
		if err := platform.ValidateRootManagedDirectory(directory.path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return i18n.Errorf("%s %s 不安全: %w", i18n.M(directory.name), directory.path, err)
		}
	}
	return nil
}
