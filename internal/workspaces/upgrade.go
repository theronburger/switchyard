package workspaces

import (
	"errors"
	"os"
	"path/filepath"
)

// UpgradeConfig installs a prepared private recipe, never translates runtime state.
func UpgradeConfig(root string, apply bool) error {
	destination := filepath.Join(root, "config.json")
	if _, err := os.Stat(destination); err == nil {
		_, err = LoadConfig(destination)
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	source := filepath.Join(root, "upgrade", "config.json")
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		if _, legacyErr := os.Stat(filepath.Join(root, "configuration.yaml")); legacyErr == nil {
			return errors.New("The upgrade needs a prepared private configuration in upgrade/config.json. Your previous configuration is unchanged.")
		}
		return nil
	} else if err != nil {
		return err
	}
	config, err := LoadConfig(source)
	if err != nil {
		return errors.New("The prepared upgrade configuration is invalid. Your previous configuration is unchanged.")
	}
	if !apply {
		return nil
	}
	return SaveConfig(destination, config)
}
