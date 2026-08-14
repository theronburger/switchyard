package marketplacecontrol

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrEnvironmentUnknown = errors.New("Marketplace environment is not registered")
	ErrRegistryInvalid    = errors.New("Marketplace environment registry is invalid")
	registryIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
)

type EnvironmentRegistration struct {
	EnvironmentID    string
	WorktreeRoot     string
	NodeExecutable   string
	YarnCJS          string
	RunRoot          string
	DaemonInstanceID string
}

type EnvironmentRegistry struct {
	byEnvironment map[string]EnvironmentRegistration
}

func NewEnvironmentRegistry(registrations []EnvironmentRegistration) (EnvironmentRegistry, error) {
	byEnvironment := make(map[string]EnvironmentRegistration, len(registrations))
	byWorktree := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		if err := validateEnvironmentRegistration(registration); err != nil {
			return EnvironmentRegistry{}, ErrRegistryInvalid
		}
		if _, duplicate := byEnvironment[registration.EnvironmentID]; duplicate {
			return EnvironmentRegistry{}, ErrRegistryInvalid
		}
		if _, duplicate := byWorktree[registration.WorktreeRoot]; duplicate {
			return EnvironmentRegistry{}, ErrRegistryInvalid
		}
		byEnvironment[registration.EnvironmentID] = registration
		byWorktree[registration.WorktreeRoot] = struct{}{}
	}
	if len(byEnvironment) == 0 {
		return EnvironmentRegistry{}, ErrRegistryInvalid
	}
	return EnvironmentRegistry{byEnvironment: byEnvironment}, nil
}

func (registry EnvironmentRegistry) Lookup(environmentID string) (EnvironmentRegistration, error) {
	registration, found := registry.byEnvironment[environmentID]
	if !found {
		return EnvironmentRegistration{}, ErrEnvironmentUnknown
	}
	return registration, nil
}

func validateEnvironmentRegistration(registration EnvironmentRegistration) error {
	if !registryIDPattern.MatchString(registration.EnvironmentID) ||
		!registryIDPattern.MatchString(registration.DaemonInstanceID) {
		return ErrRegistryInvalid
	}
	paths := []string{
		registration.WorktreeRoot,
		registration.NodeExecutable,
		registration.YarnCJS,
		registration.RunRoot,
	}
	for _, path := range paths {
		if !cleanAbsolutePath(path) {
			return ErrRegistryInvalid
		}
	}
	if filepath.Dir(registration.WorktreeRoot) == registration.WorktreeRoot ||
		filepath.Dir(registration.RunRoot) == registration.RunRoot ||
		filepath.Ext(registration.YarnCJS) != ".cjs" ||
		!pathWithin(registration.WorktreeRoot, registration.YarnCJS) {
		return ErrRegistryInvalid
	}
	return nil
}

func cleanAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path &&
		!strings.ContainsRune(path, 0)
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
