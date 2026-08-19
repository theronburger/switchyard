package marketplace

type RuntimeTarget struct {
	ID          string
	DisplayName string
	Risk        string
	WarnOnStart bool
}

type RuntimeService struct {
	ID          string
	DisplayName string
	Kind        ServiceKind
}

type RuntimeServiceSource struct {
	ServiceID string
	Root      string
}

func DefaultRuntimeTargetID() string {
	return "testing"
}

func KnownRuntimeTargets() []RuntimeTarget {
	return []RuntimeTarget{
		{ID: "development", DisplayName: "Development", Risk: "standard"},
		{ID: "testing", DisplayName: "Testing", Risk: "standard"},
		{ID: "demo", DisplayName: "Demo", Risk: "elevated", WarnOnStart: true},
		{ID: "production", DisplayName: "Production", Risk: "production", WarnOnStart: true},
	}
}

func KnownRuntimeServices() []RuntimeService {
	return []RuntimeService{
		{ID: "api", DisplayName: "API", Kind: ServiceKindAPI},
		{ID: "app", DisplayName: "App", Kind: ServiceKindWeb},
		{ID: "auth-service", DisplayName: "Auth Service", Kind: ServiceKindAPI},
		{ID: "company-service", DisplayName: "Company Service", Kind: ServiceKindAPI},
		{ID: "donation-batch-service", DisplayName: "Donation Batch Service", Kind: ServiceKindAPI},
		{ID: "donation-service", DisplayName: "Donation Service", Kind: ServiceKindAPI},
		{ID: "email-service", DisplayName: "Email Service", Kind: ServiceKindAPI},
		{ID: "graph-service", DisplayName: "Graph Service", Kind: ServiceKindAPI},
		{ID: "logged-time-service", DisplayName: "Logged Time Service", Kind: ServiceKindAPI},
		{ID: "nonprofit-service", DisplayName: "Nonprofit Service", Kind: ServiceKindAPI},
		{ID: "notification-service", DisplayName: "Notification Service", Kind: ServiceKindAPI},
		{ID: "opportunity-service", DisplayName: "Opportunity Service", Kind: ServiceKindAPI},
		{ID: "organizer", DisplayName: "Organizer", Kind: ServiceKindWeb},
		{ID: "payroll-service", DisplayName: "Payroll Service", Kind: ServiceKindAPI},
		{ID: "report-service", DisplayName: "Report Service", Kind: ServiceKindAPI},
		{ID: "slack-service", DisplayName: "Slack Service", Kind: ServiceKindAPI},
		{ID: "time-off-service", DisplayName: "Time Off Service", Kind: ServiceKindAPI},
		{ID: "wallet", DisplayName: "Wallet", Kind: ServiceKindAPI},
	}
}

func RuntimeServiceSources() []RuntimeServiceSource {
	return []RuntimeServiceSource{
		{ServiceID: "api", Root: "api"},
		{ServiceID: "app", Root: "app"},
		{ServiceID: "organizer", Root: "organizer"},
		{ServiceID: "auth-service", Root: "services/auth-service"},
		{ServiceID: "company-service", Root: "services/company-service"},
		{ServiceID: "donation-batch-service", Root: "services/donation-batch-service"},
		{ServiceID: "donation-service", Root: "services/donation-service"},
		{ServiceID: "email-service", Root: "services/email-service"},
		{ServiceID: "graph-service", Root: "services/graph-service"},
		{ServiceID: "logged-time-service", Root: "services/logged-time-service"},
		{ServiceID: "nonprofit-service", Root: "services/nonprofit-service"},
		{ServiceID: "notification-service", Root: "services/notification-service"},
		{ServiceID: "opportunity-service", Root: "services/opportunity-service"},
		{ServiceID: "payroll-service", Root: "services/payroll-service"},
		{ServiceID: "report-service", Root: "services/report-service"},
		{ServiceID: "slack-service", Root: "services/slack-service"},
		{ServiceID: "time-off-service", Root: "services/time-off-service"},
		{ServiceID: "wallet", Root: "services/wallet"},
	}
}

func RuntimeTargetEnvironment(targetID string) ([]EnvironmentVariable, bool) {
	known := false
	for _, target := range KnownRuntimeTargets() {
		if target.ID == targetID {
			known = true
			break
		}
	}
	if !known {
		return nil, false
	}
	buildStage := targetID
	if targetID != "development" {
		buildStage = "workload"
	}
	return []EnvironmentVariable{
		{Name: "BUILD_STAGE", Value: buildStage},
		{Name: "DEPLOYMENT_ENVIRONMENT", Value: targetID},
		{Name: "NODE_ENV", Value: targetID},
	}, true
}
