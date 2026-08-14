package processhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func LoadOwnership(path string) (Ownership, error) {
	fileInfo, err := os.Lstat(path)
	if err != nil {
		return Ownership{}, err
	}
	if !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm() != 0o600 {
		return Ownership{}, fmt.Errorf("%w: ownership file must be a mode-0600 regular file", ErrOwnershipInvalid)
	}

	file, err := os.Open(path)
	if err != nil {
		return Ownership{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return Ownership{}, err
	}
	if !os.SameFile(fileInfo, openedInfo) {
		return Ownership{}, fmt.Errorf("%w: ownership file changed while opening", ErrOwnershipInvalid)
	}

	payload, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil {
		return Ownership{}, err
	}
	if len(payload) > 1024*1024 {
		return Ownership{}, fmt.Errorf("%w: ownership file exceeds one MiB", ErrOwnershipInvalid)
	}
	var ownership Ownership
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&ownership); err != nil {
		return Ownership{}, fmt.Errorf("%w: %v", ErrOwnershipInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Ownership{}, fmt.Errorf("%w: multiple JSON values", ErrOwnershipInvalid)
		}
		return Ownership{}, fmt.Errorf("%w: %v", ErrOwnershipInvalid, err)
	}
	if err := validateOwnership(path, ownership); err != nil {
		return Ownership{}, err
	}
	return ownership, nil
}

func validateOwnership(path string, ownership Ownership) error {
	if ownership.SchemaVersion != OwnershipSchemaVersion {
		return fmt.Errorf("%w: schema version %d", ErrOwnershipInvalid, ownership.SchemaVersion)
	}
	if ownership.EnvironmentID == "" || ownership.ServiceID == "" || ownership.RunID == "" {
		return fmt.Errorf("%w: run identity is incomplete", ErrOwnershipInvalid)
	}
	if ownership.ProcessGroupID <= 1 || ownership.Leader.PID <= 1 || ownership.Leader.ProcessGroupID != ownership.ProcessGroupID {
		return fmt.Errorf("%w: process group identity is invalid", ErrOwnershipInvalid)
	}
	if ownership.Leader.StartedAt.IsZero() || ownership.Leader.CommandFingerprint == "" || ownership.LaunchFingerprint == "" {
		return fmt.Errorf("%w: leader identity is incomplete", ErrOwnershipInvalid)
	}
	if ownership.State == "" || ownership.StartedAt.IsZero() || ownership.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: lifecycle state is incomplete", ErrOwnershipInvalid)
	}
	runDirectory := filepath.Dir(path)
	if filepath.Clean(ownership.StdoutPath) != filepath.Join(runDirectory, StdoutLogFileName) ||
		filepath.Clean(ownership.StderrPath) != filepath.Join(runDirectory, StderrLogFileName) {
		return fmt.Errorf("%w: log paths leave the owned run directory", ErrOwnershipInvalid)
	}
	for _, member := range ownership.Members {
		if member.PID <= 1 || member.ProcessGroupID != ownership.ProcessGroupID || member.StartedAt.IsZero() || member.CommandFingerprint == "" {
			return fmt.Errorf("%w: member identity is incomplete", ErrOwnershipInvalid)
		}
	}
	return nil
}

func saveOwnership(path string, ownership Ownership) error {
	if err := validateOwnership(path, ownership); err != nil {
		return err
	}
	payload, err := json.Marshal(ownership)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".ownership.*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
