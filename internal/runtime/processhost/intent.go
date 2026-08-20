package processhost

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func LoadLaunchIntent(path string) (LaunchIntent, error) {
	fileInfo, err := os.Lstat(path)
	if err != nil {
		return LaunchIntent{}, err
	}
	if !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm() != 0o600 {
		return LaunchIntent{}, fmt.Errorf("%w: intent file must be a mode-0600 regular file", ErrLaunchIntentInvalid)
	}

	file, err := os.Open(path)
	if err != nil {
		return LaunchIntent{}, err
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return LaunchIntent{}, err
	}
	if !os.SameFile(fileInfo, openedInfo) {
		return LaunchIntent{}, fmt.Errorf("%w: intent file changed while opening", ErrLaunchIntentInvalid)
	}

	payload, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil {
		return LaunchIntent{}, err
	}
	if len(payload) > 1024*1024 {
		return LaunchIntent{}, fmt.Errorf("%w: intent file exceeds one MiB", ErrLaunchIntentInvalid)
	}
	var intent LaunchIntent
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&intent); err != nil {
		return LaunchIntent{}, fmt.Errorf("%w: %v", ErrLaunchIntentInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return LaunchIntent{}, fmt.Errorf("%w: multiple JSON values", ErrLaunchIntentInvalid)
		}
		return LaunchIntent{}, fmt.Errorf("%w: %v", ErrLaunchIntentInvalid, err)
	}
	if err := validateLaunchIntent(path, intent); err != nil {
		return LaunchIntent{}, err
	}
	return intent, nil
}

func validateLaunchIntent(path string, intent LaunchIntent) error {
	if intent.SchemaVersion != LaunchIntentSchemaVersion {
		return fmt.Errorf("%w: schema version %d", ErrLaunchIntentInvalid, intent.SchemaVersion)
	}
	if intent.EnvironmentID == "" || intent.ServiceID == "" || intent.RunID == "" {
		return fmt.Errorf("%w: run identity is incomplete", ErrLaunchIntentInvalid)
	}
	if !filepath.IsAbs(intent.Executable) || !filepath.IsAbs(intent.RunDirectory) ||
		filepath.Clean(intent.RunDirectory) != filepath.Dir(filepath.Clean(path)) {
		return fmt.Errorf("%w: executable or run directory is invalid", ErrLaunchIntentInvalid)
	}
	if !validFingerprint(intent.LaunchFingerprint) {
		return fmt.Errorf("%w: launch fingerprint is invalid", ErrLaunchIntentInvalid)
	}
	if intent.CreatedAt.IsZero() || intent.UpdatedAt.IsZero() || intent.UpdatedAt.Before(intent.CreatedAt) {
		return fmt.Errorf("%w: lifecycle timestamps are invalid", ErrLaunchIntentInvalid)
	}
	if intent.CandidateLeader != nil {
		candidate := *intent.CandidateLeader
		if candidate.PID <= 1 || candidate.ProcessGroupID != candidate.PID || unknownStartTime(candidate.StartedAt) ||
			!validFingerprint(candidate.CommandFingerprint) {
			return fmt.Errorf("%w: candidate leader identity is invalid", ErrLaunchIntentInvalid)
		}
	}
	return nil
}

func validFingerprint(fingerprint string) bool {
	if len(fingerprint) != 64 {
		return false
	}
	_, err := hex.DecodeString(fingerprint)
	return err == nil
}

func saveLaunchIntent(path string, intent LaunchIntent) error {
	if err := validateLaunchIntent(path, intent); err != nil {
		return err
	}
	return saveAtomicJSON(path, ".launch-intent.*", intent)
}

func clearLaunchIntent(path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
