package contractv1

import "fmt"

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

	repositories := make(map[string]Repository, len(snapshot.Repositories))
	worktrees := make(map[string]Worktree)
	for _, repository := range snapshot.Repositories {
		if repository.ID == "" {
			return fmt.Errorf("repository id is required")
		}
		if _, exists := repositories[repository.ID]; exists {
			return fmt.Errorf("duplicate repository id %q", repository.ID)
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
