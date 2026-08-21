package environment

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

func validateStartRequest(request StartRequest) error {
	if request.OperationID == "" || request.EnvironmentID == "" || request.RunID == "" {
		return ErrInvalidRequest
	}
	seenPorts := make(map[portlease.Key]struct{}, len(request.Ports))
	for _, reservation := range request.Ports {
		if reservation.Key.EnvironmentID != request.EnvironmentID || reservation.Key.ServiceID == "" ||
			reservation.Key.Purpose == "" {
			return ErrInvalidRequest
		}
		if _, duplicate := seenPorts[reservation.Key]; duplicate {
			return ErrInvalidRequest
		}
		seenPorts[reservation.Key] = struct{}{}
	}
	if request.Intent != nil {
		if request.Intent.ProfileDigest == "" || len(request.Intent.ServiceIDs) == 0 {
			return ErrInvalidRequest
		}
		seenServices := make(map[string]struct{}, len(request.Intent.ServiceIDs))
		for _, serviceID := range request.Intent.ServiceIDs {
			if serviceID == "" {
				return ErrInvalidRequest
			}
			if _, duplicate := seenServices[serviceID]; duplicate {
				return ErrInvalidRequest
			}
			seenServices[serviceID] = struct{}{}
		}
	}
	if request.Source != nil && !validSourceSnapshot(*request.Source) {
		return ErrInvalidRequest
	}
	return nil
}

