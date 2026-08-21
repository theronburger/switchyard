// Package helperenv builds the bounded environment handed to Switchyard's own
// helper processes such as Git, the container host client, and read-only
// observers. Helpers never inherit the daemon environment wholesale: only a
// fixed allowlist of non-secret process-base names crosses, so a token or
// credential present in the daemon's environment cannot reach a child.
package helperenv

import (
	"os"
	"sort"
	"strings"
)

// allowedNames is the complete set of names a helper may inherit. Everything
// here describes the process base (home, search path, temporary directory,
// locale, user identity, and platform toolchain selection. Proxy variables
// are deliberately excluded because proxy URLs can embed credentials.
var allowedNames = map[string]struct{}{
	"HOME": {}, "PATH": {}, "TMPDIR": {}, "USER": {}, "LOGNAME": {}, "SHELL": {},
	"LANG": {}, "LC_ALL": {}, "LC_CTYPE": {}, "TZ": {},
	// The platform git shim resolves its toolchain from these.
	"DEVELOPER_DIR": {}, "SDKROOT": {},
	// The container host client selects its local endpoint from these; they
	// name a socket, context, or certificate directory, never a credential.
	"DOCKER_HOST": {}, "DOCKER_CONTEXT": {}, "DOCKER_CONFIG": {}, "DOCKER_CERT_PATH": {}, "DOCKER_TLS_VERIFY": {},
}

// Sanitized returns the daemon's own environment reduced to the helper
// allowlist, in sorted order, with duplicate names collapsed to the last value
// the platform reported.
func Sanitized() []string {
	return Filter(os.Environ())
}

// Filter applies the helper allowlist to source. Entries without a name, with
// an empty name, or holding a NUL byte are dropped.
func Filter(source []string) []string {
	values := make(map[string]string, len(allowedNames))
	for _, entry := range source {
		name, value, found := strings.Cut(entry, "=")
		if !found || name == "" || strings.ContainsRune(entry, 0) {
			continue
		}
		if _, allowed := allowedNames[name]; allowed {
			values[name] = value
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}

// Allowed reports whether name may cross into a helper environment.
func Allowed(name string) bool {
	_, allowed := allowedNames[name]
	return allowed
}
