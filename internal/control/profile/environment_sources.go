package profile

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/theronburger/switchyard/internal/configuration"
)

// Environment source bounds. A dotenv file is repository-owned input, so it is
// read through the same no-follow, containment-checked opener as configured
// values and parsed as data with explicit limits on size, line length, entry
// count, and value length.
const (
	maximumEnvironmentSourceBytes   = 256 * 1024
	maximumEnvironmentSourceLine    = 32 * 1024
	maximumEnvironmentSourceEntries = 1024
)

var errEnvironmentSourceInvalid = errors.New("environment source is invalid")

// dotenvNamePattern is the only entry-name shape parseDotenv accepts; anything
// else on the left of `=` is a malformed line rather than a value to keep.
var dotenvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ReadEnvironmentSources resolves the allowlisted dotenv entries that the
// accepted profile declares for one target. The result is held in memory for
// the plan being compiled and is never persisted, logged, or echoed in an
// error: failures name only the source ID, its declared path, and a line
// number.
//
// Sources apply in sorted ID order, but configuration validation already
// guarantees that sources applicable to the same target never allow the same
// name, so no source can shadow another. A missing file is an error unless
// the source is optional (a symlink or unreadable file is never tolerated); an
// allowed name absent from the file is simply not set. The file is never
// sourced as shell and values are never expanded.
func ReadEnvironmentSources(registration Registration, targetID string) (map[string]string, error) {
	profile := registration.Profile
	if _, found := profile.Targets[targetID]; !found {
		return nil, ErrProfileInvalid
	}
	ids := make([]string, 0, len(profile.EnvironmentSources))
	for id := range profile.EnvironmentSources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make(map[string]string)
	for _, id := range ids {
		source := profile.EnvironmentSources[id]
		if !environmentSourceApplies(source, targetID) {
			continue
		}
		if source.Kind != "dotenv" || (source.Root != "repository" && source.Root != "worktree") {
			return nil, fmt.Errorf("environment source %q: %w", id, ErrProfileInvalid)
		}
		root := registration.RepositoryRoot
		if source.Root == "worktree" {
			root = registration.WorktreeRoot
		}
		contents, err := readBoundedValueFile(root, source.Path)
		if err != nil {
			if source.Optional && errors.Is(err, errValueSourceMissing) {
				continue
			}
			return nil, fmt.Errorf("environment source %q (%s): %w", id, source.Path, err)
		}
		if len(contents) > maximumEnvironmentSourceBytes {
			return nil, fmt.Errorf("environment source %q (%s): file exceeds %d bytes", id, source.Path, maximumEnvironmentSourceBytes)
		}
		entries, err := parseDotenv(contents)
		if err != nil {
			return nil, fmt.Errorf("environment source %q (%s): %w", id, source.Path, err)
		}
		for _, name := range source.Allow {
			if !configuration.EnvironmentSourceNameAllowed(name) {
				return nil, fmt.Errorf("environment source %q: %w", id, ErrProfileInvalid)
			}
			value, present := entries[name]
			if !present {
				continue
			}
			if _, duplicate := result[name]; duplicate {
				return nil, fmt.Errorf("environment source %q: %q is already set by another source", id, name)
			}
			result[name] = value
		}
	}
	return result, nil
}

func environmentSourceApplies(source configuration.EnvironmentSource, targetID string) bool {
	if len(source.Targets) == 0 {
		return true
	}
	for _, candidate := range source.Targets {
		if candidate == targetID {
			return true
		}
	}
	return false
}

// parseDotenv reads a dotenv file as data. It accepts blank lines, `#`
// comments, an optional `export ` prefix, bare values, single-quoted literal
// values, and double-quoted values with Go-style escapes. It rejects NUL
// bytes, lines without `=`, invalid names, duplicate names, unterminated or
// mismatched quotes, over-long lines, and too many entries. Nothing is
// expanded, substituted, or executed. Error text carries line numbers only.
func parseDotenv(contents []byte) (map[string]string, error) {
	if bytes.IndexByte(contents, 0) >= 0 {
		return nil, fmt.Errorf("%w: file contains a NUL byte", errEnvironmentSourceInvalid)
	}
	entries := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	scanner.Buffer(make([]byte, 4096), maximumEnvironmentSourceLine+1)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		raw := scanner.Text()
		if len(raw) > maximumEnvironmentSourceLine {
			return nil, fmt.Errorf("%w: line %d exceeds %d bytes", errEnvironmentSourceInvalid, lineNumber, maximumEnvironmentSourceLine)
		}
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, found := strings.CutPrefix(line, "export"); found && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}
		name, rawValue, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("%w: line %d is not an assignment", errEnvironmentSourceInvalid, lineNumber)
		}
		name = strings.TrimSpace(name)
		if !dotenvNamePattern.MatchString(name) {
			return nil, fmt.Errorf("%w: line %d has an invalid name", errEnvironmentSourceInvalid, lineNumber)
		}
		if _, duplicate := entries[name]; duplicate {
			return nil, fmt.Errorf("%w: line %d repeats %q", errEnvironmentSourceInvalid, lineNumber, name)
		}
		if len(entries) >= maximumEnvironmentSourceEntries {
			return nil, fmt.Errorf("%w: more than %d entries", errEnvironmentSourceInvalid, maximumEnvironmentSourceEntries)
		}
		value, err := parseDotenvValue(strings.TrimSpace(rawValue))
		if err != nil {
			return nil, fmt.Errorf("%w: line %d %v", errEnvironmentSourceInvalid, lineNumber, err)
		}
		entries[name] = value
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("%w: line %d exceeds %d bytes", errEnvironmentSourceInvalid, lineNumber+1, maximumEnvironmentSourceLine)
		}
		return nil, fmt.Errorf("%w: file could not be scanned", errEnvironmentSourceInvalid)
	}
	return entries, nil
}

// parseDotenvValue decodes one trimmed right-hand side. A value that opens a
// quote must close the same quote at the end of the line; anything after the
// closing quote is malformed rather than silently kept or dropped.
func parseDotenvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	switch raw[0] {
	case '\'':
		if len(raw) < 2 || raw[len(raw)-1] != '\'' {
			return "", errors.New("has an unterminated single-quoted value")
		}
		value := raw[1 : len(raw)-1]
		if strings.ContainsRune(value, '\'') {
			return "", errors.New("has a malformed single-quoted value")
		}
		return value, nil
	case '"':
		value, err := strconv.Unquote(raw)
		if err != nil {
			return "", errors.New("has a malformed double-quoted value")
		}
		return value, nil
	}
	if strings.ContainsAny(raw, "'\"") {
		return "", errors.New("has a malformed value")
	}
	return raw, nil
}
