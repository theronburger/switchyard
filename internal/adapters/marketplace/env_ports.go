package marketplace

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type PortDefault struct {
	EnvironmentVariable string
	Port                int
}

func (catalog Catalog) ParsePortDefaults(contents []byte) ([]PortDefault, error) {
	whitelist := make(map[string]struct{})
	for _, definition := range catalog.definitions {
		for _, requirement := range definition.PortRequirements {
			if requirement.PreferredPortEnvironment != "" {
				whitelist[requirement.PreferredPortEnvironment] = struct{}{}
			}
		}
	}
	return ParsePortDefaults(contents, whitelist)
}

func ParsePortDefaults(contents []byte, whitelist map[string]struct{}) ([]PortDefault, error) {
	for name := range whitelist {
		if !isPortEnvironmentVariable(name) {
			return nil, fmt.Errorf("port whitelist contains invalid variable %q", name)
		}
	}

	ports := make(map[string]int, len(whitelist))
	seen := make(map[string]int, len(whitelist))
	for lineIndex, rawLine := range bytes.Split(contents, []byte{'\n'}) {
		lineNumber := lineIndex + 1
		line := strings.TrimSpace(strings.TrimSuffix(string(rawLine), "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))

		name, rawValue, hasAssignment := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if _, allowed := whitelist[name]; !allowed {
			continue
		}
		if !hasAssignment {
			return nil, fmt.Errorf("line %d: port variable %q has no assignment", lineNumber, name)
		}
		if firstLine, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf(
				"line %d: duplicate port variable %q first appeared on line %d",
				lineNumber,
				name,
				firstLine,
			)
		}

		port, err := parsePortValue(rawValue)
		if err != nil {
			return nil, fmt.Errorf("line %d: port variable %q: %w", lineNumber, name, err)
		}
		seen[name] = lineNumber
		ports[name] = port
	}

	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	defaults := make([]PortDefault, 0, len(names))
	for _, name := range names {
		defaults = append(defaults, PortDefault{EnvironmentVariable: name, Port: ports[name]})
	}
	return defaults, nil
}

func parsePortValue(rawValue string) (int, error) {
	value := strings.TrimSpace(rawValue)
	if value == "" {
		return 0, fmt.Errorf("value is empty")
	}

	if value[0] == '\'' || value[0] == '"' {
		quote := value[0]
		closingQuote := strings.IndexByte(value[1:], quote)
		if closingQuote < 0 {
			return 0, fmt.Errorf("quoted value is not terminated")
		}
		closingQuote++
		remainder := strings.TrimSpace(value[closingQuote+1:])
		if remainder != "" && !strings.HasPrefix(remainder, "#") {
			return 0, fmt.Errorf("quoted value has trailing data")
		}
		value = value[1:closingQuote]
	} else if comment := strings.IndexByte(value, '#'); comment >= 0 {
		value = strings.TrimSpace(value[:comment])
	}

	if value == "" {
		return 0, fmt.Errorf("value is empty")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("value is not an unsigned decimal integer")
		}
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("value is outside 1-65535")
	}
	return port, nil
}

func isPortEnvironmentVariable(name string) bool {
	if !strings.HasPrefix(name, "DEED_") || !strings.HasSuffix(name, "_PORT") {
		return false
	}
	middle := strings.TrimSuffix(strings.TrimPrefix(name, "DEED_"), "_PORT")
	if middle == "" {
		return false
	}
	for _, character := range middle {
		if (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '_' {
			return false
		}
	}
	return true
}
