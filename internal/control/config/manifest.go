package config

import (
	"bytes"
	"fmt"
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
}

type DisplaySettings struct {
	Name string
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
	inDisplay := false
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
		field, rawValue, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found || field == "" {
			return LocalManifest{}, fmt.Errorf("line %d: expected a mapping field", lineNumber)
		}
		rawValue = strings.TrimSpace(rawValue)
		switch indentation {
		case 0:
			inDisplay = false
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
				inDisplay = true
			default:
				return LocalManifest{}, fmt.Errorf("line %d: unknown top-level field", lineNumber)
			}
		case 2:
			if !inDisplay {
				return LocalManifest{}, fmt.Errorf("line %d: nested field is outside display", lineNumber)
			}
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
		default:
			return LocalManifest{}, fmt.Errorf("line %d: indentation must be zero or two spaces", lineNumber)
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
	return manifest, nil
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
