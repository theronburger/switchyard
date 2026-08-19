package config

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

const (
	LocalManifestFilename       = ".switchyard.yaml"
	LocalManifestExcludePattern = "/.switchyard.yaml"
	LocalManifestSchemaVersion  = 1
)

type LocalManifest struct {
	SchemaVersion int
	Adapter       string
	Display       DisplaySettings
	Runtime       RuntimeSettings
	Workspace     WorkspaceSettings
}

type DisplaySettings struct {
	Name string
}

type RuntimeSettings struct {
	DefaultTarget      string
	Targets            []string
	WarnOnStartTargets []string
	Services           []string
}

type WorkspaceSettings struct {
	ManagedRoot string
	DefaultBase string
}

func ParseLocalManifest(contents []byte) (LocalManifest, error) {
	if len(contents) == 0 {
		return LocalManifest{}, fmt.Errorf("local manifest is empty")
	}
	if bytes.IndexByte(contents, 0) >= 0 {
		return LocalManifest{}, fmt.Errorf("local manifest contains a NUL byte")
	}

	manifest := LocalManifest{}
	seenTopLevel := make(map[string]struct{})
	seenDisplay := make(map[string]struct{})
	seenRuntime := make(map[string]struct{})
	seenWorkspace := make(map[string]struct{})
	section := ""
	runtimeList := ""
	for lineIndex, rawLine := range bytes.Split(contents, []byte{'\n'}) {
		lineNumber := lineIndex + 1
		line, err := removeYAMLComment(string(bytes.TrimSuffix(rawLine, []byte{'\r'})))
		if err != nil {
			return LocalManifest{}, fmt.Errorf("line %d: invalid quoted scalar", lineNumber)
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.ContainsRune(line, '\t') {
			return LocalManifest{}, fmt.Errorf("line %d: tabs are not allowed", lineNumber)
		}

		indentation := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		if indentation == 4 {
			if section != "runtime" || runtimeList == "" || !strings.HasPrefix(trimmed, "- ") {
				return LocalManifest{}, fmt.Errorf("line %d: invalid runtime list item", lineNumber)
			}
			value, err := parseYAMLScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			if err != nil || !isRuntimeID(value) {
				return LocalManifest{}, fmt.Errorf("line %d: invalid runtime identifier", lineNumber)
			}
			switch runtimeList {
			case "targets":
				manifest.Runtime.Targets = append(manifest.Runtime.Targets, value)
			case "services":
				manifest.Runtime.Services = append(manifest.Runtime.Services, value)
			case "warnOnStart":
				manifest.Runtime.WarnOnStartTargets = append(manifest.Runtime.WarnOnStartTargets, value)
			default:
				return LocalManifest{}, fmt.Errorf("line %d: invalid runtime list", lineNumber)
			}
			continue
		}

		field, rawValue, found := strings.Cut(trimmed, ":")
		if !found || field == "" {
			return LocalManifest{}, fmt.Errorf("line %d: expected a mapping field", lineNumber)
		}
		rawValue = strings.TrimSpace(rawValue)
		switch indentation {
		case 0:
			section = ""
			runtimeList = ""
			if _, duplicate := seenTopLevel[field]; duplicate {
				return LocalManifest{}, fmt.Errorf("line %d: duplicate top-level field", lineNumber)
			}
			seenTopLevel[field] = struct{}{}
			switch field {
			case "schemaVersion":
				value, err := parseYAMLScalar(rawValue)
				if err != nil {
					return LocalManifest{}, fmt.Errorf("line %d: invalid schema version", lineNumber)
				}
				manifest.SchemaVersion, err = strconv.Atoi(value)
				if err != nil {
					return LocalManifest{}, fmt.Errorf("line %d: invalid schema version", lineNumber)
				}
			case "adapter":
				value, err := parseYAMLScalar(rawValue)
				if err != nil {
					return LocalManifest{}, fmt.Errorf("line %d: invalid adapter", lineNumber)
				}
				manifest.Adapter = value
			case "display":
				if rawValue != "" {
					return LocalManifest{}, fmt.Errorf("line %d: display must be a mapping", lineNumber)
				}
				section = "display"
			case "runtime":
				if rawValue != "" {
					return LocalManifest{}, fmt.Errorf("line %d: runtime must be a mapping", lineNumber)
				}
				section = "runtime"
			case "workspace":
				if rawValue != "" {
					return LocalManifest{}, fmt.Errorf("line %d: workspace must be a mapping", lineNumber)
				}
				section = "workspace"
			default:
				return LocalManifest{}, fmt.Errorf("line %d: unknown top-level field", lineNumber)
			}
		case 2:
			runtimeList = ""
			switch section {
			case "display":
				if _, duplicate := seenDisplay[field]; duplicate {
					return LocalManifest{}, fmt.Errorf("line %d: duplicate display field", lineNumber)
				}
				seenDisplay[field] = struct{}{}
				if field != "name" {
					return LocalManifest{}, fmt.Errorf("line %d: unknown display field", lineNumber)
				}
				value, err := parseYAMLScalar(rawValue)
				if err != nil {
					return LocalManifest{}, fmt.Errorf("line %d: invalid display name", lineNumber)
				}
				manifest.Display.Name = value
			case "runtime":
				if _, duplicate := seenRuntime[field]; duplicate {
					return LocalManifest{}, fmt.Errorf("line %d: duplicate runtime field", lineNumber)
				}
				seenRuntime[field] = struct{}{}
				switch field {
				case "defaultTarget":
					value, err := parseYAMLScalar(rawValue)
					if err != nil || !isRuntimeID(value) {
						return LocalManifest{}, fmt.Errorf("line %d: invalid default target", lineNumber)
					}
					manifest.Runtime.DefaultTarget = value
				case "targets", "warnOnStart", "services":
					if rawValue != "" {
						return LocalManifest{}, fmt.Errorf("line %d: runtime %s must be a list", lineNumber, field)
					}
					runtimeList = field
					switch field {
					case "targets":
						manifest.Runtime.Targets = []string{}
					case "warnOnStart":
						manifest.Runtime.WarnOnStartTargets = []string{}
					case "services":
						manifest.Runtime.Services = []string{}
					}
				default:
					return LocalManifest{}, fmt.Errorf("line %d: unknown runtime field", lineNumber)
				}
			case "workspace":
				if _, duplicate := seenWorkspace[field]; duplicate {
					return LocalManifest{}, fmt.Errorf("line %d: duplicate workspace field", lineNumber)
				}
				seenWorkspace[field] = struct{}{}
				value, err := parseYAMLScalar(rawValue)
				if err != nil {
					return LocalManifest{}, fmt.Errorf("line %d: invalid workspace field", lineNumber)
				}
				switch field {
				case "managedRoot":
					manifest.Workspace.ManagedRoot = value
				case "defaultBase":
					manifest.Workspace.DefaultBase = value
				default:
					return LocalManifest{}, fmt.Errorf("line %d: unknown workspace field", lineNumber)
				}
			default:
				return LocalManifest{}, fmt.Errorf("line %d: nested field is outside a mapping", lineNumber)
			}
		default:
			return LocalManifest{}, fmt.Errorf("line %d: indentation must be zero, two, or four spaces", lineNumber)
		}
	}

	if manifest.SchemaVersion != LocalManifestSchemaVersion {
		return LocalManifest{}, fmt.Errorf("local manifest schema version is unsupported")
	}
	if !isAdapterName(manifest.Adapter) {
		return LocalManifest{}, fmt.Errorf("local manifest adapter is invalid")
	}
	if manifest.Display.Name != "" && !isDisplayName(manifest.Display.Name) {
		return LocalManifest{}, fmt.Errorf("local manifest display name is invalid")
	}
	if err := validateRuntimeSettings(manifest.Runtime); err != nil {
		return LocalManifest{}, err
	}
	if err := validateWorkspaceSettings(manifest.Workspace); err != nil {
		return LocalManifest{}, err
	}
	return manifest, nil
}

func validateWorkspaceSettings(settings WorkspaceSettings) error {
	if settings.ManagedRoot != "" && (!filepath.IsAbs(settings.ManagedRoot) ||
		filepath.Clean(settings.ManagedRoot) != settings.ManagedRoot || settings.ManagedRoot == string(filepath.Separator)) {
		return fmt.Errorf("local manifest managed workspace root is invalid")
	}
	if settings.DefaultBase != "" && (!isDisplayName(settings.DefaultBase) || len(settings.DefaultBase) > 256) {
		return fmt.Errorf("local manifest default workspace base is invalid")
	}
	return nil
}

func validateRuntimeSettings(settings RuntimeSettings) error {
	configured := settings.DefaultTarget != "" || settings.Targets != nil ||
		settings.WarnOnStartTargets != nil || settings.Services != nil
	if !configured {
		return nil
	}
	if settings.DefaultTarget == "" || len(settings.Targets) == 0 || len(settings.Targets) > 16 ||
		len(settings.Services) == 0 || len(settings.Services) > 32 {
		return fmt.Errorf("local manifest runtime settings are incomplete or unbounded")
	}
	targets, err := uniqueRuntimeIDs(settings.Targets)
	if err != nil {
		return err
	}
	if _, found := targets[settings.DefaultTarget]; !found {
		return fmt.Errorf("local manifest default target is not listed")
	}
	warnTargets, err := uniqueRuntimeIDs(settings.WarnOnStartTargets)
	if err != nil {
		return err
	}
	for target := range warnTargets {
		if _, found := targets[target]; !found {
			return fmt.Errorf("local manifest warn-on-start target is not listed")
		}
	}
	if _, err := uniqueRuntimeIDs(settings.Services); err != nil {
		return err
	}
	return nil
}

func uniqueRuntimeIDs(values []string) (map[string]struct{}, error) {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !isRuntimeID(value) {
			return nil, fmt.Errorf("local manifest runtime identifier is invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("local manifest runtime identifier is duplicated")
		}
		seen[value] = struct{}{}
	}
	return seen, nil
}

func isRuntimeID(value string) bool {
	return isAdapterName(value)
}

func removeYAMLComment(line string) (string, error) {
	var quote byte
	for index := 0; index < len(line); index++ {
		character := line[index]
		switch {
		case quote == 0 && (character == '\'' || character == '"'):
			quote = character
		case quote == '\'' && character == '\'' && index+1 < len(line) && line[index+1] == '\'':
			index++
		case quote == '"' && character == '\\' && index+1 < len(line):
			index++
		case quote != 0 && quote == character:
			quote = 0
		case quote == 0 && character == '#':
			return strings.TrimRight(line[:index], " "), nil
		}
	}
	if quote != 0 {
		return "", fmt.Errorf("unterminated quote")
	}
	return strings.TrimRight(line, " "), nil
}

func parseYAMLScalar(rawValue string) (string, error) {
	if rawValue == "" {
		return "", fmt.Errorf("scalar is empty")
	}
	if rawValue[0] == '"' {
		value, err := strconv.Unquote(rawValue)
		if err != nil {
			return "", err
		}
		return value, nil
	}
	if rawValue[0] == '\'' {
		if len(rawValue) < 2 || rawValue[len(rawValue)-1] != '\'' {
			return "", fmt.Errorf("single-quoted scalar is not terminated")
		}
		return strings.ReplaceAll(rawValue[1:len(rawValue)-1], "''", "'"), nil
	}
	if strings.ContainsAny(rawValue, "[]{}&*!|>%@`\"'") {
		return "", fmt.Errorf("plain scalar uses unsupported YAML syntax")
	}
	return strings.TrimSpace(rawValue), nil
}

func isAdapterName(adapter string) bool {
	if adapter == "" || adapter[0] < 'a' || adapter[0] > 'z' {
		return false
	}
	for _, character := range adapter[1:] {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func isDisplayName(displayName string) bool {
	if strings.TrimSpace(displayName) == "" || len(displayName) > 80 {
		return false
	}
	for _, character := range displayName {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
