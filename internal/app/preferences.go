package app

import (
	"errors"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"mihomoctl/internal/i18n"
)

// Preferences is independent of Mihomo state, credentials and initialization.
// It uses the same ownership and atomic-write protections as client.yaml.
type Preferences struct {
	Path string
}

func DefaultPreferences() Preferences {
	return Preferences{Path: filepath.Join(filepath.Dir(clientConfigPath()), "preferences.yaml")}
}

func (p Preferences) LoadLanguage() (i18n.Language, error) {
	owner, err := planClientOwnership(p.Path)
	if err != nil {
		return i18n.Chinese, err
	}
	var content []byte
	if owner == nil {
		content, err = readLocalClientFile(p.Path)
	} else {
		content, err = readOwnedClientFile(owner)
	}
	if errors.Is(err, os.ErrNotExist) {
		return i18n.Chinese, nil
	}
	if err != nil {
		return i18n.Chinese, err
	}
	var preferences struct {
		Language string `yaml:"language"`
	}
	if err := yaml.Unmarshal(content, &preferences); err != nil {
		return i18n.Chinese, err
	}
	if preferences.Language == "" {
		return i18n.Chinese, nil
	}
	return i18n.Parse(preferences.Language)
}

func (p Preferences) SaveLanguage(language i18n.Language) error {
	if _, err := i18n.Parse(string(language)); err != nil {
		return err
	}
	owner, err := planClientOwnership(p.Path)
	if err != nil {
		return err
	}
	// Validate any existing file before replacing it. Preserve unknown fields
	// so future user preferences survive writes from this version.
	var existing []byte
	if owner == nil {
		existing, err = readLocalClientFile(p.Path)
	} else {
		existing, err = readOwnedClientFile(owner)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fields := make(map[string]any)
	if len(existing) != 0 {
		if err := yaml.Unmarshal(existing, &fields); err != nil {
			return err
		}
	}
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["language"] = string(language)
	content, err := yaml.Marshal(fields)
	if err != nil {
		return err
	}
	if owner != nil {
		return writeOwnedClientFile(owner, content, 0o600, owner.uid, owner.gid)
	}
	return atomicWrite(p.Path, content, 0o600)
}
