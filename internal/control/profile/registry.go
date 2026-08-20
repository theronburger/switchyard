package profile

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/theronburger/switchyard/internal/configuration"
)

var (
	ErrProfileInvalid = errors.New("repository profile is invalid")
	profileIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
)

type Registration struct {
	EnvironmentID      string
	RepositoryID       string
	WorktreeID         string
	ProfileKey         string
	ProfileDigest      string
	RepositoryRoot     string
	WorktreeRoot       string
	RuntimeRoot        string
	CacheRoot          string
	HomeDirectory      string
	TemporaryDirectory string
	ExecutablePath     string
	DaemonInstanceID   string
	Values             map[string]string
	Profile            configuration.Repository
}

type Registry struct {
	byEnvironment map[string]Registration
}

func NewRegistry(registrations []Registration) (Registry, error) {
	if len(registrations) == 0 {
		return Registry{}, ErrProfileInvalid
	}
	byEnvironment := make(map[string]Registration, len(registrations))
	for _, registration := range registrations {
		if !validRegistration(registration) {
			return Registry{}, ErrProfileInvalid
		}
		if _, duplicate := byEnvironment[registration.EnvironmentID]; duplicate {
			return Registry{}, ErrProfileInvalid
		}
		byEnvironment[registration.EnvironmentID] = registration
	}
	return Registry{byEnvironment: byEnvironment}, nil
}

func (registry Registry) Lookup(environmentID string) (Registration, error) {
	registration, found := registry.byEnvironment[environmentID]
	if !found {
		return Registration{}, ErrProfileInvalid
	}
	return registration, nil
}

func validRegistration(registration Registration) bool {
	if !profileIDPattern.MatchString(registration.EnvironmentID) || !profileIDPattern.MatchString(registration.RepositoryID) ||
		!profileIDPattern.MatchString(registration.WorktreeID) || !profileIDPattern.MatchString(registration.ProfileKey) ||
		!profileIDPattern.MatchString(registration.DaemonInstanceID) || !strings.HasPrefix(registration.ProfileDigest, "sha256:") {
		return false
	}
	for _, path := range []string{
		registration.RepositoryRoot, registration.WorktreeRoot, registration.RuntimeRoot, registration.CacheRoot,
		registration.HomeDirectory, registration.TemporaryDirectory,
	} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
			return false
		}
	}
	if registration.ExecutablePath == "" || strings.ContainsRune(registration.ExecutablePath, 0) {
		return false
	}
	for _, path := range filepath.SplitList(registration.ExecutablePath) {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return false
		}
	}
	return true
}