func validSourceSnapshot(source SourceSnapshot) bool {
	if source.ObservedAt.IsZero() || (len(source.Revision) != 40 && len(source.Revision) != 64) {
		return false
	}
	for _, character := range source.Revision {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}

func validateExecutionPlan(
	environmentID string,
	runID string,
	leases []portlease.Lease,
	plan ExecutionPlan,
) error {
	finiteCommands := make([]PreparationSpec, 0, len(plan.Preparations)+len(plan.Initializations))
	finiteCommands = append(finiteCommands, plan.Preparations...)
	finiteCommands = append(finiteCommands, plan.Initializations...)
	commandIDs := make(map[string]struct{}, len(finiteCommands))
	commandRunDirectories := make([]string, 0, len(finiteCommands))
	for _, preparation := range finiteCommands {
		if !validPreparation(preparation) {
			return ErrInvalidRequest
		}
		if _, duplicate := commandIDs[preparation.ID]; duplicate {
			return ErrInvalidRequest
		}
		for _, existing := range commandRunDirectories {
			if pathsOverlap(existing, preparation.RunDirectory) {
				return ErrInvalidRequest
			}
		}
		commandIDs[preparation.ID] = struct{}{}
		commandRunDirectories = append(commandRunDirectories, preparation.RunDirectory)
	}
	if plan.Projection != nil {
		if plan.Projection.ID == "" {
			return ErrInvalidRequest
		}
		seenArtifacts := make(map[string]struct{}, len(plan.Projection.ArtifactIDs))
		for _, artifactID := range plan.Projection.ArtifactIDs {
			if artifactID == "" {
				return ErrInvalidRequest
			}
			if _, duplicate := seenArtifacts[artifactID]; duplicate {
				return ErrInvalidRequest
			}
			seenArtifacts[artifactID] = struct{}{}
		}
	}
	for _, goal := range plan.Infrastructure {
		if !goal.Kind.Valid() || goal.Name == "" || goal.Identity.Validate() != nil ||
			goal.Identity.EnvironmentID != environmentID || goal.Identity.RunID != runID ||
			goal.DesiredState != containerhost.DesiredRunning {
			return ErrInvalidRequest
		}
	}
	if len(plan.Initializations) != 0 && len(plan.Infrastructure) == 0 {
		return ErrInvalidRequest
	}
	services := flattenedServiceStages(plan)
	if len(plan.Services) != 0 && len(plan.ServiceStages) != 0 {
		return ErrInvalidRequest
	}
	seenServices := make(map[string]struct{}, len(services))
	leaseKeys := make(map[portlease.Key]struct{}, len(leases))
	for _, lease := range leases {
		leaseKeys[lease.Key] = struct{}{}
	}
	for _, service := range services {
		if service.ID == "" || service.Process.EnvironmentID != environmentID ||
			service.Process.ServiceID != service.ID || service.Process.RunID != runID ||
			!filepath.IsAbs(service.Process.RunDirectory) {
			return ErrInvalidRequest
		}
		if _, duplicate := seenServices[service.ID]; duplicate {
			return ErrInvalidRequest
		}
		for _, preparationRunDirectory := range commandRunDirectories {
			if pathsOverlap(preparationRunDirectory, service.Process.RunDirectory) {
				return ErrInvalidRequest
			}
		}
		seenServices[service.ID] = struct{}{}
		seenServicePorts := make(map[portlease.Key]struct{}, len(service.PortKeys))
		for _, key := range service.PortKeys {
			if key.EnvironmentID != environmentID || key.ServiceID != service.ID || key.Purpose == "" {
				return ErrInvalidRequest
			}
			if _, assigned := leaseKeys[key]; !assigned {
				return ErrInvalidRequest
			}
			if _, duplicate := seenServicePorts[key]; duplicate {
				return ErrInvalidRequest
			}
			seenServicePorts[key] = struct{}{}
		}
	}
	for _, stage := range plan.ServiceStages {
		if len(stage) == 0 {
			return ErrInvalidRequest
		}
	}
	return nil
}

func flattenedServiceStages(plan ExecutionPlan) []ServiceLaunch {
	if len(plan.ServiceStages) == 0 {
		return plan.Services
	}
	count := 0
	for _, stage := range plan.ServiceStages {
		count += len(stage)
	}
	services := make([]ServiceLaunch, 0, count)
	for _, stage := range plan.ServiceStages {
		services = append(services, stage...)
	}
	return services
}

func pathsOverlap(left, right string) bool {
	leftToRight, leftError := filepath.Rel(left, right)
	rightToLeft, rightError := filepath.Rel(right, left)
	return leftError == nil && pathIsWithin(leftToRight) ||
		rightError == nil && pathIsWithin(rightToLeft)
}

func pathIsWithin(relative string) bool {
	return relative == "." || relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validPreparation(preparation PreparationSpec) bool {
	if preparation.ID == "" || len(preparation.ID) > 256 ||
		!filepath.IsAbs(preparation.Executable) || filepath.Clean(preparation.Executable) != preparation.Executable ||
		!filepath.IsAbs(preparation.Directory) || filepath.Clean(preparation.Directory) != preparation.Directory ||
		!filepath.IsAbs(preparation.RunDirectory) ||
		filepath.Clean(preparation.RunDirectory) != preparation.RunDirectory ||
		preparation.Timeout <= 0 || preparation.Timeout > 30*time.Minute {
		return false
	}
	for _, argument := range preparation.Arguments {
		if argument == "" || len(argument) > 1024*1024 || strings.ContainsRune(argument, 0) {
			return false
		}
	}
	seenEnvironment := make(map[string]struct{}, len(preparation.Environment))
	for _, variable := range preparation.Environment {
		name, _, found := strings.Cut(variable, "=")
		if !found || name == "" || strings.ContainsRune(variable, 0) {
			return false
		}
		if _, duplicate := seenEnvironment[name]; duplicate {
			return false
		}
		seenEnvironment[name] = struct{}{}
	}
	return true
}

func (coordinator *Coordinator) requireExecutionDependencies(plan ExecutionPlan) error {
	if (len(plan.Preparations) != 0 || len(plan.Initializations) != 0) && coordinator.preparations == nil {
		return ErrInvalidRequest
	}
	if plan.Projection != nil && coordinator.projections == nil {
		return ErrInvalidRequest
	}
	if len(plan.Infrastructure) != 0 && coordinator.infrastructure == nil {
		return ErrInvalidRequest
	}
	if len(plan.Services) != 0 && (coordinator.processes == nil || coordinator.readiness == nil) {
		return ErrInvalidRequest
	}
	return nil
}

func (coordinator *Coordinator) requireStopDependencies(result EnvironmentResult) error {
	if result.Projection != nil && coordinator.projections == nil {
		return ErrInvalidRequest
	}
	if len(result.Infrastructure) != 0 && coordinator.infrastructure == nil {
		return ErrInvalidRequest
	}
	if len(result.Services) != 0 && coordinator.processes == nil {
		return ErrInvalidRequest
	}
	return nil
}

func (coordinator *Coordinator) validateOwnedResult(result EnvironmentResult) error {
	if result.EnvironmentID == "" || result.RunID == "" {
		return ErrInvalidRequest
	}
	for _, lease := range result.Ports {
		if lease.Key.EnvironmentID != result.EnvironmentID || lease.Key.ServiceID == "" ||
			lease.Key.Purpose == "" {
			return ErrForeignOwnership
		}
	}
	if result.Projection != nil {
		if err := validateProjectionChange(result.EnvironmentID, result.RunID, *result.Projection); err != nil {
			return err
		}
	}
	for _, goal := range result.Infrastructure {
		if goal.Identity.Validate() != nil || goal.Identity.EnvironmentID != result.EnvironmentID ||
			goal.Identity.RunID != result.RunID {
			return ErrForeignOwnership
		}
	}
	for _, service := range result.Services {
		if !service.Owned || service.EnvironmentID != result.EnvironmentID || service.RunID != result.RunID ||
			service.ID == "" || !filepath.IsAbs(service.OwnershipPath) {
			return ErrForeignOwnership
		}
		if service.Process.EnvironmentID != result.EnvironmentID || service.Process.RunID != result.RunID ||
			service.Process.ServiceID != service.ID {
			return ErrForeignOwnership
		}
	}
	return nil
}

func validateProjectionChange(environmentID, runID string, change ProjectionChange) error {
	if !change.Owned || change.ID == "" || change.RollbackToken == "" ||
		change.EnvironmentID != environmentID || change.RunID != runID {
		return ErrForeignOwnership
	}
	return nil
}

func validateRollbackEntry(environmentID, runID string, entry RollbackEntry) error {
	if !entry.Armed {
		return nil
	}
	switch entry.Kind {
	case RollbackPorts:
		if len(entry.PortKeys) == 0 {
			return ErrInvalidRequest
		}
		for _, key := range entry.PortKeys {
			if key.EnvironmentID != environmentID || key.ServiceID == "" || key.Purpose == "" {
				return ErrForeignOwnership
			}
		}
	case RollbackProjection:
		if entry.Projection == nil {
			return ErrInvalidRequest
		}
		if err := validateProjectionChange(environmentID, runID, *entry.Projection); err != nil {
			return err
		}
	case RollbackInfrastructure:
		if len(entry.Infrastructure) == 0 {
			return ErrInvalidRequest
		}
		for _, goal := range entry.Infrastructure {
			if goal.Identity.Validate() != nil || goal.Identity.EnvironmentID != environmentID ||
				goal.Identity.RunID != runID {
				return ErrForeignOwnership
			}
		}
	case RollbackProcess:
		if entry.Process == nil || !entry.Process.Owned || entry.Process.EnvironmentID != environmentID ||
			entry.Process.RunID != runID || !filepath.IsAbs(entry.Process.OwnershipPath) {
			return ErrForeignOwnership
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

func transitionEnvironment(operation *OperationRecord, next domain.EnvironmentState) error {
	if err := domain.ValidateEnvironmentTransition(operation.EnvironmentState, next); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	operation.EnvironmentState = next
	return nil
}

func transitionOperation(operation *OperationRecord, next domain.OperationState) error {
	if err := domain.ValidateOperationTransition(operation.State, next); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidState, err)
	}
	operation.State = next
	return nil
}

func unleasedReservationKeys(
	reservations []portlease.Reservation,
	existing []portlease.Lease,
) []portlease.Key {
	existingKeys := make(map[portlease.Key]struct{}, len(existing))
	for _, lease := range existing {
		existingKeys[lease.Key] = struct{}{}
	}
	keys := make([]portlease.Key, 0, len(reservations))
	for _, reservation := range reservations {
		if _, exists := existingKeys[reservation.Key]; !exists {
			keys = append(keys, reservation.Key)
		}
	}
	return keys
}

func leasesForKeys(leases []portlease.Lease, keys []portlease.Key) []portlease.Lease {
	wanted := make(map[portlease.Key]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	result := make([]portlease.Lease, 0, len(keys))
	for _, lease := range leases {
		if _, exists := wanted[lease.Key]; exists {
			result = append(result, lease)
		}
	}
	return result
}

func leasesNotOwnedByRollback(leases []portlease.Lease, rollback []RollbackEntry) []portlease.Lease {
	released := make(map[portlease.Key]struct{})
	for _, entry := range rollback {
		if entry.Kind != RollbackPorts {
			continue
		}
		for _, key := range entry.PortKeys {
			released[key] = struct{}{}
		}
	}
	result := make([]portlease.Lease, 0, len(leases))
	for _, lease := range leases {
		if _, wasReleased := released[lease.Key]; !wasReleased {
			result = append(result, lease)
		}
	}
	return result
}

func cloneLeases(leases []portlease.Lease) []portlease.Lease {
	return append([]portlease.Lease(nil), leases...)
}

func clonePreparation(preparation PreparationSpec) PreparationSpec {
	copy := preparation
	copy.Arguments = append([]string(nil), preparation.Arguments...)
	copy.Environment = append([]string(nil), preparation.Environment...)
	return copy
}

func cloneGoals(goals []containerhost.Goal) []containerhost.Goal {
	cloned := append([]containerhost.Goal(nil), goals...)
	for index := range cloned {
		cloned[index].PortBindings = append([]containerhost.PortBinding(nil), goals[index].PortBindings...)
		cloned[index].Environment = append([]string(nil), goals[index].Environment...)
	}
	return cloned
}

func cloneProjection(change *ProjectionChange) *ProjectionChange {
	if change == nil {
		return nil
	}
	copy := *change
	return &copy
}

func cloneIntent(intent *PlanIntent) *PlanIntent {
	if intent == nil {
		return nil
	}
	copy := *intent
	copy.ServiceIDs = append([]string(nil), intent.ServiceIDs...)
	return &copy
}

func cloneSource(source *SourceSnapshot) *SourceSnapshot {
	if source == nil {
		return nil
	}
	copy := *source
	return &copy
}

func cloneService(service *ServiceResult) *ServiceResult {
	if service == nil {
		return nil
	}
	copy := *service
	copy.Process.Members = append([]processhost.ProcessIdentity(nil), service.Process.Members...)
	return &copy
}

func cloneEnvironment(result EnvironmentResult) EnvironmentResult {
	copy := result
	copy.Ports = cloneLeases(result.Ports)
	copy.Projection = cloneProjection(result.Projection)
	copy.Infrastructure = cloneGoals(result.Infrastructure)
	copy.Services = append([]ServiceResult(nil), result.Services...)
	copy.Source = cloneSource(result.Source)
	for index := range copy.Services {
		copy.Services[index].Process.Members = append(
			copy.Services[index].Process.Members[:0:0], result.Services[index].Process.Members...,
		)
	}
	return copy
}

func environmentPointer(result EnvironmentResult) *EnvironmentResult {
	copy := cloneEnvironment(result)
	return &copy
}

func hasResources(result EnvironmentResult) bool {
	return len(result.Ports) != 0 || result.Projection != nil ||
		len(result.Infrastructure) != 0 || len(result.Services) != 0
}
