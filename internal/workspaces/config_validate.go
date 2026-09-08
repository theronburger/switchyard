package workspaces

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var configID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var portVariable = regexp.MustCompile(`\{port:([^{}]*)\}`)

func ValidateConfig(config Config) error {
	if config.SchemaVersion != 1 {
		return errors.New("Configuration schemaVersion must be 1.")
	}
	ids, paths := map[string]bool{}, map[string]bool{}
	for index, repository := range config.Repositories {
		if !configID.MatchString(repository.ID) || ids[repository.ID] {
			return fmt.Errorf("Repository %d needs a unique ID using letters, numbers, hyphens or underscores.", index+1)
		}
		ids[repository.ID] = true
		if !absoluteConfigPath(repository.Path) || paths[repository.Path] {
			return fmt.Errorf("Repository %s needs a unique absolute directory path.", repository.ID)
		}
		paths[repository.Path] = true
		if repository.WorktreesPath != "" && !absoluteConfigPath(repository.WorktreesPath) {
			return fmt.Errorf("Repository %s worktreesPath must be absolute.", repository.ID)
		}
		if strings.TrimSpace(repository.Name) == "" || len(repository.Name) > 128 {
			return fmt.Errorf("Repository %s needs a name of at most 128 characters.", repository.ID)
		}
		if err := validateRepositoryConfig(repository); err != nil {
			return fmt.Errorf("Repository %s: %w", repository.ID, err)
		}
	}
	for path, choice := range config.Choices {
		if !absoluteConfigPath(path) || (choice.Target != "" && !configID.MatchString(choice.Target)) {
			return errors.New("Saved workspace choices need absolute paths and valid target IDs.")
		}
		seen := map[string]bool{}
		for _, service := range choice.Services {
			if !configID.MatchString(service) || seen[service] {
				return errors.New("Saved workspace choices contain an invalid or repeated service ID.")
			}
			seen[service] = true
		}
	}
	return nil
}

func validateRepositoryConfig(repository RepositoryConfig) error {
	targets, services, ports := map[string]bool{}, map[string]ServiceConfig{}, map[string]bool{}
	for _, target := range repository.Targets {
		if !configID.MatchString(target.ID) || targets[target.ID] {
			return errors.New("Targets need unique valid IDs.")
		}
		targets[target.ID] = true
		if strings.TrimSpace(target.Name) == "" || len(target.Name) > 128 {
			return errors.New("Targets need names of at most 128 characters.")
		}
	}
	if !targets[repository.DefaultTarget] {
		return errors.New("defaultTarget must identify a configured target.")
	}
	for _, service := range repository.Services {
		if !configID.MatchString(service.ID) {
			return errors.New("Services need valid IDs.")
		}
		if _, duplicate := services[service.ID]; duplicate {
			return errors.New("Service IDs must be unique.")
		}
		services[service.ID] = service
		if strings.TrimSpace(service.Name) == "" || len(service.Name) > 128 {
			return fmt.Errorf("Service %s needs a name of at most 128 characters.", service.ID)
		}
		portNames, variables := map[string]bool{}, map[string]bool{}
		for _, port := range service.Ports {
			if !configID.MatchString(port.Name) || portNames[port.Name] || port.Preferred < 0 || port.Preferred > 65535 {
				return fmt.Errorf("Service %s ports need unique valid names and numbers from 0 to 65535.", service.ID)
			}
			portNames[port.Name], ports[service.ID+"."+port.Name] = true, true
			if port.Environment != "" {
				if !environmentName.MatchString(port.Environment) || variables[port.Environment] {
					return fmt.Errorf("Service %s ports need distinct valid environment variable names.", service.ID)
				}
				variables[port.Environment] = true
			}
		}
		ready := service.Readiness
		if ready.Port != "" && !portNames[ready.Port] {
			return fmt.Errorf("Service %s readiness must name one of its ports.", service.ID)
		}
		if ready.Path != "" && (ready.Port == "" || !strings.HasPrefix(ready.Path, "/") || strings.ContainsAny(ready.Path, "\r\n\x00")) {
			return fmt.Errorf("Service %s HTTP readiness needs a port and a path beginning with /.", service.ID)
		}
		if ready.TimeoutSeconds < 0 || ready.TimeoutSeconds > 3600 {
			return fmt.Errorf("Service %s readiness timeout must be between 0 and 3600 seconds.", service.ID)
		}
	}
	for _, target := range repository.Targets {
		if err := validateCommandEnvironment(target.Environment, ports); err != nil {
			return fmt.Errorf("Target %s: %w", target.ID, err)
		}
	}
	for _, command := range repository.Setup {
		if err := validateCommand(command, ports); err != nil {
			return fmt.Errorf("Setup: %w", err)
		}
	}
	for _, service := range repository.Services {
		for _, command := range append(append([]Command{}, service.Prepare...), service.Command) {
			if err := validateCommand(command, ports); err != nil {
				return fmt.Errorf("Service %s: %w", service.ID, err)
			}
		}
		seen := map[string]bool{}
		for _, dependency := range service.Dependencies {
			if _, found := services[dependency]; !found || seen[dependency] {
				return fmt.Errorf("Service %s has a missing or repeated dependency.", service.ID)
			}
			seen[dependency] = true
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return false
		}
		if visited[id] {
			return true
		}
		visiting[id] = true
		for _, dependency := range services[id].Dependencies {
			if !visit(dependency) {
				return false
			}
		}
		visiting[id], visited[id] = false, true
		return true
	}
	for id := range services {
		if !visit(id) {
			return errors.New("Service dependencies contain a cycle.")
		}
	}
	return nil
}

func validateCommand(command Command, ports map[string]bool) error {
	if strings.TrimSpace(command.Script) == "" || strings.ContainsRune(command.Script, 0) || len(command.Script) > 256*1024 {
		return errors.New("Commands need a nonempty script smaller than 256 KB.")
	}
	directory := command.Directory
	if directory != "" && (filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == ".." ||
		strings.HasPrefix(directory, ".."+string(filepath.Separator)) || strings.ContainsRune(directory, 0)) {
		return errors.New("Command directory must be relative and remain inside the workspace.")
	}
	if command.TimeoutSeconds < 0 || command.TimeoutSeconds > 86400 {
		return errors.New("Command timeout must be between 0 and 86400 seconds.")
	}
	return validateCommandEnvironment(command.Environment, ports)
}

func validateCommandEnvironment(environment map[string]string, ports map[string]bool) error {
	for name, value := range environment {
		if !environmentName.MatchString(name) || strings.ContainsRune(value, 0) {
			return errors.New("Environment entries need valid names and cannot contain NUL characters.")
		}
		for _, match := range portVariable.FindAllStringSubmatch(value, -1) {
			if !ports[match[1]] {
				return errors.New("Environment references an unknown {port:service.port}.")
			}
		}
		if strings.Contains(portVariable.ReplaceAllString(value, ""), "{port:") {
			return errors.New("Environment contains an incomplete port reference.")
		}
	}
	return nil
}

func absoluteConfigPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
}
