package workspace

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maximumSteps        = 64
	maximumRequirements = 128
	maximumToolchains   = 16
	maximumArguments    = 256
	maximumEnvironment  = 256
)

var (
	idPattern          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	fingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func validatePlan(plan Plan) error {
	if !idPattern.MatchString(plan.WorktreeID) || !idPattern.MatchString(plan.Adapter) ||
		!cleanAbsolutePath(plan.WorktreeRoot) ||
		(plan.Ownership != OwnershipAdopted && plan.Ownership != OwnershipManaged) ||
		!fingerprintPattern.MatchString(plan.Fingerprint) || len(plan.Steps) > maximumSteps ||
		len(plan.Requirements) == 0 || len(plan.Requirements) > maximumRequirements ||
		len(plan.Toolchains) > maximumToolchains {
		return ErrInvalidPlan
	}
	seen := make(map[string]struct{})
	for _, step := range plan.Steps {
		if !validStep(step) {
			return ErrInvalidPlan
		}
		if _, duplicate := seen["step\x00"+step.ID]; duplicate {
			return ErrInvalidPlan
		}
		seen["step\x00"+step.ID] = struct{}{}
	}
	for _, requirement := range plan.Requirements {
		if !idPattern.MatchString(requirement.ID) || !cleanAbsolutePath(requirement.Path) ||
			(requirement.Kind != RequirementDirectory && requirement.Kind != RequirementRegularFile &&
				requirement.Kind != RequirementExecutable) {
			return ErrInvalidPlan
		}
		if _, duplicate := seen["requirement\x00"+requirement.ID]; duplicate {
			return ErrInvalidPlan
		}
		seen["requirement\x00"+requirement.ID] = struct{}{}
	}
	for _, toolchain := range plan.Toolchains {
		if !idPattern.MatchString(toolchain.ID) || toolchain.RequestedVersion == "" ||
			toolchain.ResolvedVersion == "" || !cleanAbsolutePath(toolchain.Executable) ||
			strings.ContainsRune(toolchain.RequestedVersion, 0) ||
			strings.ContainsRune(toolchain.ResolvedVersion, 0) {
			return ErrInvalidPlan
		}
		if _, duplicate := seen["toolchain\x00"+toolchain.ID]; duplicate {
			return ErrInvalidPlan
		}
		seen["toolchain\x00"+toolchain.ID] = struct{}{}
	}
	return nil
}

func validStep(step StepSpec) bool {
	if !idPattern.MatchString(step.ID) || !cleanAbsolutePath(step.Executable) ||
		!cleanAbsolutePath(step.Directory) || !cleanAbsolutePath(step.RunDirectory) ||
		step.Timeout <= 0 || step.Timeout > 60*time.Minute || len(step.Arguments) > maximumArguments ||
		len(step.Environment) == 0 || len(step.Environment) > maximumEnvironment {
		return false
	}
	for _, argument := range step.Arguments {
		if argument == "" || len(argument) > 1024*1024 || strings.ContainsRune(argument, 0) {
			return false
		}
	}
	seen := make(map[string]struct{}, len(step.Environment))
	for _, variable := range step.Environment {
		name, _, found := strings.Cut(variable, "=")
		if !found || name == "" || strings.ContainsRune(variable, 0) {
			return false
		}
		if _, duplicate := seen[name]; duplicate {
			return false
		}
		seen[name] = struct{}{}
	}
	return true
}

func validateRecord(record OperationRecord) error {
	if !idPattern.MatchString(record.OperationID) || !idPattern.MatchString(record.WorktreeID) ||
		!fingerprintPattern.MatchString(record.Fingerprint) || record.NextStep < 0 ||
		record.StepCount < 0 || record.StepCount > maximumSteps || record.NextStep > record.StepCount {
		return ErrInvalidRecord
	}
	validState := record.State == StatePending || record.State == StateRunning ||
		record.State == StateReady || record.State == StateFailed
	validPhase := record.Phase == PhasePending || record.Phase == PhasePreparing ||
		record.Phase == PhaseVerifying || record.Phase == PhaseComplete
	if !validState || !validPhase || (record.FailureCode != "" && !idPattern.MatchString(record.FailureCode)) {
		return ErrInvalidRecord
	}
	return nil
}

func validateResult(result Result) error {
	if result.State != StateReady || result.PreparedAt.IsZero() {
		return ErrInvalidPlan
	}
	plan := Plan{
		WorktreeID: result.WorktreeID, Adapter: result.Adapter, WorktreeRoot: result.WorktreeRoot,
		Ownership: result.Ownership, Fingerprint: result.Fingerprint,
		Requirements: []Requirement{{ID: "validation", Path: result.WorktreeRoot, Kind: RequirementDirectory}},
		Toolchains:   result.Toolchains,
	}
	return validatePlan(plan)
}

func cleanAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
}
