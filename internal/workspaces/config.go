package workspaces

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const maximumConfigBytes = 4 * 1024 * 1024

func LoadConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{SchemaVersion: 1, Repositories: []RepositoryConfig{}, Choices: map[string]Choices{}}, nil
	}
	if err != nil {
		return Config{}, errors.New("Cannot open configuration file.")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumConfigBytes {
		return Config{}, errors.New("Configuration must be a regular file smaller than 4 MB.")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumConfigBytes+1))
	if err != nil || len(contents) > maximumConfigBytes {
		return Config{}, errors.New("Cannot read configuration file.")
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Config{}, errors.New("Configuration is not valid JSON for this version.")
	}
	if err := ValidateConfig(config); err != nil {
		return Config{}, err
	}
	if config.Repositories == nil {
		config.Repositories = []RepositoryConfig{}
	}
	if config.Choices == nil {
		config.Choices = map[string]Choices{}
	}
	return config, nil
}

func SaveConfig(path string, config Config) error {
	if err := ValidateConfig(config); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(config, "", "  ")
	if err != nil || len(contents) > maximumConfigBytes {
		return errors.New("Configuration is too large to save.")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("Cannot create configuration directory.")
	}
	file, err := os.CreateTemp(directory, ".config-*")
	if err != nil {
		return errors.New("Cannot create configuration file.")
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(contents, '\n')); err != nil {
		return errors.New("Cannot write configuration file.")
	}
	if err := file.Sync(); err != nil {
		return errors.New("Cannot flush configuration file.")
	}
	if err := file.Close(); err != nil {
		return errors.New("Cannot close configuration file.")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return errors.New("Cannot replace configuration file.")
	}
	parent, err := os.Open(directory)
	if err != nil {
		return errors.New("Cannot flush configuration directory.")
	}
	defer func() { _ = parent.Close() }()
	if err := parent.Sync(); err != nil {
		return errors.New("Cannot flush configuration directory.")
	}
	return nil
}
