package state

import (
	"net"
	"path/filepath"
	"reflect"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

func validateOperationRecord(record environmentcontrol.OperationRecord) error {
	if record.ID == "" || record.EnvironmentID == "" || record.RunID == "" || len(record.Failure) > 4096 {
		return ErrEnvironmentRecordInvalid
	}
	if record.Kind != environmentcontrol.OperationStart && record.Kind != environmentcontrol.OperationStop {
		return ErrEnvironmentRecordInvalid
	}
	if !validOperationState(record.State) || !validEnvironmentState(record.EnvironmentState) || !validOperationPhase(record.Phase) {
		return ErrEnvironmentRecordInvalid
	}
	if record.State == domain.OperationPending && record.Phase != environmentcontrol.PhasePending {
		return ErrEnvironmentRecordInvalid
	}
	if record.State == domain.OperationRunning && record.Phase == environmentcontrol.PhaseComplete {
		return ErrEnvironmentRecordInvalid
	}
	if terminalOperation(record.State) && record.Phase != environmentcontrol.PhaseComplete {
		return ErrEnvironmentRecordInvalid
	}
	if (record.State == domain.OperationPending || record.State == domain.OperationRunning || record.State == domain.OperationSucceeded) && record.Failure != "" {
		return ErrEnvironmentRecordInvalid
	}
	if (record.State == domain.OperationFailed || record.State == domain.OperationCancelled) && record.Failure == "" {
		return ErrEnvironmentRecordInvalid
	}
	if record.Rollback == nil {
		return ErrEnvironmentRecordInvalid
	}
	if record.Intent != nil {
		if record.Intent.Adapter == "" || record.Intent.ServiceIDs == nil || len(record.Intent.ServiceIDs) == 0 {
			return ErrEnvironmentRecordInvalid
		}
		seen := make(map[string]struct{}, len(record.Intent.ServiceIDs))
		for _, serviceID := range record.Intent.ServiceIDs {
			if serviceID == "" {
				return ErrEnvironmentRecordInvalid
			}
			if _, duplicate := seen[serviceID]; duplicate {
				return ErrEnvironmentRecordInvalid
			}
			seen[serviceID] = struct{}{}
		}
	}
	if record.Target != nil {
		if err := validateEnvironmentResult(*record.Target); err != nil || record.Target.EnvironmentID != record.EnvironmentID || record.Target.RunID != record.RunID {
			return ErrEnvironmentRecordInvalid
		}
	}
	if record.Kind == environmentcontrol.OperationStart && record.Target != nil {
		return ErrEnvironmentRecordInvalid
	}
	if record.Kind == environmentcontrol.OperationStop && (record.Target == nil || record.Intent != nil || len(record.Rollback) != 0) {
		return ErrEnvironmentRecordInvalid
	}
	for _, entry := range record.Rollback {
		if err := validateRollbackRecord(record.EnvironmentID, record.RunID, entry); err != nil {
			return ErrEnvironmentRecordInvalid
		}
	}
	return nil
}

func validateRollbackRecord(environmentID, runID string, entry environmentcontrol.RollbackEntry) error {
	switch entry.Kind {
	case environmentcontrol.RollbackPorts:
		if entry.PortKeys == nil || entry.Leases == nil || (entry.Armed && len(entry.PortKeys) == 0) {
			return ErrEnvironmentRecordInvalid
		}
		keys := make(map[portlease.Key]struct{}, len(entry.PortKeys))
		for _, key := range entry.PortKeys {
			if key.EnvironmentID != environmentID || key.ServiceID == "" || key.Purpose == "" {
				return ErrEnvironmentRecordInvalid
			}
			if _, duplicate := keys[key]; duplicate {
				return ErrEnvironmentRecordInvalid
			}
			keys[key] = struct{}{}
		}
		for _, lease := range entry.Leases {
			if lease.Key.EnvironmentID != environmentID || lease.Key.ServiceID == "" || lease.Key.Purpose == "" ||
				!validLeaseHost(lease.Host) || lease.Port < 1 || lease.Port > 65535 {
				return ErrEnvironmentRecordInvalid
			}
			if _, expected := keys[lease.Key]; !expected {
				return ErrEnvironmentRecordInvalid
			}
		}
	case environmentcontrol.RollbackProjection:
		if entry.Projection == nil || !validProjection(environmentID, runID, *entry.Projection) {
			return ErrEnvironmentRecordInvalid
		}
	case environmentcontrol.RollbackInfrastructure:
		if entry.Infrastructure == nil || (entry.Armed && len(entry.Infrastructure) == 0) {
			return ErrEnvironmentRecordInvalid
		}
		for _, goal := range entry.Infrastructure {
			if !validGoal(environmentID, runID, goal) {
				return ErrEnvironmentRecordInvalid
			}
		}
	case environmentcontrol.RollbackProcess:
		if entry.Process == nil || !validServiceIdentity(environmentID, runID, *entry.Process) {
			return ErrEnvironmentRecordInvalid
		}
		if entry.Applied {
			if !validServiceResult(environmentID, runID, *entry.Process) {
				return ErrEnvironmentRecordInvalid
			}
		} else if entry.Process.Process.EnvironmentID != "" || entry.Process.Process.ServiceID != "" || entry.Process.Process.RunID != "" {
			if !validServiceResult(environmentID, runID, *entry.Process) {
				return ErrEnvironmentRecordInvalid
			}
		}
	default:
		return ErrEnvironmentRecordInvalid
	}
	return nil
}

func validateEnvironmentResult(result environmentcontrol.EnvironmentResult) error {
	if result.EnvironmentID == "" || result.RunID == "" || result.UpdatedAt.IsZero() || !validEnvironmentState(result.State) ||
		result.Ports == nil || result.Infrastructure == nil || result.Services == nil {
		return ErrEnvironmentResultInvalid
	}
	if result.State == domain.EnvironmentUnknown || result.State == domain.EnvironmentStarting || result.State == domain.EnvironmentStopping {
		return ErrEnvironmentResultInvalid
	}
	seenPorts := make(map[int]struct{}, len(result.Ports))
	seenKeys := make(map[portlease.Key]struct{}, len(result.Ports))
	for _, lease := range result.Ports {
		if lease.Key.EnvironmentID != result.EnvironmentID || lease.Key.ServiceID == "" || lease.Key.Purpose == "" ||
			!validLeaseHost(lease.Host) || lease.Port < 1 || lease.Port > 65535 {
			return ErrEnvironmentResultInvalid
		}
		if _, duplicate := seenPorts[lease.Port]; duplicate {
			return ErrEnvironmentResultInvalid
		}
		seenPorts[lease.Port] = struct{}{}
		if _, duplicate := seenKeys[lease.Key]; duplicate {
			return ErrEnvironmentResultInvalid
		}
		seenKeys[lease.Key] = struct{}{}
	}
	if result.Projection != nil && !validProjection(result.EnvironmentID, result.RunID, *result.Projection) {
		return ErrEnvironmentResultInvalid
	}
	for _, goal := range result.Infrastructure {
		if !validGoal(result.EnvironmentID, result.RunID, goal) {
			return ErrEnvironmentResultInvalid
		}
	}
	seenServices := make(map[string]struct{}, len(result.Services))
	for _, service := range result.Services {
		if !validServiceResult(result.EnvironmentID, result.RunID, service) {
			return ErrEnvironmentResultInvalid
		}
		if _, duplicate := seenServices[service.ID]; duplicate {
			return ErrEnvironmentResultInvalid
		}
		seenServices[service.ID] = struct{}{}
	}
	return nil
}

func validLeaseHost(host string) bool {
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

func validProjection(environmentID, runID string, projection environmentcontrol.ProjectionChange) bool {
	return projection.Owned && projection.ID != "" && projection.RollbackToken != "" &&
		projection.EnvironmentID == environmentID && projection.RunID == runID
}

func validGoal(environmentID, runID string, goal containerhost.Goal) bool {
	return goal.Kind.Valid() && goal.Name != "" && goal.Identity.Validate() == nil &&
		goal.Identity.EnvironmentID == environmentID && goal.Identity.RunID == runID &&
		(goal.DesiredState == containerhost.DesiredRunning || goal.DesiredState == containerhost.DesiredStopped || goal.DesiredState == containerhost.DesiredAbsent)
}

func validServiceResult(environmentID, runID string, service environmentcontrol.ServiceResult) bool {
	if !validServiceIdentity(environmentID, runID, service) || service.Process.EnvironmentID != environmentID ||
		service.Process.RunID != runID || service.Process.ServiceID != service.ID || service.Process.Members == nil {
		return false
	}
	return validServiceObservation(service.Observation)
}

func validServiceObservation(observation environmentcontrol.ServiceObservation) bool {
	return observation.ProcessCount >= 0 && observation.MemoryBytes >= 0 && observation.CPUPercent >= 0 &&
		observation.CPUPercent <= 100 && len(observation.State) <= 64 && len(observation.Code) <= 128
}

func validEnvironmentRefresh(
	current environmentcontrol.EnvironmentResult,
	next environmentcontrol.EnvironmentResult,
) bool {
	if current.EnvironmentID != next.EnvironmentID || current.RunID != next.RunID ||
		current.State != domain.EnvironmentRunning || next.State != domain.EnvironmentRunning ||
		!reflect.DeepEqual(current.Ports, next.Ports) || !reflect.DeepEqual(current.Projection, next.Projection) ||
		!reflect.DeepEqual(current.Infrastructure, next.Infrastructure) || len(current.Services) != len(next.Services) {
		return false
	}
	for index := range current.Services {
		left := current.Services[index]
		right := next.Services[index]
		left.Health = environmentcontrol.HealthReport{}
		right.Health = environmentcontrol.HealthReport{}
		left.Observation = environmentcontrol.ServiceObservation{}
		right.Observation = environmentcontrol.ServiceObservation{}
		if !reflect.DeepEqual(left, right) {
			return false
		}
	}
	return true
}

func validServiceIdentity(environmentID, runID string, service environmentcontrol.ServiceResult) bool {
	return service.Owned && service.ID != "" && service.EnvironmentID == environmentID && service.RunID == runID &&
		filepath.IsAbs(service.OwnershipPath)
}

func validOperationState(state domain.OperationState) bool {
	return state == domain.OperationPending || state == domain.OperationRunning || state == domain.OperationSucceeded ||
		state == domain.OperationFailed || state == domain.OperationCancelled
}

func validEnvironmentState(state domain.EnvironmentState) bool {
	return state == domain.EnvironmentUnknown || state == domain.EnvironmentStopped || state == domain.EnvironmentStarting ||
		state == domain.EnvironmentRunning || state == domain.EnvironmentStopping || state == domain.EnvironmentFailed ||
		state == domain.EnvironmentOrphaned
}

func validOperationPhase(phase environmentcontrol.OperationPhase) bool {
	switch phase {
	case environmentcontrol.PhasePending,
		environmentcontrol.PhaseReservingPorts,
		environmentcontrol.PhasePreparingServices,
		environmentcontrol.PhaseMaterializing,
		environmentcontrol.PhaseEnsuringInfrastructure,
		environmentcontrol.PhaseInitializingInfrastructure,
		environmentcontrol.PhaseLaunchingServices,
		environmentcontrol.PhaseWaitingReadiness,
		environmentcontrol.PhaseStoppingServices,
		environmentcontrol.PhaseStoppingInfrastructure,
		environmentcontrol.PhaseRemovingProjection,
		environmentcontrol.PhaseReleasingPorts,
		environmentcontrol.PhaseRollingBack,
		environmentcontrol.PhaseComplete:
		return true
	default:
		return false
	}
}

func normalizeOperationRecord(record environmentcontrol.OperationRecord) environmentcontrol.OperationRecord {
	if len(record.Rollback) == 0 {
		record.Rollback = make([]environmentcontrol.RollbackEntry, 0)
	} else {
		record.Rollback = append([]environmentcontrol.RollbackEntry(nil), record.Rollback...)
	}
	for index := range record.Rollback {
		record.Rollback[index].PortKeys = append([]portlease.Key(nil), record.Rollback[index].PortKeys...)
		record.Rollback[index].Leases = append([]portlease.Lease(nil), record.Rollback[index].Leases...)
		record.Rollback[index].Infrastructure = append([]containerhost.Goal(nil), record.Rollback[index].Infrastructure...)
		if record.Rollback[index].PortKeys == nil {
			record.Rollback[index].PortKeys = make([]portlease.Key, 0)
		}
		if record.Rollback[index].Leases == nil {
			record.Rollback[index].Leases = make([]portlease.Lease, 0)
		}
		if record.Rollback[index].Infrastructure == nil {
			record.Rollback[index].Infrastructure = make([]containerhost.Goal, 0)
		}
		if record.Rollback[index].Process != nil {
			process := *record.Rollback[index].Process
			process.Process.Members = append([]processhost.ProcessIdentity(nil), process.Process.Members...)
			if process.Process.Members == nil {
				process.Process.Members = make([]processhost.ProcessIdentity, 0)
			}
			record.Rollback[index].Process = &process
		}
	}
	if record.Intent != nil {
		intent := *record.Intent
		if intent.ServiceIDs == nil {
			intent.ServiceIDs = make([]string, 0)
		} else {
			intent.ServiceIDs = append([]string(nil), intent.ServiceIDs...)
		}
		record.Intent = &intent
	}
	if record.Target != nil {
		target := normalizeEnvironmentResult(*record.Target)
		record.Target = &target
	}
	return record
}

func normalizeEnvironmentResult(result environmentcontrol.EnvironmentResult) environmentcontrol.EnvironmentResult {
	if len(result.Ports) == 0 {
		result.Ports = make([]portlease.Lease, 0)
	} else {
		result.Ports = append([]portlease.Lease(nil), result.Ports...)
	}
	if len(result.Infrastructure) == 0 {
		result.Infrastructure = make([]containerhost.Goal, 0)
	} else {
		result.Infrastructure = append([]containerhost.Goal(nil), result.Infrastructure...)
	}
	if len(result.Services) == 0 {
		result.Services = make([]environmentcontrol.ServiceResult, 0)
	} else {
		result.Services = append([]environmentcontrol.ServiceResult(nil), result.Services...)
	}
	for index := range result.Services {
		if result.Services[index].Process.Members == nil {
			result.Services[index].Process.Members = make([]processhost.ProcessIdentity, 0)
		} else {
			result.Services[index].Process.Members = append([]processhost.ProcessIdentity(nil), result.Services[index].Process.Members...)
		}
	}
	return result
}

func normalizeContractEnvironment(environment contractv1.Environment) contractv1.Environment {
	if environment.Services == nil {
		environment.Services = make([]contractv1.Service, 0)
	}
	if environment.PortLeases == nil {
		environment.PortLeases = make([]contractv1.PortLease, 0)
	}
	if environment.InfrastructureLeases == nil {
		environment.InfrastructureLeases = make([]contractv1.InfrastructureLease, 0)
	}
	if environment.URLs == nil {
		environment.URLs = make(map[string]string)
	}
	if environment.AttentionAlertIDs == nil {
		environment.AttentionAlertIDs = make([]string, 0)
	}
	for index := range environment.Services {
		if environment.Services[index].PortLeaseIDs == nil {
			environment.Services[index].PortLeaseIDs = make([]string, 0)
		}
	}
	return environment
}

func cloneContractEnvironment(environment *contractv1.Environment) *contractv1.Environment {
	if environment == nil {
		return nil
	}
	copy := *environment
	copy.Services = append([]contractv1.Service(nil), environment.Services...)
	for index := range copy.Services {
		copy.Services[index].PortLeaseIDs = append([]string(nil), environment.Services[index].PortLeaseIDs...)
	}
	copy.PortLeases = append([]contractv1.PortLease(nil), environment.PortLeases...)
	copy.InfrastructureLeases = append([]contractv1.InfrastructureLease(nil), environment.InfrastructureLeases...)
	copy.URLs = make(map[string]string, len(environment.URLs))
	for key, value := range environment.URLs {
		copy.URLs[key] = value
	}
	copy.AttentionAlertIDs = append([]string(nil), environment.AttentionAlertIDs...)
	return &copy
}
