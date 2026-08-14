package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type TurboPackage struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type TurboDiscovery struct {
	runner          CommandRunner
	turboExecutable string
}

func NewTurboDiscovery(runner CommandRunner, turboExecutable string) TurboDiscovery {
	return TurboDiscovery{runner: runner, turboExecutable: turboExecutable}
}

func (discovery TurboDiscovery) ListPackages(ctx context.Context, repositoryRoot string) ([]TurboPackage, error) {
	if err := discovery.validate(repositoryRoot); err != nil {
		return nil, fmt.Errorf("list Turbo packages: %w", err)
	}

	output, err := discovery.runner.Run(ctx, Invocation{
		Executable: discovery.turboExecutable,
		Arguments: []string{
			"--no-update-notifier",
			"ls",
			"--filter=./services/*",
			"--filter=api",
			"--filter=app",
			"--filter=organizer",
			"--output=json",
		},
		WorkingDirectory: repositoryRoot,
	})
	if err != nil {
		return nil, fmt.Errorf("list Turbo packages: %w", err)
	}

	packages, err := ParseTurboPackages(output.Stdout)
	if err != nil {
		return nil, fmt.Errorf("parse Turbo packages: %w", err)
	}
	return packages, nil
}

func (discovery TurboDiscovery) AffectedPackages(
	ctx context.Context,
	repositoryRoot string,
	mergeBaseRevision string,
) ([]string, error) {
	if err := discovery.validate(repositoryRoot); err != nil {
		return nil, fmt.Errorf("find affected Turbo packages: %w", err)
	}
	if !isGitObjectID(mergeBaseRevision) {
		return nil, fmt.Errorf("find affected Turbo packages: merge-base revision must be a Git object ID")
	}

	output, err := discovery.runner.Run(ctx, Invocation{
		Executable: discovery.turboExecutable,
		Arguments: []string{
			"--no-update-notifier",
			"run",
			"start",
			"--dry-run=json",
			"--filter=...[" + mergeBaseRevision + "]",
		},
		WorkingDirectory: repositoryRoot,
	})
	if err != nil {
		return nil, fmt.Errorf("find affected Turbo packages: %w", err)
	}

	packages, err := ParseAffectedTurboPackages(output.Stdout)
	if err != nil {
		return nil, fmt.Errorf("parse affected Turbo packages: %w", err)
	}
	return packages, nil
}

func (discovery TurboDiscovery) validate(repositoryRoot string) error {
	if discovery.runner == nil {
		return fmt.Errorf("command runner is required")
	}
	if discovery.turboExecutable == "" {
		return fmt.Errorf("Turbo executable is required")
	}
	if repositoryRoot == "" {
		return fmt.Errorf("repository root is required")
	}
	return nil
}

func ParseTurboPackages(contents []byte) ([]TurboPackage, error) {
	var envelope struct {
		Packages json.RawMessage `json:"packages"`
	}
	if err := decodeSingleJSONValue(contents, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Packages) == 0 || bytes.Equal(envelope.Packages, []byte("null")) {
		return nil, fmt.Errorf("packages field is required")
	}

	var packages []TurboPackage
	switch firstJSONByte(envelope.Packages) {
	case '{':
		var collection struct {
			Items []TurboPackage `json:"items"`
		}
		if err := json.Unmarshal(envelope.Packages, &collection); err != nil {
			return nil, fmt.Errorf("decode packages collection: %w", err)
		}
		if collection.Items == nil {
			return nil, fmt.Errorf("packages items field is required")
		}
		packages = collection.Items
	case '[':
		if err := json.Unmarshal(envelope.Packages, &packages); err != nil {
			return nil, fmt.Errorf("decode packages array: %w", err)
		}
	default:
		return nil, fmt.Errorf("packages must be an object or array")
	}

	if err := validateTurboPackages(packages); err != nil {
		return nil, err
	}
	return packages, nil
}

func ParseAffectedTurboPackages(contents []byte) ([]string, error) {
	var dryRun struct {
		Tasks []struct {
			Package string `json:"package"`
			Task    string `json:"task"`
			Command string `json:"command"`
		} `json:"tasks"`
	}
	if err := decodeSingleJSONValue(contents, &dryRun); err != nil {
		return nil, err
	}
	if dryRun.Tasks == nil {
		return nil, fmt.Errorf("tasks field is required")
	}

	seen := make(map[string]struct{}, len(dryRun.Tasks))
	packages := make([]string, 0, len(dryRun.Tasks))
	for taskIndex, task := range dryRun.Tasks {
		if task.Task != "start" || task.Command == "<NONEXISTENT>" {
			continue
		}
		if task.Package == "" {
			return nil, fmt.Errorf("task %d has no package", taskIndex+1)
		}
		if task.Command == "" {
			return nil, fmt.Errorf("task %d has no command", taskIndex+1)
		}
		if _, exists := seen[task.Package]; exists {
			continue
		}
		seen[task.Package] = struct{}{}
		packages = append(packages, task.Package)
	}
	return packages, nil
}

func validateTurboPackages(packages []TurboPackage) error {
	seen := make(map[string]struct{}, len(packages))
	for packageIndex, turboPackage := range packages {
		if turboPackage.Name == "" {
			return fmt.Errorf("package %d has no name", packageIndex+1)
		}
		if _, exists := seen[turboPackage.Name]; exists {
			return fmt.Errorf("duplicate package %q", turboPackage.Name)
		}
		seen[turboPackage.Name] = struct{}{}
	}
	return nil
}

func decodeSingleJSONValue(contents []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	err := decoder.Decode(&trailing)
	if err == nil {
		return fmt.Errorf("decode JSON: multiple values are not allowed")
	}
	if err != io.EOF {
		return fmt.Errorf("decode JSON trailing content: %w", err)
	}
	return nil
}

func firstJSONByte(contents []byte) byte {
	trimmed := bytes.TrimSpace(contents)
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}
