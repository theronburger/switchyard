package contractv1

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maximumOpaqueIDBytes       = 256
	maximumIdempotencyKeyBytes = 512
	maximumRequestedServices   = 32
)

func (snapshot StatusSnapshot) Validate() error {
	if snapshot.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema version: got %d, want %d", snapshot.SchemaVersion, SchemaVersion)
	}
	if snapshot.SnapshotRevision < 0 {
		return fmt.Errorf("snapshot revision must not be negative")
	}
	if snapshot.Daemon.InstanceID == "" {
		return fmt.Errorf("daemon instance id is required")
	}
	if snapshot.Repositories == nil || snapshot.Environments == nil ||
		snapshot.Operations == nil || snapshot.Alerts == nil {
		return fmt.Errorf("status snapshot collections must be JSON arrays, not null")
	}

	repositories := make(map[string]Repository, len(snapshot.Repositories))
	worktrees := make(map[string]Worktree)
	for _, repository := range snapshot.Repositories {
		if repository.ID == "" {
			return fmt.Errorf("repository id is required")
		}
		if _, exists := repositories[repository.ID]; exists {
			return fmt.Errorf("duplicate repository id %q", repository.ID)
		}
		if repository.Worktrees == nil {
			return fmt.Errorf("repository %q worktrees must be a JSON array, not null", repository.ID)
		}
		repositories[repository.ID] = repository
		for _, worktree := range repository.Worktrees {
			if worktree.ID == "" {
				return fmt.Errorf("worktree id is required in repository %q", repository.ID)
			}
			if _, exists := worktrees[worktree.ID]; exists {
				return fmt.Errorf("duplicate worktree id %q", worktree.ID)
			}
			worktrees[worktree.ID] = worktree
		}
	}

	environments := make(map[string]Environment, len(snapshot.Environments))
	for _, environment := range snapshot.Environments {
		if environment.ID == "" {
			return fmt.Errorf("environment id is required")
		}
		if _, exists := environments[environment.ID]; exists {
			return fmt.Errorf("duplicate environment id %q", environment.ID)
		}
		if _, exists := repositories[environment.RepositoryID]; !exists {
			return fmt.Errorf("environment %q references unknown repository %q", environment.ID, environment.RepositoryID)
		}
		if _, exists := worktrees[environment.WorktreeID]; !exists {
			return fmt.Errorf("environment %q references unknown worktree %q", environment.ID, environment.WorktreeID)
		}
		if environment.Revision < 0 {
			return fmt.Errorf("environment %q revision must not be negative", environment.ID)
		}
		if environment.Services == nil || environment.PortLeases == nil ||
			environment.InfrastructureLeases == nil || environment.AttentionAlertIDs == nil {
			return fmt.Errorf("environment %q collections must be JSON arrays, not null", environment.ID)
		}
		if environment.URLs == nil {
			return fmt.Errorf("environment %q urls must be a JSON object, not null", environment.ID)
		}
		if err := validateEnvironment(environment); err != nil {
			return err
		}
		environments[environment.ID] = environment
	}

	alerts := make(map[string]Alert, len(snapshot.Alerts))
	for _, alert := range snapshot.Alerts {
		if alert.ID == "" {
			return fmt.Errorf("alert id is required")
		}
		if _, exists := alerts[alert.ID]; exists {
			return fmt.Errorf("duplicate alert id %q", alert.ID)
		}
		if alert.EnvironmentID != "" {
			if _, exists := environments[alert.EnvironmentID]; !exists {
				return fmt.Errorf("alert %q references unknown environment %q", alert.ID, alert.EnvironmentID)
			}
		}
		alerts[alert.ID] = alert
	}

	for _, environment := range snapshot.Environments {
		for _, alertID := range environment.AttentionAlertIDs {
			if _, exists := alerts[alertID]; !exists {
				return fmt.Errorf("environment %q references unknown alert %q", environment.ID, alertID)
			}
		}
	}

	return nil
}

