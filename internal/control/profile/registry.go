package profile

import (
	"errors"
	"path/filepath"
	"regexp"
	"sort"
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
	HostHomeDirectory  string
	TemporaryDirectory string
	ExecutablePath     string
	DaemonInstanceID   string
	Values             map[string]string
	Profile            configuration.Repository
}

// Registry resolves the profile registration for an environment. Every
// environment has exactly one current registration compiled from the accepted
// configuration head. It may also carry pinned registrations compiled from
// older accepted revisions that a durable run still references, so a daemon
// restart after a later acceptance recovers the exact payload the run started
// from instead of silently re-reading the head.
type Registry struct {
	byEnvironment map[string]Registration
	pinned        map[pinnedRegistrationKey]Registration
}

type pinnedRegistrationKey struct {
	environmentID string
	profileDigest string
}

// NewRegistry builds a registry from current registrations plus optional
// pinned registrations. A pinned registration must name an environment that
// has a current registration and the same profile key; it may not duplicate
// the current digest or another pinned digest.
func NewRegistry(registrations []Registration, pinned ...Registration) (Registry, error) {
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
	pinnedByKey := make(map[pinnedRegistrationKey]Registration, len(pinned))
	for _, registration := range pinned {
		current, found := byEnvironment[registration.EnvironmentID]
		if !found || !validRegistration(registration) || registration.ProfileKey != current.ProfileKey ||
			registration.WorktreeID != current.WorktreeID || registration.ProfileDigest == current.ProfileDigest {
			return Registry{}, ErrProfileInvalid
		}
		key := pinnedRegistrationKey{environmentID: registration.EnvironmentID, profileDigest: registration.ProfileDigest}
		if _, duplicate := pinnedByKey[key]; duplicate {
			return Registry{}, ErrProfileInvalid
		}
		pinnedByKey[key] = registration
	}
	return Registry{byEnvironment: byEnvironment, pinned: pinnedByKey}, nil
}

// Lookup returns the current registration for an environment.
func (registry Registry) Lookup(environmentID string) (Registration, error) {
	registration, found := registry.byEnvironment[environmentID]
	if !found {
		return Registration{}, ErrProfileInvalid
	}
	return registration, nil
}

// LookupPinned returns the registration compiled from exactly the accepted
// profile digest a run is pinned to. An empty digest selects the current
// registration for results persisted before pinning was recorded. A digest
// that is neither current nor pinned is an error: a digest without its exact
// payload is insufficient.
func (registry Registry) LookupPinned(environmentID, profileDigest string) (Registration, error) {
	current, err := registry.Lookup(environmentID)
	if err != nil {
		return Registration{}, err
	}
	if profileDigest == "" || profileDigest == current.ProfileDigest {
		return current, nil
	}
	registration, found := registry.pinned[pinnedRegistrationKey{environmentID: environmentID, profileDigest: profileDigest}]
	if !found {
		return Registration{}, ErrProfileInvalid
	}
	return registration, nil
}

// PinnedDigests lists the non-current digests registered for an environment.
func (registry Registry) PinnedDigests(environmentID string) []string {
	digests := make([]string, 0)
	for key := range registry.pinned {
		if key.environmentID == environmentID {
			digests = append(digests, key.profileDigest)
		}
	}
	sort.Strings(digests)
	return digests
}

func validRegistration(registration Registration) bool {
	if !profileIDPattern.MatchString(registration.EnvironmentID) || !profileIDPattern.MatchString(registration.RepositoryID) ||
		!profileIDPattern.MatchString(registration.WorktreeID) || !profileIDPattern.MatchString(registration.ProfileKey) ||
		!profileIDPattern.MatchString(registration.DaemonInstanceID) || !strings.HasPrefix(registration.ProfileDigest, "sha256:") {
		return false
	}
	for _, path := range []string{
		registration.RepositoryRoot, registration.WorktreeRoot, registration.RuntimeRoot, registration.CacheRoot,
		registration.HomeDirectory, registration.HostHomeDirectory, registration.TemporaryDirectory,
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
