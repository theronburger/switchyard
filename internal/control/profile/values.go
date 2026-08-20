package profile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/theronburger/switchyard/internal/configuration"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const maximumValueSourceBytes = 1024 * 1024

// ReadValues resolves bounded repository-owned inputs without placing their
// contents in configuration history, durable state, or diagnostics.
func ReadValues(profile configuration.Repository, repositoryRoot, worktreeRoot string) (map[string]string, error) {
	values := make(map[string]string, len(profile.Values))
	for id, source := range profile.Values {
		root := repositoryRoot
		if source.Root == "worktree" {
			root = worktreeRoot
		}
		contents, err := readBoundedValueFile(root, source.Path)
		if err != nil {
			return nil, fmt.Errorf("read configured value %q: %w", id, err)
		}
		value, err := decodeValueSource(source, contents)
		if err != nil {
			return nil, fmt.Errorf("decode configured value %q: %w", id, err)
		}
		if source.Trim {
			value = strings.TrimSpace(value)
		}
		if source.TrimPrefix != "" {
			if !strings.HasPrefix(value, source.TrimPrefix) {
				return nil, fmt.Errorf("decode configured value %q: required prefix is absent", id)
			}
			value = strings.TrimPrefix(value, source.TrimPrefix)
		}
		if strings.ContainsRune(value, 0) || len(value) > maximumValueSourceBytes {
			return nil, fmt.Errorf("decode configured value %q: value is invalid", id)
		}
		values[id] = value
	}
	return values, nil
}

func readBoundedValueFile(root, relative string) ([]byte, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || relative == "" || filepath.IsAbs(relative) ||
		filepath.Clean(relative) != relative || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("value source path is invalid")
	}
	rootDescriptor, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("value source is unavailable")
	}
	defer func() { _ = unix.Close(rootDescriptor) }()
	descriptor := rootDescriptor
	segments := strings.Split(filepath.ToSlash(relative), "/")
	for index, segment := range segments {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(segments)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(descriptor, segment, flags, 0)
		if descriptor != rootDescriptor {
			_ = unix.Close(descriptor)
		}
		if openErr != nil {
			return nil, errors.New("value source is unavailable")
		}
		descriptor = next
	}
	file := os.NewFile(uintptr(descriptor), filepath.Join(root, relative))
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximumValueSourceBytes {
		return nil, errors.New("value source is not a bounded regular file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumValueSourceBytes+1))
	if err != nil || len(contents) > maximumValueSourceBytes {
		return nil, errors.New("value source could not be read safely")
	}
	return contents, nil
}

func decodeValueSource(source configuration.ValueSource, contents []byte) (string, error) {
	switch source.Kind {
	case "text-file":
		return string(contents), nil
	case "dotenv":
		return dotenvValue(contents, source.Key)
	case "json-pointer":
		return structuredScalar(contents, source.Key, true)
	case "yaml-scalar":
		return structuredScalar(contents, source.Key, false)
	default:
		return "", errors.New("value source kind is unsupported")
	}
}

func dotenvValue(contents []byte, key string) (string, error) {
	if key == "" {
		return "", errors.New("dotenv key is required")
	}
	result := ""
	found := false
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	scanner.Buffer(make([]byte, 4096), maximumValueSourceBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimSuffix(scanner.Text(), "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, raw, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		if found {
			return "", errors.New("dotenv key is duplicated")
		}
		value, err := parseDotenvScalar(strings.TrimSpace(raw))
		if err != nil {
			return "", err
		}
		result, found = value, true
	}
	if err := scanner.Err(); err != nil {
		return "", errors.New("dotenv source is invalid")
	}
	if !found {
		return "", errors.New("dotenv key was not found")
	}
	return result, nil
}

func parseDotenvScalar(raw string) (string, error) {
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return raw[1 : len(raw)-1], nil
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		value, err := strconv.Unquote(raw)
		if err != nil {
			return "", errors.New("dotenv quoted value is invalid")
		}
		return value, nil
	}
	return raw, nil
}

func structuredScalar(contents []byte, pointer string, jsonSource bool) (string, error) {
	var value any
	if jsonSource {
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return "", errors.New("JSON source is invalid")
		}
	} else {
		var node yaml.Node
		if err := yaml.Unmarshal(contents, &node); err != nil {
			return "", errors.New("YAML source is invalid")
		}
		if err := node.Decode(&value); err != nil {
			return "", errors.New("YAML source is invalid")
		}
	}
	if pointer != "" {
		if !strings.HasPrefix(pointer, "/") {
			return "", errors.New("structured value key must be a JSON pointer")
		}
		for _, segment := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
			segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
			mapping, ok := value.(map[string]any)
			if !ok {
				return "", errors.New("structured value path is not a mapping")
			}
			value, ok = mapping[segment]
			if !ok {
				return "", errors.New("structured value path was not found")
			}
		}
	}
	switch scalar := value.(type) {
	case string:
		return scalar, nil
	case bool:
		return strconv.FormatBool(scalar), nil
	case json.Number:
		return scalar.String(), nil
	case int:
		return strconv.Itoa(scalar), nil
	case int64:
		return strconv.FormatInt(scalar, 10), nil
	case uint64:
		return strconv.FormatUint(scalar, 10), nil
	case float64:
		return strconv.FormatFloat(scalar, 'g', -1, 64), nil
	default:
		return "", errors.New("structured value is not a scalar")
	}
}