func validateEnvironment(environment Environment) error {
	services := make(map[string]Service, len(environment.Services))
	for _, service := range environment.Services {
		if service.ID == "" {
			return fmt.Errorf("service id is required in environment %q", environment.ID)
		}
		if _, exists := services[service.ID]; exists {
			return fmt.Errorf("duplicate service id %q in environment %q", service.ID, environment.ID)
		}
		if service.PortLeaseIDs == nil {
			return fmt.Errorf("service %q port leases must be a JSON array, not null", service.ID)
		}
		services[service.ID] = service
	}

	leases := make(map[string]PortLease, len(environment.PortLeases))
	for _, lease := range environment.PortLeases {
		if lease.ID == "" {
			return fmt.Errorf("port lease id is required in environment %q", environment.ID)
		}
		if _, exists := leases[lease.ID]; exists {
			return fmt.Errorf("duplicate port lease id %q in environment %q", lease.ID, environment.ID)
		}
		if _, exists := services[lease.ServiceID]; !exists {
			return fmt.Errorf("port lease %q references unknown service %q", lease.ID, lease.ServiceID)
		}
		if lease.Port < 1 || lease.Port > 65535 {
			return fmt.Errorf("port lease %q has invalid port %d", lease.ID, lease.Port)
		}
		leases[lease.ID] = lease
	}

	for _, service := range environment.Services {
		for _, leaseID := range service.PortLeaseIDs {
			if _, exists := leases[leaseID]; !exists {
				return fmt.Errorf("service %q references unknown port lease %q", service.ID, leaseID)
			}
		}
	}

	return nil
}

func (request MutationRequest) Validate() error {
	if request.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema version: got %d, want %d", request.SchemaVersion, SchemaVersion)
	}
	if !validOpaqueValue(request.RequestID, maximumOpaqueIDBytes) {
		return fmt.Errorf("request id is invalid")
	}
	if !validOpaqueValue(request.IdempotencyKey, maximumIdempotencyKeyBytes) {
		return fmt.Errorf("idempotency key is invalid")
	}
	if request.ExpectedEnvironmentRevision != nil && *request.ExpectedEnvironmentRevision < 0 {
		return fmt.Errorf("expected environment revision must not be negative")
	}
	return nil
}

func (request StartEnvironmentRequest) Validate() error {
	if err := request.MutationRequest.Validate(); err != nil {
		return err
	}
	if !validOpaqueValue(request.WorktreeID, maximumOpaqueIDBytes) {
		return fmt.Errorf("worktree id is invalid")
	}
	if request.ServiceIDs == nil || len(request.ServiceIDs) == 0 ||
		len(request.ServiceIDs) > maximumRequestedServices {
		return fmt.Errorf("service ids must be a non-empty bounded JSON array")
	}
	seen := make(map[string]struct{}, len(request.ServiceIDs))
	for _, serviceID := range request.ServiceIDs {
		if !validOpaqueValue(serviceID, maximumOpaqueIDBytes) {
			return fmt.Errorf("service id is invalid")
		}
		if _, duplicate := seen[serviceID]; duplicate {
			return fmt.Errorf("duplicate service id %q", serviceID)
		}
		seen[serviceID] = struct{}{}
	}
	return nil
}

func (request StopEnvironmentRequest) Validate() error {
	return request.MutationRequest.Validate()
}

func (receipt MutationReceipt) Validate() error {
	if receipt.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema version: got %d, want %d", receipt.SchemaVersion, SchemaVersion)
	}
	if !validOpaqueValue(receipt.RequestID, maximumOpaqueIDBytes) ||
		!validOpaqueValue(receipt.OperationID, maximumOpaqueIDBytes) {
		return fmt.Errorf("mutation receipt identifiers are invalid")
	}
	if receipt.AcceptedAt.IsZero() {
		return fmt.Errorf("mutation receipt acceptance time is required")
	}
	if receipt.EnvironmentID != "" && !validOpaqueValue(receipt.EnvironmentID, maximumOpaqueIDBytes) {
		return fmt.Errorf("mutation receipt environment id is invalid")
	}
	return nil
}

func validOpaqueValue(value string, maximumBytes int) bool {
	if value == "" || len(value) > maximumBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
